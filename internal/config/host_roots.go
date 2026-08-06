// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"runtime"

	"enclave/internal/model"
)

// Host-side enclave files are split across three roots: config (user-edited),
// state (machine-generated), and cache (regenerable). Linux and other Unixes
// follow the XDG Base Directory Specification. macOS uses distinct config and
// state directories under ~/Library/Application Support and the Apple cache
// directory under ~/Library/Caches. Cleanup targets live under the state root,
// and a guard forbids them from resolving under the config root.

func hostConfigRoot(home string) string {
	if runtime.GOOS == "darwin" {
		return macConfigRoot(home)
	}
	return xdgConfigRoot(home)
}

func hostStateRoot(home string) string {
	if runtime.GOOS == "darwin" {
		return macStateRoot(home)
	}
	return xdgStateRoot(home)
}

func hostCacheRoot(home string) string {
	if runtime.GOOS == "darwin" {
		return macCacheRoot(home)
	}
	return xdgCacheRoot(home)
}

// XDG base directory resolvers. Each returns the enclave-specific subtree
// under the corresponding XDG base directory, honoring the XDG_* environment
// overrides with the spec-mandated fallbacks under home. Per the XDG Base
// Directory Specification, environment values that are not absolute paths are
// ignored in favor of the fallback.

func xdgConfigRoot(home string) string {
	return filepath.Join(xdgBaseDir("XDG_CONFIG_HOME", home, ".config"), model.AppName)
}

func xdgStateRoot(home string) string {
	return filepath.Join(xdgBaseDir("XDG_STATE_HOME", home, filepath.Join(".local", "state")), model.AppName)
}

func xdgCacheRoot(home string) string {
	return filepath.Join(xdgBaseDir("XDG_CACHE_HOME", home, ".cache"), model.AppName)
}

// xdgBaseDir resolves an XDG base directory. It uses the environment override
// only when it is set to an absolute path; otherwise it falls back to
// home/relFallback as mandated by the spec (empty or relative values are
// ignored).
func xdgBaseDir(envVar string, home string, relFallback string) string {
	if dir := os.Getenv(envVar); filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(home, relFallback)
}

// macOS base directory resolvers. Config and state are distinct subtrees of the
// reverse-DNS application directory under ~/Library/Application Support.
// Regenerable caches follow the Apple convention of ~/Library/Caches. macOS
// ignores XDG_* overrides, matching platform conventions and the Go standard
// library.

func macAppSupportDir(home string) string {
	return filepath.Join(home, "Library", "Application Support", model.AppID)
}

func macConfigRoot(home string) string {
	return filepath.Join(macAppSupportDir(home), "config")
}

func macStateRoot(home string) string {
	return filepath.Join(macAppSupportDir(home), "state")
}

func macCacheRoot(home string) string {
	return filepath.Join(home, "Library", "Caches", model.AppID)
}
