// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/gateway"
	"enclave/internal/gateway/bundle"
	"enclave/internal/model"
	"enclave/internal/network"
)

func (b *Backend) startGateway(_ context.Context, req backend.Request) (string, func(), error) {
	gatewayConfigDir := config.HostProjectGatewayConfigDir(b.opts.Host.Home, req.Session.ProjectHash, req.Session.Tool)
	policy := network.EffectivePolicy{
		Mode:          model.NetworkModeRestricted,
		Domains:       append([]string(nil), req.Network.Egress.AllowedDomains...),
		DeniedDomains: append([]string(nil), req.Network.Egress.DeniedDomains...),
		Resolvers:     append([]string(nil), req.Network.Egress.Resolvers...),
	}
	if err := bundle.WriteConfigBundle(bundle.BundleWriteConfig{Dir: gatewayConfigDir, Policy: policy, Tool: req.Session.Tool}); err != nil {
		return "", nil, fmt.Errorf("failed to write gateway config bundle: %w", err)
	}

	var tempFiles []string
	secretReleaseFile, err := writeSecretReleaseConfig(req.Secrets)
	if err != nil {
		return "", nil, err
	}
	if secretReleaseFile != "" {
		tempFiles = append(tempFiles, secretReleaseFile)
	}

	if err := os.MkdirAll(config.HostTLSHostsDir(b.opts.Host.Home), 0o700); err != nil {
		cleanupFiles(tempFiles)
		return "", nil, fmt.Errorf("failed to initialize gateway TLS hosts cache dir: %w", err)
	}

	workspaceID := workspaceIDFromSession(req.Session, b.opts.ProjectDir)
	startConfig := gateway.StartConfig{
		Paths:             b.opts.Paths,
		Profile:           model.Profile{Name: req.Session.Tool},
		AllowlistPath:     req.Network.Egress.AllowlistPath,
		ContainerName:     req.Session.Name,
		ForceRebuild:      b.opts.ForceRebuild,
		NoRebuild:         b.opts.NoRebuild,
		NetworkLogMode:    b.opts.NetworkLogMode,
		NetworkLogPath:    config.HostProjectNetworkLogPath(b.opts.Host.Home, req.Session.ProjectHash, req.Session.Tool),
		GatewayConfigDir:  gatewayConfigDir,
		PortBindings:      portMap(req.Ports),
		ExposedPorts:      portSet(req.Ports),
		LoopbackPorts:     append([]string(nil), req.Network.LoopbackPorts...),
		IdeBridgePorts:    append([]string(nil), req.Network.IdeBridgePorts...),
		Home:              b.opts.Host.Home,
		ProjectDir:        req.Session.Worktree,
		WorkspaceID:       workspaceID,
		ProjectHash:       req.Session.ProjectHash,
		SecretReleaseFile: secretReleaseFile,
		TLSRootDir:        config.HostTLSDir(b.opts.Host.Home),
	}
	result, err := gateway.Start(startConfig)
	if err != nil {
		cleanupFiles(tempFiles)
		return "", nil, fmt.Errorf("failed to start DNS gateway: %w", err)
	}
	tempFiles = append(tempFiles, result.TempFiles...)
	cleanup := func() {
		gateway.Stop(req.Session.Name)
		cleanupFiles(tempFiles)
	}
	return result.ContainerName, cleanup, nil
}

func workspaceIDFromSession(session backend.SessionMeta, projectDir string) string {
	workspaceID := strings.TrimSpace(session.RealWorktree)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(projectDir)
	}
	if workspaceID == "" {
		workspaceID = session.Worktree
	}
	return workspaceID
}

func writeSecretReleaseConfig(secrets []backend.SecretRelease) (string, error) {
	entries := make([]model.SecretReleaseEntry, 0, len(secrets))
	for _, secret := range secrets {
		if secret.HTTP == nil {
			continue
		}
		entries = append(entries, model.SecretReleaseEntry{
			SecretID:    secret.SecretID,
			Placeholder: secret.Placeholder,
			Value:       secret.Value,
			Hosts:       append([]string(nil), secret.HTTP.Hosts...),
			Header:      secret.HTTP.Header,
			Format:      secret.HTTP.Format,
		})
	}
	if len(entries) == 0 {
		return "", nil
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("marshal secret release entries: %w", err)
	}
	file, err := os.CreateTemp("", "enclave-secret-release-*.json")
	if err != nil {
		return "", fmt.Errorf("create temp file for secret release config: %w", err)
	}
	tmpPath := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath is created by os.CreateTemp.
		return "", fmt.Errorf("write secret release config: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath is created by os.CreateTemp.
		return "", fmt.Errorf("close secret release config: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil { // #nosec G302 G703 -- tmpPath is created by os.CreateTemp.
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath is created by os.CreateTemp.
		return "", fmt.Errorf("chmod secret release config: %w", err)
	}
	return tmpPath, nil
}

func cleanupFiles(paths []string) {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		_ = os.Remove(filepath.Clean(path)) // #nosec G304 G703 -- paths are temp files created by this process or gateway.
	}
}
