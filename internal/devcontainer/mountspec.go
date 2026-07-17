// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package devcontainer

import (
	"fmt"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	yaconfig "enclave/internal/config"
	"enclave/internal/util"
)

// ParseMountSpec parses a Docker `--mount`-style spec (comma-separated
// key=value pairs) into a backend.Mount. Bind sources are tilde-expanded and,
// when not absolute, resolved relative to projectDir. It is shared by the
// runtime (top-level devcontainer mounts/workspaceMount) and the Docker backend
// (runArg --mount) so the parsing and the security check below have one home.
func ParseMountSpec(spec string, projectDir string, home string) (backend.Mount, error) {
	if strings.TrimSpace(spec) == "" {
		return backend.Mount{}, fmt.Errorf("mount spec is empty")
	}
	parts := strings.Split(spec, ",")
	m := backend.Mount{Type: backend.MountTypeVolume}
	readOnly := false
	var source string
	var target string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			key := strings.ToLower(strings.TrimSpace(kv[0]))
			value := strings.TrimSpace(kv[1])
			switch key {
			case "type":
				m.Type = backend.MountType(value)
			case "src", "source":
				source = value
			case "dst", "destination", "target":
				target = value
			case "readonly", "ro":
				readOnly = value == "true" || value == "1" || value == "" || value == "ro"
			case "readwrite", "rw":
				readOnly = false
			}
			continue
		}
		switch strings.ToLower(part) {
		case "readonly", "ro":
			readOnly = true
		case "readwrite", "rw":
			readOnly = false
		}
	}
	if target == "" {
		return backend.Mount{}, fmt.Errorf("mount spec missing target")
	}
	m.ReadOnly = readOnly
	switch m.Type {
	case backend.MountTypeBind:
		if source == "" {
			return backend.Mount{}, fmt.Errorf("bind mount missing source")
		}
		source = util.ExpandTilde(source, home)
		if !filepath.IsAbs(source) {
			source = filepath.Join(projectDir, source)
		}
		m.Source = source
	case backend.MountTypeVolume:
		if source == "" {
			return backend.Mount{}, fmt.Errorf("volume mount missing source")
		}
		m.Source = source
	case backend.MountTypeTmpfs:
		// tmpfs mounts carry only a target; no source or extra options.
	default:
		return backend.Mount{}, fmt.Errorf("unsupported mount type %q", m.Type)
	}
	m.ContainerPath = target
	return m, nil
}

// IsBlockedMount reports whether a parsed devcontainer mount is a bind mount
// whose source must be rejected for security reasons. Non-bind mounts (named
// volumes, tmpfs) pass through unchanged. projectDir is the host-side project
// directory; bind sources outside it are blocked.
func IsBlockedMount(m backend.Mount, projectDir string) bool {
	if m.Type != backend.MountTypeBind {
		return false
	}
	return isBlockedBindSource(m.Source, projectDir)
}

// isBlockedBindSource reports whether a host bind-mount source supplied via
// devcontainer config (top-level mounts or runArgs) must not be mounted into the
// container. Devcontainer config is adversarial input, so the resolved source
// must be inside projectDir. Symlink resolution is required because Docker
// follows bind-source symlinks at mount time.
func isBlockedBindSource(source string, projectDir string) bool {
	if source == "" {
		return false
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return true
	}
	resolved := filepath.Clean(abs)
	if evaluated, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = filepath.Clean(evaluated)
	}
	if resolved == "/" {
		return true
	}
	if util.IsCriticalSystemDir(resolved) || util.IsSystemDir(resolved) {
		return true
	}
	return !yaconfig.IsPathWithinProjectDir(resolved, projectDir)
}
