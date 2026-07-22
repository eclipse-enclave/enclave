// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

// LoadProfile loads the sandbox profile for name from its spec.yaml/spec.json
// document. It returns os.ErrNotExist (via LoadSpec) if no spec is present.
func LoadProfile(paths model.Paths, name string) (model.Profile, error) {
	return loadSpecProfile(paths, name)
}

func ListProfiles(paths model.Paths) ([]string, error) {
	// ListTools already returns only tool extensions that have a
	// spec.yaml/spec.json, and every sandbox spec synthesizes a profile via
	// specToProfile, so no additional filtering is required.
	tools, err := ListTools(paths)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, tool := range tools {
		if hasSpecFile(paths, tool, KindSandbox) {
			names = append(names, tool)
		}
	}

	sort.Strings(names)
	return names, nil
}

func validateAndNormalizeProfile(profile *model.Profile) error {
	if profile == nil {
		return fmt.Errorf("profile is nil")
	}
	normalizedSecrets, err := validateAndNormalizeSecretConfigs(profile.Secrets)
	if err != nil {
		return err
	}
	profile.Secrets = normalizedSecrets

	secretIDs := map[string]struct{}{}
	for id := range profile.Secrets {
		secretIDs[id] = struct{}{}
	}
	if err := validateAndNormalizeProviderCredentialSecrets(profile.Providers, secretIDs); err != nil {
		return err
	}
	if err := validateProfileQEMUMemory(profile); err != nil {
		return err
	}
	if err := validateProfileSkillsPath(profile); err != nil {
		return err
	}
	if err := validateAndNormalizePorts(profile); err != nil {
		return err
	}
	if err := validateAndNormalizeMemoryDir(profile); err != nil {
		return err
	}
	return validateAndNormalizeProviderSecurestorage(profile.Providers)
}

// validateAndNormalizeMemoryDir checks the container-home-relative memory
// mount target. The path must be relative, free of traversal, and resolve to a
// concrete location (not "."). The cleaned value is written back to the profile.
func validateAndNormalizeMemoryDir(profile *model.Profile) error {
	if profile.MemoryDir != "" {
		cleaned, err := cleanMemoryPath(profile.MemoryDir)
		if err != nil {
			return fmt.Errorf("memory_dir: %w", err)
		}
		profile.MemoryDir = cleaned
	}
	return nil
}

func validateProfileQEMUMemory(profile *model.Profile) error {
	if profile.QEMUMinMemoryMiB < 0 {
		return fmt.Errorf("qemu_min_memory_mib must be non-negative")
	}
	return nil
}

func validateProfileSkillsPath(profile *model.Profile) error {
	skillsDir := strings.TrimSpace(profile.SkillsDir)
	if skillsDir == "" {
		return nil
	}
	configDir := strings.TrimSpace(profile.ConfigDir)
	if configDir == "" {
		return fmt.Errorf("skills_dir requires config_dir")
	}
	configPath := containerProfilePath(configDir)
	skillsPath := containerProfilePath(skillsDir)
	if skillsPath == configPath || !strings.HasPrefix(skillsPath, configPath+"/") {
		return fmt.Errorf("skills_dir %q must be below config_dir %q", profile.SkillsDir, profile.ConfigDir)
	}
	return nil
}

func containerProfilePath(value string) string {
	if path.IsAbs(value) {
		return path.Clean(value)
	}
	return path.Join(model.ContainerHome, value)
}

func cleanMemoryPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be relative: %s", path)
	}
	if util.HasPathTraversal(path) {
		return "", fmt.Errorf("path contains traversal: %s", path)
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." {
		return "", fmt.Errorf("path resolves to current directory: %s", path)
	}
	return cleaned, nil
}

// validateAndNormalizePorts checks declared port entries and fills in defaults
// for the label and open-URL template so downstream code can rely on them.
func validateAndNormalizePorts(profile *model.Profile) error {
	return normalizePortConfigs(profile.Ports, profile.Name)
}

// normalizePortConfigs checks declared port entries (tool or feature) and
// fills in defaults for the label and open-URL template so downstream code
// can rely on them.
func normalizePortConfigs(ports []model.PortConfig, defaultLabel string) error {
	for i := range ports {
		p := &ports[i]
		if p.Container < 1 || p.Container > 65535 {
			return fmt.Errorf("ports[%d]: container port %d out of range (1-65535)", i, p.Container)
		}
		switch p.HostAllocation {
		case "", model.HostAllocationFixed, model.HostAllocationAuto:
		default:
			return fmt.Errorf("ports[%d]: invalid hostAllocation %q (want %q or %q)",
				i, p.HostAllocation, model.HostAllocationFixed, model.HostAllocationAuto)
		}
		if p.Label == "" {
			p.Label = defaultLabel
		}
		if p.OpenURL == "" {
			p.OpenURL = "http://localhost:" + model.PortHostPlaceholder
		}
	}
	return nil
}
