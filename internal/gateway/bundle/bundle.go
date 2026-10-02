// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package bundle renders, writes, and reads the gateway DNS config bundle
// (dnsmasq.conf, domains.txt, meta.json). It deliberately avoids importing
// internal/docker so the in-container gateway proxy can read the allowed
// domains without depending on host-side Docker helpers.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/model"
	"enclave/internal/network"
	"enclave/internal/util"
)

type BundleWriteConfig struct {
	Dir    string
	Policy network.EffectivePolicy
	Tool   string
}

type renderedBundle struct {
	dnsmasq []byte
	domains []byte
	denied  []byte
	meta    []byte
}

type renderedBundleFiles struct {
	dnsmasq     []byte
	domains     []byte
	denied      []byte
	resolver    string
	domainCount int
}

type bundleMeta struct {
	GeneratedAt string `json:"generated_at"`
	Generation  string `json:"generation"`
	Mode        string `json:"mode"`
	ModeSource  string `json:"mode_source,omitempty"`
	Tool        string `json:"tool"`
	Resolver    string `json:"resolver"`
	DomainCount int    `json:"domain_count"`
}

func WriteConfigBundle(cfg BundleWriteConfig) error {
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		return errors.New("gateway config bundle dir is required")
	}

	bundle, err := renderConfigBundle(cfg.Policy, cfg.Tool)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create gateway config dir: %w", err)
	}

	if err := util.WriteFileAtomic(filepath.Join(dir, filepath.Base(model.GatewayConfigDNSMasqPath)), bundle.dnsmasq, 0o644); err != nil {
		return fmt.Errorf("write dnsmasq bundle: %w", err)
	}
	if err := util.WriteFileAtomic(filepath.Join(dir, filepath.Base(model.GatewayConfigDomainsPath)), bundle.domains, 0o644); err != nil {
		return fmt.Errorf("write domains bundle: %w", err)
	}
	if err := util.WriteFileAtomic(filepath.Join(dir, filepath.Base(model.GatewayConfigDeniedPath)), bundle.denied, 0o644); err != nil {
		return fmt.Errorf("write denied bundle: %w", err)
	}
	if err := util.WriteFileAtomic(filepath.Join(dir, filepath.Base(model.GatewayConfigMetaPath)), bundle.meta, 0o644); err != nil {
		return fmt.Errorf("write meta bundle: %w", err)
	}
	return util.SyncDir(dir)
}

func ConfigBundleHash(cfg BundleWriteConfig) (string, error) {
	files, err := renderBundleFiles(cfg.Policy, cfg.Tool)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	hasher.Write([]byte(filepath.Base(model.GatewayConfigDNSMasqPath)))
	hasher.Write([]byte{0})
	hasher.Write(files.dnsmasq)
	hasher.Write([]byte{0})
	hasher.Write([]byte(filepath.Base(model.GatewayConfigDomainsPath)))
	hasher.Write([]byte{0})
	hasher.Write(files.domains)
	hasher.Write([]byte{0})
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func renderConfigBundle(policy network.EffectivePolicy, tool string) (renderedBundle, error) {
	var empty renderedBundle
	files, err := renderBundleFiles(policy, tool)
	if err != nil {
		return empty, err
	}

	meta, err := renderBundleMeta(policy, strings.TrimSpace(tool), files.resolver, files.domainCount, time.Now().UTC())
	if err != nil {
		return empty, err
	}

	return renderedBundle{
		dnsmasq: files.dnsmasq,
		domains: files.domains,
		denied:  files.denied,
		meta:    meta,
	}, nil
}

func renderBundleFiles(policy network.EffectivePolicy, tool string) (renderedBundleFiles, error) {
	var empty renderedBundleFiles
	mode, err := normalizeMode(policy.Mode)
	if err != nil {
		return empty, err
	}
	policy.Mode = mode
	var domains, denied []string
	if mode == model.NetworkModeRestricted {
		domains, denied, err = network.EffectiveRenderDomainsForTool(policy, tool)
	}
	if err != nil {
		return empty, err
	}
	resolver := network.FirstIPv4Resolver(policy.Resolvers)
	dnsmasq, err := network.RenderDnsmasqConfigForTool(policy, tool, resolver)
	if err != nil {
		return empty, err
	}
	domainsTxt := joinLines(domains)
	deniedTxt := joinLines(denied)

	files := renderedBundleFiles{
		dnsmasq:     []byte(dnsmasq),
		domains:     []byte(domainsTxt),
		denied:      []byte(deniedTxt),
		resolver:    resolver,
		domainCount: len(domains),
	}
	if err := validateRenderedBundle(files.dnsmasq, mode); err != nil {
		return empty, err
	}
	return files, nil
}

func renderBundleMeta(policy network.EffectivePolicy, tool string, resolver string, domainCount int, generatedAt time.Time) ([]byte, error) {
	mode, err := normalizeMode(policy.Mode)
	if err != nil {
		return nil, err
	}
	metaValue := bundleMeta{
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Generation:  generatedAt.Format(time.RFC3339Nano),
		Mode:        mode,
		ModeSource:  policy.ModeSource,
		Tool:        tool,
		Resolver:    resolver,
		DomainCount: domainCount,
	}
	metaJSON, err := json.MarshalIndent(metaValue, "", "  ")
	if err != nil {
		return nil, err
	}
	metaJSON = append(metaJSON, '\n')
	return metaJSON, nil
}

func validateRenderedBundle(dnsmasq []byte, mode string) error {
	content := string(dnsmasq)
	complete := strings.Contains(content, "no-resolv") && strings.Contains(content, "no-poll")
	if mode == model.NetworkModeUnrestricted {
		complete = complete && strings.Contains(content, "\nserver=") && !strings.Contains(content, "address=")
	} else {
		complete = complete && strings.Contains(content, "address=/#/")
	}
	if !complete {
		return errors.New("rendered dnsmasq bundle is incomplete")
	}
	return nil
}

func joinLines(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, "\n") + "\n"
}

// ReadDeniedDomains parses the bundle's denied.txt into canonical domains. A
// missing file yields no domains (older bundles predate the deny list), so the
// proxy stays backward-compatible; a malformed line is a hard error.
func ReadDeniedDomains(path string) ([]string, error) {
	// #nosec G304 -- path is resolved from the internal gateway bundle layout.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read denied file %s: %w", path, err)
	}
	seen := map[string]struct{}{}
	domains := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		domain, err := network.CanonicalPolicyDomain(line)
		if err != nil {
			return nil, fmt.Errorf("parse denied file %s: %w", path, err)
		}
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return domains, nil
}

func ReadAllowedDomains(path string) ([]string, error) {
	// #nosec G304 -- path is resolved from the internal gateway bundle layout.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read domains file %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	domains := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		domain, err := network.CanonicalPolicyDomain(line)
		if err != nil {
			return nil, fmt.Errorf("parse domains file %s: %w", path, err)
		}
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return domains, nil
}

// PolicyState records the startup destination-policy choice and its source.
// The allow-all-network source remains a session override during live reload.
type PolicyState struct {
	Mode       string `json:"mode"`
	ModeSource string `json:"mode_source,omitempty"`
}

// ReadMode reads the destination-policy mode from trusted gateway metadata.
// Missing or malformed metadata fails closed; an absent mode is not unrestricted.
func ReadMode(path string) (string, error) {
	state, err := ReadPolicyState(path)
	return state.Mode, err
}

func ReadPolicyState(path string) (PolicyState, error) {
	var empty PolicyState
	// #nosec G304 -- path is resolved from the internal gateway bundle layout.
	data, err := os.ReadFile(path)
	if err != nil {
		return empty, fmt.Errorf("read gateway metadata: %w", err)
	}
	var meta bundleMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return empty, fmt.Errorf("parse gateway metadata: %w", err)
	}
	if meta.Mode == "" {
		return empty, errors.New("gateway metadata mode is missing")
	}
	mode, err := normalizeMode(meta.Mode)
	if err != nil {
		return empty, err
	}
	switch meta.ModeSource {
	case "", "default", "global", "project":
	case "allow-all-network":
		if mode != model.NetworkModeUnrestricted {
			return empty, errors.New("allow-all-network metadata requires unrestricted mode")
		}
	default:
		return empty, errors.New("invalid gateway destination policy source")
	}
	return PolicyState{Mode: mode, ModeSource: meta.ModeSource}, nil
}

func normalizeMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", model.NetworkModeRestricted:
		return model.NetworkModeRestricted, nil
	case model.NetworkModeUnrestricted:
		return model.NetworkModeUnrestricted, nil
	default:
		return "", errors.New("invalid gateway destination policy mode")
	}
}
