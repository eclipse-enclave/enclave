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

// neverAutoForwardPrefix covers the launcher's own controls. They are
// Windows-side settings that mean nothing inside the distribution, and matching
// the prefix rather than each name keeps a control added later from crossing
// because someone forgot to list it.
const neverAutoForwardPrefix = "ENCLAVE_WSL_"

// neverAutoForwardNames lists the remaining ENCLAVE_ variables the launcher
// keeps to itself. ENCLAVE_HOME holds a Windows path that would point the Linux
// binary at an asset tree it cannot use. Naming any excluded variable explicitly
// in ENCLAVE_WSL_FORWARD_ENV still forwards it.
var neverAutoForwardNames = map[string]bool{
	"ENCLAVE_HOME": true,
}

func autoForwardable(name string) bool {
	// Windows environment variable names are case-insensitive, so every question
	// about one is asked in upper case.
	upper := strings.ToUpper(name)
	if !strings.HasPrefix(upper, autoForwardPrefix) {
		return false
	}
	if strings.HasPrefix(upper, neverAutoForwardPrefix) || neverAutoForwardNames[upper] {
		return false
	}
	// WSLENV is colon-separated and its entries are name/flags, so a name
	// containing either delimiter would splice a second entry into the list and
	// forward a variable nobody named. Windows allows such names even though
	// nothing sane sets one.
	return validateEnvName(name) == nil
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

// forwardedEntries collects the WSLENV entries to add. The explicitly
// configured ones come first, because mergeWSLENV keeps the first entry for a
// name: an ENCLAVE_ variable would otherwise be forwarded by the automatic rule
// under its bare name, and the flag suffix the user asked for — the /p that
// translates the path — would be dropped as a duplicate.
func forwardedEntries(environ []string, lookup lookupFunc) ([]string, []string, error) {
	var automatic []string
	for _, entry := range environ {
		// The name is forwarded as the user spelled it, only the decision is
		// case-insensitive.
		if name, _, found := strings.Cut(entry, "="); found && autoForwardable(name) {
			automatic = append(automatic, name)
		}
	}
	// os.Environ order is not specified, so sort for a stable WSLENV.
	sort.Strings(automatic)

	configured, warnings, err := parseForwardList(lookup)
	if err != nil {
		return nil, nil, err
	}
	return append(configured, automatic...), warnings, nil
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
// Names are compared in upper case, as Windows compares them.
func mergeWSLENV(existing string, entries []string) string {
	var merged []string
	seen := map[string]bool{}
	add := func(entry string) {
		name, _, _ := strings.Cut(entry, "/")
		key := strings.ToUpper(name)
		if seen[key] {
			return
		}
		merged = append(merged, entry)
		seen[key] = true
	}

	for _, entry := range strings.Split(existing, ":") {
		if entry != "" {
			add(entry)
		}
	}
	for _, entry := range entries {
		add(entry)
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
