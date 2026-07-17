// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/gateway/bundle"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/policy"
	"enclave/internal/util"
)

// gatewayManagerForInput selects the backend and returns its gateway-reload
// surface. The second return is a non-zero exit code on failure.
func gatewayManagerForInput(input *CommandInput) (backend.GatewayManager, int) {
	host, err := resolveHost()
	if err != nil {
		logx.Warnf("Failed to resolve host for backend selection: %v", err)
	}
	be, err := selectBackend(input.Options, dockerBackendOptions(host, input.Ctx.Paths, model.BuildOptions{}, input.Options.RunOptions))
	if err != nil {
		logx.Errorf("%v", err)
		return nil, 1
	}
	manager, ok := be.(backend.GatewayManager)
	if !ok {
		logx.Errorf("backend %s does not support network gateway management", be.Name())
		return nil, 1
	}
	return manager, 0
}

func discoverGatewayTargets(input *CommandInput, manager backend.GatewayManager, allRunning bool) ([]backend.GatewayInfo, string, error) {
	filter := backend.GatewayFilter{}
	scopeLabel := "all running gateways"
	if !allRunning {
		project, err := input.Ctx.Project()
		if err != nil {
			return nil, "", err
		}
		filter.Tool = input.Options.Tool
		filter.ProjectHash = project.Hash
		filter.WorkspaceID = util.WorkspaceIdentityHash(project.RealDir, project.Dir)
		scopeLabel = fmt.Sprintf("current project/tool (%s/%s)", project.Hash, input.Options.Tool)
	}

	targets, err := manager.ListGateways(context.Background(), filter)
	if err != nil {
		return nil, "", err
	}
	return targets, scopeLabel, nil
}

func desiredGatewayBundleHash(resolver policy.EffectiveResolver, target backend.GatewayInfo, targetProjectDir string) (string, error) {
	if strings.TrimSpace(target.Tool) == "" {
		return "", fmt.Errorf("missing tool label on gateway %s", target.ShortID())
	}
	if strings.TrimSpace(target.ProjectHash) == "" {
		return "", fmt.Errorf("missing project hash label on gateway %s", target.ShortID())
	}
	resolved, err := resolver.Resolve(policy.ResolveInput{
		ProjectDir:  targetProjectDir,
		ProjectHash: target.ProjectHash,
		Tool:        target.Tool,
	})
	if err != nil {
		return "", err
	}

	return bundle.ConfigBundleHash(bundle.BundleWriteConfig{
		Policy: resolved.Effective,
		Tool:   target.Tool,
	})
}

func hashGatewayBundleDir(dir string) (string, error) {
	paths := []string{
		filepath.Join(dir, filepath.Base(model.GatewayConfigDNSMasqPath)),
		filepath.Join(dir, filepath.Base(model.GatewayConfigDomainsPath)),
	}

	hasher := sha256.New()
	for _, path := range paths {
		// #nosec G304 -- path is assembled from known bundle filenames under an internal caller-provided directory.
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read bundle file %s: %w", path, err)
		}
		hasher.Write([]byte(filepath.Base(path)))
		hasher.Write([]byte{0})
		hasher.Write(data)
		hasher.Write([]byte{0})
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func readGatewayBundleGeneration(bundleDir string) (string, error) {
	metaPath := filepath.Join(bundleDir, filepath.Base(model.GatewayConfigMetaPath))
	// #nosec G304 -- path is assembled from known bundle metadata filename in internal gateway config dir.
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return "", fmt.Errorf("read bundle metadata %s: %w", metaPath, err)
	}
	var meta struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("parse bundle metadata %s: %w", metaPath, err)
	}
	generation := strings.TrimSpace(meta.Generation)
	if generation == "" {
		return "", fmt.Errorf("bundle metadata %s missing generation", metaPath)
	}
	return generation, nil
}

func resolveGatewayTargetProjectDir(currentProject model.Project, target backend.GatewayInfo) (string, error) {
	if strings.TrimSpace(target.ProjectHash) == "" {
		return "", fmt.Errorf("missing project hash label on gateway %s", target.ShortID())
	}
	projectDir := strings.TrimSpace(target.ProjectDir)
	if projectDir != "" {
		return projectDir, nil
	}
	if target.ProjectHash == currentProject.Hash {
		return currentProject.Dir, nil
	}
	return "", fmt.Errorf("gateway %s missing %s label; restart this session with the latest enclave", target.ShortID(), model.GatewayLabelProjectDir)
}
