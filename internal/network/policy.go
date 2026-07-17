// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"enclave/internal/config"
)

// Policy represents a network.jsonc policy file.
type Policy struct {
	Mode                 string         `json:"mode,omitempty"`
	InheritToolAllowlist *bool          `json:"inherit_tool_allowlist,omitempty"`
	InheritGlobalPolicy  *bool          `json:"inherit_global_policy,omitempty"`
	Domains              PolicyDomains  `json:"domains,omitempty"`
	Advanced             PolicyAdvanced `json:"advanced,omitempty"`
}

// PolicyDomains holds domain allowlist entries.
type PolicyDomains struct {
	Global []string            `json:"global,omitempty"`
	Tools  map[string][]string `json:"tools,omitempty"`
}

// PolicyAdvanced holds advanced configuration.
type PolicyAdvanced struct {
	AllowlistFile string   `json:"allowlist_file,omitempty"`
	Resolvers     []string `json:"resolvers,omitempty"`
}

// GlobalPolicyPath returns the path to the global network policy file.
func GlobalPolicyPath(home string) string {
	return config.HostNetworkPolicyPath(home)
}

// ProjectPolicyPath returns the path to the project-level network policy file,
// now stored under the config root keyed by project hash. Returns an empty
// string when projectHash is empty.
func ProjectPolicyPath(home string, projectHash string) string {
	if projectHash == "" {
		return ""
	}
	return config.HostProjectNetworkPolicyPath(home, projectHash)
}

// LoadPolicy reads a network policy from the given path.
// Returns a zero-value Policy if the file does not exist.
func LoadPolicy(path string) (Policy, error) {
	// #nosec G304 -- path points to explicit enclave policy files.
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Policy{}, nil
		}
		return Policy{}, err
	}
	cleaned := StripJSONCComments(data)
	var p Policy
	if err := json.Unmarshal(cleaned, &p); err != nil {
		return Policy{}, err
	}
	return NormalizePolicy(p)
}

// SavePolicy writes a network policy to the given path as formatted JSON.
func SavePolicy(path string, p Policy) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// #nosec G306 -- policy files are intentionally world-readable for tooling interoperability.
	return os.WriteFile(path, data, 0o644)
}
