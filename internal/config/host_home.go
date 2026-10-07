// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"os"
	"os/user"
	"strings"

	"enclave/internal/util"
)

// ResolveHostHome returns the best-effort home directory for host paths.
// It prefers the HOME-derived path if writable, otherwise falls back to the user database.
func ResolveHostHome() (string, error) {
	return resolveHostHome(false)
}

// ResolveHostHomeReadOnly is ResolveHostHome for code that must not write, such
// as everything that runs before the root guard. It judges writability from
// permissions instead of creating a probe file, so it picks the same home as
// ResolveHostHome unless creating a file fails for reasons access(2) does not
// see, such as a full disk.
func ResolveHostHomeReadOnly() (string, error) {
	return resolveHostHome(true)
}

func resolveHostHome(readOnly bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	current, err := user.Current()
	if err != nil {
		return "", err
	}
	primary := strings.TrimSpace(home)
	fallback := strings.TrimSpace(current.HomeDir)

	usable := IsWritableDir
	if readOnly {
		usable = looksWritableDir
	}
	if primary != "" && usable(primary) {
		return primary, nil
	}
	if fallback != "" && fallback != primary && usable(fallback) {
		return fallback, nil
	}
	return "", homeNotWritableError(primary, fallback)
}

// looksWritableDir reports whether path is a directory the process may create
// files in, judged from permissions alone so that nothing is written.
func looksWritableDir(path string) bool {
	return util.IsDir(path) && accessAllowsCreate(path)
}

// IsWritableDir reports whether the directory exists and is writable.
func IsWritableDir(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	tmp, err := os.CreateTemp(path, ".enclave-writecheck-*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name) // #nosec G703 -- path from os.CreateTemp is trusted.
		return false
	}
	if err := os.Remove(name); err != nil { // #nosec G703 -- path from os.CreateTemp is trusted.
		return false
	}
	return true
}

func homeNotWritableError(primary string, fallback string) error {
	if primary == "" && fallback == "" {
		return fmt.Errorf("home directory is not set")
	}
	if primary == "" {
		return fmt.Errorf("home directory is not writable: HOME is not set, user home=%s (set HOME to a writable path)", fallback)
	}
	if fallback == "" || fallback == primary {
		return fmt.Errorf("home directory is not writable: %s (set HOME to a writable path)", primary)
	}
	return fmt.Errorf("home directory is not writable: HOME=%s USER=%s (set HOME to a writable path)", primary, fallback)
}
