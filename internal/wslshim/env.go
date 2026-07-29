// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"fmt"
	"sort"
	"strings"
)

// wslenvVar names the variable WSL consults to decide which Windows environment
// variables reach the distribution. Nothing crosses without being listed there.
const wslenvVar = "WSLENV"

// wslenvFlags are the per-entry suffixes WSL understands: /p translates a single
// path, /l a path list, /u restricts sharing to Windows-to-WSL, /w to the
// reverse direction.
const wslenvFlags = "pluw"

// autoForwardPrefix is forwarded without configuration, because a variable named
// this way is meant for enclave and only enclave reads it.
const autoForwardPrefix = "ENCLAVE_"

// neverAutoForward lists ENCLAVE_ variables the launcher keeps to itself.
// The ENCLAVE_WSL_ controls are Windows-side settings that mean nothing inside
// the distribution, and ENCLAVE_HOME holds a Windows path that would point the
// Linux binary at an asset tree it cannot use. Naming any of them explicitly in
// ENCLAVE_WSL_FORWARD_ENV still forwards it.
var neverAutoForward = map[string]bool{
	envDistro:           true,
	envAllowWindowsPath: true,
	envForwardEnv:       true,
	"ENCLAVE_HOME":      true,
}

// forwardEnv returns the environment for wsl.exe together with any warnings
// worth showing the user.
//
// Forwarding the whole Windows environment into the distribution would be wrong,
// so the launcher forwards ENCLAVE_ variables automatically and anything else
// only when it is named in ENCLAVE_WSL_FORWARD_ENV. Credentials are the sharp
// edge here: an ANTHROPIC_API_KEY set in PowerShell does not cross on its own.
func forwardEnv(environ []string) ([]string, []string, error) {
	lookup := lookupIn(environ)

	entries, warnings, err := forwardedEntries(environ, lookup)
	if err != nil {
		return nil, nil, err
	}

	existing, _ := lookup(wslenvVar)
	value := mergeWSLENV(existing, entries)
	if value == "" {
		return environ, warnings, nil
	}
	return replaceEnv(environ, wslenvVar, value), warnings, nil
}

// forwardedEntries collects the WSLENV entries to add, automatic ones first and
// then the explicitly configured ones.
func forwardedEntries(environ []string, lookup lookupFunc) ([]string, []string, error) {
	var automatic []string
	for _, entry := range environ {
		name, _, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(name, autoForwardPrefix) || neverAutoForward[name] {
			continue
		}
		automatic = append(automatic, name)
	}
	// os.Environ order is not specified, so sort for a stable WSLENV.
	sort.Strings(automatic)

	configured, warnings, err := parseForwardList(lookup)
	if err != nil {
		return nil, nil, err
	}
	return append(automatic, configured...), warnings, nil
}

// parseForwardList reads ENCLAVE_WSL_FORWARD_ENV, a comma-separated allow-list
// of variable names, each optionally carrying a WSLENV flag suffix such as
// MY_DIR/p to have the path translated.
func parseForwardList(lookup lookupFunc) ([]string, []string, error) {
	raw, ok := lookup(envForwardEnv)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil, nil
	}

	var entries, warnings []string
	for _, field := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(field)
		if entry == "" {
			continue
		}

		name, flags, _ := strings.Cut(entry, "/")
		if err := validateEnvName(name); err != nil {
			return nil, nil, fmt.Errorf("%s contains an invalid entry %q: %w", envForwardEnv, entry, err)
		}
		if err := validateWSLENVFlags(flags); err != nil {
			return nil, nil, fmt.Errorf("%s contains an invalid entry %q: %w", envForwardEnv, entry, err)
		}

		if _, set := lookup(name); !set {
			warnings = append(warnings, fmt.Sprintf("%s lists %s, which is not set; nothing will be forwarded for it", envForwardEnv, name))
			continue
		}
		entries = append(entries, entry)
	}
	return entries, warnings, nil
}

func validateEnvName(name string) error {
	if name == "" {
		return fmt.Errorf("the variable name is empty")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return fmt.Errorf("%q is not a valid environment variable name", name)
		}
	}
	return nil
}

func validateWSLENVFlags(flags string) error {
	for _, flag := range flags {
		if !strings.ContainsRune(wslenvFlags, flag) {
			return fmt.Errorf("%q is not a WSLENV flag (expected any of %s)", string(flag), wslenvFlags)
		}
	}
	return nil
}

// mergeWSLENV appends entries to a WSLENV the user may already have set,
// preserving their order and letting their flags win for a name listed twice.
func mergeWSLENV(existing string, entries []string) string {
	var merged []string
	seen := map[string]bool{}
	for _, entry := range strings.Split(existing, ":") {
		if entry == "" {
			continue
		}
		name, _, _ := strings.Cut(entry, "/")
		merged = append(merged, entry)
		seen[name] = true
	}
	for _, entry := range entries {
		name, _, _ := strings.Cut(entry, "/")
		if seen[name] {
			continue
		}
		merged = append(merged, entry)
		seen[name] = true
	}
	return strings.Join(merged, ":")
}

// replaceEnv sets name in place if present, matching case-insensitively as
// Windows does, and appends it otherwise.
func replaceEnv(environ []string, name, value string) []string {
	out := make([]string, 0, len(environ)+1)
	replaced := false
	for _, entry := range environ {
		key, _, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(key, name) && !replaced {
			out = append(out, key+"="+value)
			replaced = true
			continue
		}
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, name+"="+value)
	}
	return out
}
