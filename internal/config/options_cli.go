// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

type CLIValueKind int

const (
	CLIValueNone CLIValueKind = iota
	CLIValueRequired
)

type CLIFlag struct {
	Name                string
	Usage               string
	ValueKind           CLIValueKind
	MissingValueMessage string
	Apply               func(*model.Options, *model.OptionSources, string) error
}

func CLIFlagIndex() map[string]CLIFlag {
	flags := map[string]CLIFlag{}
	for _, spec := range OptionSpecs() {
		for _, flag := range spec.CLIFlags {
			flags[flag.Name] = flag
		}
	}
	return flags
}

// OptionSpecsForGroups returns the subset of OptionSpecs whose Group is in the
// provided list. Used by the CLI to register only the flags relevant to a
// given command.
func OptionSpecsForGroups(groups ...OptionGroup) []OptionSpec {
	if len(groups) == 0 {
		return nil
	}
	want := make(map[OptionGroup]struct{}, len(groups))
	for _, g := range groups {
		want[g] = struct{}{}
	}
	var out []OptionSpec
	for _, spec := range OptionSpecs() {
		if _, ok := want[spec.Group]; ok {
			out = append(out, spec)
		}
	}
	return out
}

// OptionSpecsByName returns the subset of OptionSpecs whose Name matches one
// of the given names. Used by commands that need a specific flag without
// pulling in the full group (e.g. `ps --name` for session filtering).
func OptionSpecsByName(names ...string) []OptionSpec {
	if len(names) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	var out []OptionSpec
	for _, spec := range OptionSpecs() {
		if _, ok := want[spec.Name]; ok {
			out = append(out, spec)
		}
	}
	return out
}

func attachCLIFlags(specs []OptionSpec) {
	flagMap := optionCLIFlags()
	for i := range specs {
		specs[i].CLIFlags = flagMap[specs[i].Name]
	}
}

func boolFlag(name string, usage string, apply func(*model.Options, *model.OptionSources)) CLIFlag {
	return CLIFlag{
		Name:      name,
		Usage:     usage,
		ValueKind: CLIValueNone,
		Apply: func(opts *model.Options, sources *model.OptionSources, _ string) error {
			apply(opts, sources)
			return nil
		},
	}
}

func valueFlag(name string, usage string, missing string, apply func(*model.Options, *model.OptionSources, string) error) CLIFlag {
	return CLIFlag{
		Name:                name,
		Usage:               usage,
		ValueKind:           CLIValueRequired,
		MissingValueMessage: missing,
		Apply:               apply,
	}
}

func applySecretsScope(opts *model.Options, value string) error {
	switch value {
	case model.SecretsScopeProject, model.SecretsScopeGlobal, model.SecretsScopeBoth:
		opts.SecretsScope = value
		return nil
	default:
		return fmt.Errorf("invalid --secrets-scope: %s (use: %s|%s|%s)", value, model.SecretsScopeProject, model.SecretsScopeGlobal, model.SecretsScopeBoth)
	}
}

func applyAuthScope(opts *model.Options, value string) error {
	switch value {
	case model.AuthScopeShared, model.AuthScopeProject:
		opts.AuthScope = value
		return nil
	default:
		return fmt.Errorf("invalid --auth-scope: %s (use: %s|%s)", value, model.AuthScopeShared, model.AuthScopeProject)
	}
}

// authNameMaxLength bounds the identity slug, which becomes a path segment in
// the managed store layout (hoststore.DirFor); 32 matches the session-name
// length convention.
const authNameMaxLength = 32

func applyAuthName(opts *model.Options, value string) error {
	name, err := ValidateAuthName(value)
	if err != nil {
		return err
	}
	opts.AuthName = name
	return nil
}

// ValidateAuthName normalizes and validates an --auth-name slug used to select a
// named shared auth store. It lowercases and trims the input, then requires
// [a-z0-9] with interior hyphens allowed. Hash-like tokens (12 hex characters)
// are reserved for project identities. The empty string is invalid; callers
// treat an unset AuthName as "no named identity" without calling this.
func ValidateAuthName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", errors.New("--auth-name requires a non-empty value")
	}
	if len(name) > authNameMaxLength {
		return "", fmt.Errorf("invalid --auth-name %q: must be at most %d characters", raw, authNameMaxLength)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		isAlnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if isAlnum {
			continue
		}
		if c == '-' && i > 0 && i < len(name)-1 {
			continue
		}
		return "", fmt.Errorf("invalid --auth-name %q: use lowercase letters, digits, and interior hyphens", raw)
	}
	for _, token := range strings.Split(name, "-") {
		if model.IsHashSegment(token) {
			return "", fmt.Errorf("invalid --auth-name %q: %q looks like a project hash and would collide with managed store naming", raw, token)
		}
	}
	return name, nil
}

func applyHostConfig(opts *model.Options, value string) error {
	switch value {
	case model.HostConfigNone, model.HostConfigPassthrough:
		opts.HostConfig = value
		return nil
	default:
		return fmt.Errorf("invalid --host-config: %s (use: %s|%s)", value, model.HostConfigNone, model.HostConfigPassthrough)
	}
}

func applyNetworkLog(opts *model.Options, value string) error {
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case model.NetworkLogCoarse, model.NetworkLogRequests:
		opts.NetworkLog = normalized
		return nil
	default:
		return fmt.Errorf("invalid --network-log: %s (use: %s|%s)", value, model.NetworkLogCoarse, model.NetworkLogRequests)
	}
}

func applyProjectMount(opts *model.Options, value string) error {
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case model.ProjectMountWritable, model.ProjectMountReadonly:
		opts.ProjectMount = normalized
		return nil
	default:
		return fmt.Errorf("invalid --project-mount: %s (use: %s|%s)", value, model.ProjectMountWritable, model.ProjectMountReadonly)
	}
}

func applyWorktreeMetadata(opts *model.Options, value string) error {
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case model.WorktreeMetadataFollow, model.WorktreeMetadataReadonly, model.WorktreeMetadataNone:
		opts.WorktreeMetadata = normalized
		return nil
	default:
		return fmt.Errorf("invalid --worktree-metadata: %s (use: %s|%s|%s)", value, model.WorktreeMetadataFollow, model.WorktreeMetadataReadonly, model.WorktreeMetadataNone)
	}
}

type csvValueFunc func(string) (string, bool, error)

func appendCSVUnique(dst []string, value string, normalize csvValueFunc) ([]string, int, error) {
	parts := strings.Split(value, ",")
	added := 0
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		normalized, keep, err := normalize(item)
		if err != nil {
			return dst, added, err
		}
		if !keep {
			continue
		}
		if !slices.Contains(dst, normalized) {
			dst = append(dst, normalized)
		}
		added++
	}
	return dst, added, nil
}

func keepCSVValue(value string) (string, bool, error) {
	return value, true, nil
}

func applyPassEnv(opts *model.Options, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("--pass-env requires a value")
	}
	var added int
	var err error
	opts.PassEnv, added, err = appendCSVUnique(opts.PassEnv, value, func(key string) (string, bool, error) {
		if !isValidEnvKey(key) {
			return "", false, fmt.Errorf("invalid --pass-env key: %s", key)
		}
		return key, true, nil
	})
	if err != nil {
		return err
	}
	if added == 0 {
		return errors.New("--pass-env requires at least one key")
	}
	return nil
}

func applyCacheFrom(opts *model.Options, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("--cache-from requires a value")
	}
	var added int
	var err error
	opts.CacheFrom, added, err = appendCSVUnique(opts.CacheFrom, value, keepCSVValue)
	if err != nil {
		return err
	}
	if added == 0 {
		return errors.New("--cache-from requires at least one image")
	}
	return nil
}

func applyFeatures(opts *model.Options, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("--features requires a value")
	}
	sawNone := false
	var added int
	var err error
	opts.Features, added, err = appendCSVUnique(opts.Features, value, func(feature string) (string, bool, error) {
		if strings.EqualFold(feature, "none") {
			sawNone = true
			return "", false, nil
		}
		return feature, true, nil
	})
	if err != nil {
		return err
	}
	if sawNone {
		if added > 0 {
			return errors.New("--features value 'none' cannot be combined with other features")
		}
		opts.Features = []string{}
		return nil
	}
	if added == 0 {
		return errors.New("--features requires at least one feature")
	}
	return nil
}

func applyProgress(opts *model.Options, value string) error {
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case model.BuildProgressQuiet, model.BuildProgressCompact, model.BuildProgressVerbose:
		opts.Progress = normalized
		return nil
	default:
		return fmt.Errorf("invalid --progress value: %s (use: %s|%s|%s)", value, model.BuildProgressQuiet, model.BuildProgressCompact, model.BuildProgressVerbose)
	}
}

func applyBridgePort(opts *model.Options, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("--bridge-port requires a port number")
	}
	var added int
	var err error
	opts.BridgePorts, added, err = appendCSVUnique(opts.BridgePorts, value, func(port string) (string, bool, error) {
		if !util.IsPortNumber(port) {
			return "", false, fmt.Errorf("invalid --bridge-port value: %s (must be 1-65535)", port)
		}
		return port, true, nil
	})
	if err != nil {
		return err
	}
	if added == 0 {
		return errors.New("--bridge-port requires at least one port")
	}
	return nil
}

func isValidEnvKey(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
