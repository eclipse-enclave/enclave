// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// discoverIdeBridgePorts scans ~/.claude/ide/ for *.lock files and extracts
// port numbers from the filenames. Each lock file is named <port>.lock.
func discoverIdeBridgePorts(hostHome string) []string {
	ideDir := filepath.Join(hostHome, ".claude", "ide")
	entries, err := os.ReadDir(ideDir)
	if err != nil {
		return nil
	}
	var ports []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".lock") {
			continue
		}
		port := strings.TrimSuffix(name, ".lock")
		if util.IsPortNumber(port) {
			ports = append(ports, port)
		}
	}
	return ports
}

// mergeBridgePorts combines auto-discovered IDE bridge ports with explicitly
// specified --bridge-port values, deduplicating the result.
func mergeBridgePorts(discovered, explicit []string) []string {
	if len(explicit) == 0 {
		return discovered
	}
	seen := make(map[string]bool, len(discovered))
	for _, p := range discovered {
		seen[p] = true
	}
	merged := append([]string{}, discovered...)
	for _, p := range explicit {
		if !seen[p] {
			merged = append(merged, p)
			seen[p] = true
		}
	}
	return merged
}

// addIdeBridgeMount adds a read-only bind mount for the host's ~/.claude/ide/
// directory and sets the IDE bridge ports environment variable.
func (r *Runtime) addIdeBridgeMount(mounts *mountAccumulator) {
	if len(r.ideBridgePorts) == 0 {
		return
	}
	hostIdeDir := filepath.Join(r.host.Home, ".claude", "ide")
	if util.PathExists(hostIdeDir) {
		containerIdeDir := filepath.Join(r.containerHome, ".claude", "ide")
		mounts.AddMount(bindMount(hostIdeDir, containerIdeDir, true))
	}
	mounts.AddEnv(model.EnvIdeBridgePorts, strings.Join(r.ideBridgePorts, ","))
	r.logBridgePorts()
}

// logBridgePorts logs explicit --bridge-port ports and auto-discovered IDE
// ports as separate lines so users can see what is being forwarded and why.
func (r *Runtime) logBridgePorts() {
	explicit := make(map[string]bool, len(r.run.BridgePorts))
	for _, p := range r.run.BridgePorts {
		explicit[p] = true
	}
	var idePorts, cliPorts []string
	for _, p := range r.ideBridgePorts {
		if explicit[p] {
			cliPorts = append(cliPorts, p)
		} else {
			idePorts = append(idePorts, p)
		}
	}
	if len(cliPorts) > 0 {
		logx.Infof("Bridge ports (host → container): %s", strings.Join(cliPorts, ", "))
	}
	if len(idePorts) > 0 {
		logx.Infof("IDE bridge ports (auto-discovered): %s", strings.Join(idePorts, ", "))
	}
}
