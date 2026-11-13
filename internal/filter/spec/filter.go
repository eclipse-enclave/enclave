// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package spec loads filter extensions (spec.yaml and presets), validates
// them, resolves presets and scopes, and compiles them into a filter.Policy.
package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"enclave/internal/filter"
	"enclave/internal/model"
	"enclave/internal/util"
)

// Spec identity and layout.
const (
	Kind             = "filter"
	SchemaVersion    = "1"
	SpecFilename     = "spec.yaml"
	SpecFilenameJSON = "spec.json"
	PresetsDir       = "presets"
	PresetExt        = ".yaml"
	ReadmeFilename   = "README.md"
)

const maxFileBytes = 1 << 20

// recognizedEntries are the top-level entries of a filter extension directory
// that are not reported as ignored.
var recognizedEntries = map[string]bool{
	SpecFilename:                  true,
	SpecFilenameJSON:              true,
	ReadmeFilename:                true,
	PresetsDir:                    true,
	model.CommandsDirName:         true,
	model.ExtensionSourceFilename: true,
	"LICENSE":                     true,
	"SECURITY.md":                 true,
}

// Filter is a validated filter extension.
type Filter struct {
	Name          string
	DisplayName   string
	Description   string
	DefaultPreset string
	// Dir is the extension directory and SpecPath its spec file.
	Dir      string
	SpecPath string

	Hosts []string
	// Confine lists domains above governed hosts whose other names the
	// session cannot reach while the filter is active.
	Confine []string
	// JSONKeys is "" or filter.JSONKeysProtobuf.
	JSONKeys     string
	Credential   Credential
	Resolvers    map[string]Resolver
	Counters     map[string]filter.Counter
	Capabilities map[string]Capability
	Routes       []filter.Route
	Deny         *filter.DenyTemplate

	// Warnings are non-fatal findings, such as ignored files.
	Warnings []string
}

// Credential is the validated credential section.
type Credential struct {
	// Type is filter.CredentialOAuth2 or filter.CredentialStatic.
	Type        string
	OAuth2      *OAuth2
	Static      *Static
	Inject      filter.Inject
	Env         []string
	Placeholder *model.SecretPlaceholderConfig
	DenyHeaders []string
	DenyParams  []string
}

// OAuth2 is the validated OAuth2 client configuration. ScopeImplies uses full
// scope strings.
type OAuth2 struct {
	AuthURL      string
	TokenURL     string
	RevokeURL    string
	AuthParams   map[string]string
	ScopePrefix  string
	ScopeImplies map[string][]string
	ClientHelp   string
}

// Static is where a static credential is read from on the host. Priority is
// model.SecretPriorityEnvFirst or model.SecretPriorityFileFirst.
type Static struct {
	Env      []string
	File     *model.SecretFileSource
	Priority string
}

// Resolver is a resolver definition with the full scopes its lookups need.
type Resolver struct {
	filter.Resolver
	Scopes []string
}

// Capability is a capability definition. Scopes are full scope strings.
type Capability struct {
	Description string
	Public      bool
	Scopes      []string
	Constraints map[string]Constraint
}

// Constraint is a constraint definition. Default is nil without a default.
type Constraint struct {
	Kind     string
	Var      string
	Resolver string
	Default  *filter.ConstraintValue
}

// Load reads and validates the filter extension in dir, including every
// shipped preset. A non-empty name must match the spec's name.
func Load(dir string, name string) (*Filter, error) {
	specPath, err := findSpecFile(dir)
	if err != nil {
		return nil, err
	}
	data, err := readRegularFile(specPath)
	if err != nil {
		return nil, err
	}
	f, err := Parse(data, specPath)
	if err != nil {
		return nil, err
	}
	if name != "" && f.Name != name {
		return nil, fmt.Errorf("%s: name must be %q (got %q)", specPath, name, f.Name)
	}
	f.Dir = dir
	f.Warnings = append(f.Warnings, ignoredEntryWarnings(dir)...)
	if err := f.validateShippedPresets(); err != nil {
		return nil, err
	}
	return f, nil
}

// Parse validates a spec document. label names the document in errors,
// normally its path. The result has no Dir, so it has no shipped presets.
func Parse(data []byte, label string) (*Filter, error) {
	var doc specDocument
	if err := decodeYAML(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	f, err := buildFilter(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	f.SpecPath = label
	return f, nil
}

func findSpecFile(dir string) (string, error) {
	for _, name := range []string{SpecFilename, SpecFilenameJSON} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s: no %s found: %w", dir, SpecFilename, os.ErrNotExist)
}

// readRegularFile reads a bounded regular file, so a FIFO or device cannot
// hang the loader.
func readRegularFile(path string) ([]byte, error) {
	return util.ReadRegularFile(path, maxFileBytes)
}

func ignoredEntryWarnings(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{fmt.Sprintf("cannot read %s: %v", dir, err)}
	}
	var warnings []string
	for _, entry := range entries {
		if recognizedEntries[entry.Name()] {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s is ignored: a filter is data only and uses %s, %s, %s/, and %s/",
			filepath.Join(dir, entry.Name()), SpecFilename, ReadmeFilename, PresetsDir, model.CommandsDirName))
	}
	sort.Strings(warnings)
	return warnings
}
