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

	meta, err := renderBundleMeta(strings.TrimSpace(tool), files.resolver, files.domainCount, time.Now().UTC())
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
	if strings.EqualFold(strings.TrimSpace(policy.Mode), model.NetworkModeUnrestricted) {
		return empty, errors.New("live gateway apply does not support unrestricted mode")
	}

	domains, denied, err := network.EffectiveRenderDomainsForTool(policy, tool)
	if err != nil {
		return empty, err
	}
	resolver := network.FirstIPv4Resolver(policy.Resolvers)
	dnsmasq := network.RenderDNSMasqConfig(domains, denied, resolver)
	domainsTxt := joinLines(domains)
	deniedTxt := joinLines(denied)

	files := renderedBundleFiles{
		dnsmasq:     []byte(dnsmasq),
		domains:     []byte(domainsTxt),
		denied:      []byte(deniedTxt),
		resolver:    resolver,
		domainCount: len(domains),
	}
	if err := validateRenderedBundle(files.dnsmasq); err != nil {
		return empty, err
	}
	return files, nil
}

func renderBundleMeta(tool string, resolver string, domainCount int, generatedAt time.Time) ([]byte, error) {
	metaValue := bundleMeta{
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Generation:  generatedAt.Format(time.RFC3339Nano),
		Mode:        model.NetworkModeRestricted,
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

func validateRenderedBundle(dnsmasq []byte) error {
	content := string(dnsmasq)
	if !strings.Contains(content, "no-resolv") || !strings.Contains(content, "address=/#/") {
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
