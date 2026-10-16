// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"enclave/internal/config"
	"enclave/internal/hosthooks"
	"enclave/internal/model"
	"os"
	"path/filepath"
	"testing"
)

func TestHostDiscoveryIgnoresContainerSelectionAndOtherTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv(hosthooks.ChildEnv, "")
	configs := hosthooks.ConfigMap{}
	enabled := true
	for id, dir := range map[string]string{
		"user:local":    filepath.Join(config.HostHooksDir(home), "local"),
		"feature:extra": filepath.Join(config.HostExtensionsDir(home), "features", "extra", "hooks", "host"),
		"tool:claude":   filepath.Join(config.HostExtensionsDir(home), "tools", "claude", "hooks", "host"),
		"tool:codex":    filepath.Join(config.HostExtensionsDir(home), "tools", "codex", "hooks", "host"),
	} {
		writeAppFile(t, filepath.Join(dir, "manifest.json"), `{"schemaVersion":"1","phases":{"prepare":"prepare"}}`, 0600)
		writeAppFile(t, filepath.Join(dir, "prepare"), "#!/bin/sh\ntouch SENTINEL\n", 0700)
		configs[id] = hosthooks.Config{Enabled: &enabled}
	}
	opts := model.Options{RunOptions: model.RunOptions{Tool: "claude", HostHooks: configs}, BuildOptions: model.BuildOptions{Slim: true, Features: []string{}}}
	seen := map[string]bool{}
	providers := configuredHostProviders(opts, seen)
	if len(providers) != 3 || len(seen) != 0 {
		t.Fatalf("providers=%v warnings=%v", providers, seen)
	}
	for _, p := range providers {
		if p.ID == "tool:codex" {
			t.Fatal("unselected tool provider active")
		}
		if _, err := os.Stat(filepath.Join(p.Dir, "SENTINEL")); !os.IsNotExist(err) {
			t.Fatal("discovery executed hook")
		}
	}
	t.Setenv(hosthooks.ChildEnv, "1")
	if len(configuredHostProviders(opts, seen)) != 0 {
		t.Fatal("recursive hook chain activated")
	}
}
func TestTargetHostSettingsUseActualToolAndProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	project := t.TempDir()
	path, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, path, `{"host_hooks":{"user:notify":{"enabled":true,"settings":{"scope":"global"}}},"tool_overrides":{"codex":{"host_hooks":{"user:notify":{"settings":{"scope":"codex"}}}}}}`, 0600)
	target := model.TerminalTarget{Tool: "codex", ProjectDir: project}
	fallback := model.Options{RunOptions: model.RunOptions{Tool: "claude"}}
	got := targetHostOptions(target, fallback)
	if got.Tool != "codex" || got.HostHooks["user:notify"].Settings["scope"] != "codex" {
		t.Fatal(got)
	}
	writeAppFile(t, config.ProjectConfigJSONPath(project), `{"tool_overrides":{"codex":{"host_hooks":{"user:notify":{"settings":{}}}}}}`, 0600)
	got = targetHostOptions(target, fallback)
	if len(got.HostHooks["user:notify"].Settings) != 0 {
		t.Fatal(got.HostHooks)
	}
	target.ProjectDir = ""
	if got := targetHostOptions(target, fallback); got.Tool != "claude" {
		t.Fatal("missing metadata did not fall back")
	}
}
