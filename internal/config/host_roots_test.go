// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"path/filepath"
	"testing"

	"enclave/internal/model"
)

func TestXDGRootsFallBackUnderHome(t *testing.T) {
	unsetXDGEnv(t)

	home := "/tmp/xdg-home"
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"config", xdgConfigRoot(home), filepath.Join(home, ".config", "enclave")},
		{"state", xdgStateRoot(home), filepath.Join(home, ".local", "state", "enclave")},
		{"cache", xdgCacheRoot(home), filepath.Join(home, ".cache", "enclave")},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s root = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestXDGRootsHonorAbsoluteEnvOverride(t *testing.T) {
	home := "/tmp/xdg-home"
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	t.Setenv("XDG_CACHE_HOME", "/custom/cache")

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"config", xdgConfigRoot(home), filepath.Join("/custom/config", "enclave")},
		{"state", xdgStateRoot(home), filepath.Join("/custom/state", "enclave")},
		{"cache", xdgCacheRoot(home), filepath.Join("/custom/cache", "enclave")},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s root = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestXDGRootsIgnoreRelativeEnvOverride(t *testing.T) {
	unsetXDGEnv(t)

	home := "/tmp/xdg-home"
	// Relative env values must be ignored in favor of the spec fallback.
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	t.Setenv("XDG_CACHE_HOME", "also/relative")

	if got, want := xdgConfigRoot(home), filepath.Join(home, ".config", "enclave"); got != want {
		t.Errorf("config root = %q, want %q", got, want)
	}
	if got, want := xdgCacheRoot(home), filepath.Join(home, ".cache", "enclave"); got != want {
		t.Errorf("cache root = %q, want %q", got, want)
	}
}

func TestMacRootsUseAppleLayout(t *testing.T) {
	// The macOS roots use the reverse-DNS application directory and must not be
	// affected by the XDG_* environment overrides.
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	t.Setenv("XDG_CACHE_HOME", "/custom/cache")

	home := "/Users/tester"
	appSupport := filepath.Join(home, "Library", "Application Support", model.AppID)
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"config", macConfigRoot(home), filepath.Join(appSupport, "config")},
		{"state", macStateRoot(home), filepath.Join(appSupport, "state")},
		{"cache", macCacheRoot(home), filepath.Join(home, "Library", "Caches", model.AppID)},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s root = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestMacRootsStayDistinct guards the invariant the cleanup guard relies on:
// the macOS config and state roots must be siblings, neither nested under the
// other.
func TestMacRootsStayDistinct(t *testing.T) {
	home := "/Users/tester"
	config := macConfigRoot(home)
	state := macStateRoot(home)
	if config == state {
		t.Fatalf("config and state roots must differ, both = %q", config)
	}
	if isUnder(state, config) {
		t.Fatalf("state root %q must not be under config root %q", state, config)
	}
	if isUnder(config, state) {
		t.Fatalf("config root %q must not be under state root %q", config, state)
	}
}

// isUnder reports whether path equals root or is nested beneath it.
func isUnder(path string, root string) bool {
	if path == root {
		return true
	}
	return len(path) > len(root) && path[:len(root)] == root && path[len(root)] == filepath.Separator
}

// unsetXDGEnv clears the XDG base directory environment variables so that host
// environment does not leak into tests that assert the spec fallbacks.
func unsetXDGEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, "")
	}
}
