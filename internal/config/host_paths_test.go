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
	"testing"

	"enclave/internal/model"
)

func TestHostPaths(t *testing.T) {
	unsetXDGEnv(t)

	home := "/tmp/test-home"
	projectHash := "projecthash"
	tool := "codex"

	configRoot := hostConfigRoot(home)
	stateRoot := hostStateRoot(home)

	if got, want := HostSkillsDir(home), filepath.Join(configRoot, "skills"); got != want {
		t.Fatalf("HostSkillsDir() = %q, want %q", got, want)
	}
	if got, want := HostConfigDir(home), filepath.Join(configRoot, "tools"); got != want {
		t.Fatalf("HostConfigDir() = %q, want %q", got, want)
	}
	if got, want := HostToolConfigDir(home, tool), filepath.Join(configRoot, "tools", tool); got != want {
		t.Fatalf("HostToolConfigDir() = %q, want %q", got, want)
	}
	if got, want := HostToolPatchesDir(home, tool), filepath.Join(configRoot, "patches", tool); got != want {
		t.Fatalf("HostToolPatchesDir() = %q, want %q", got, want)
	}
	if got, want := HostGatewayAllowlistsDir(home), filepath.Join(configRoot, "gateway-allowlists"); got != want {
		t.Fatalf("HostGatewayAllowlistsDir() = %q, want %q", got, want)
	}
	if got, want := HostProjectHomeConfigDir(home, projectHash, tool), filepath.Join(stateRoot, "projects", projectHash, tool, model.HomeConfigDirName); got != want {
		t.Fatalf("HostProjectHomeConfigDir() = %q, want %q", got, want)
	}
	if got, want := HostProjectSkillsDir(home, projectHash), filepath.Join(configRoot, "projects", projectHash, "skills"); got != want {
		t.Fatalf("HostProjectSkillsDir() = %q, want %q", got, want)
	}
	if got, want := HostProjectConfigDir(home, projectHash, tool), filepath.Join(configRoot, "projects", projectHash, tool, "config"); got != want {
		t.Fatalf("HostProjectConfigDir() = %q, want %q", got, want)
	}
	if got, want := HostProjectToolPatchesDir(home, projectHash, tool), filepath.Join(configRoot, "projects", projectHash, "patches", tool); got != want {
		t.Fatalf("HostProjectToolPatchesDir() = %q, want %q", got, want)
	}
	if got, want := HostProjectGatewayAllowlistsDir(home, projectHash), filepath.Join(configRoot, "projects", projectHash, "gateway-allowlists"); got != want {
		t.Fatalf("HostProjectGatewayAllowlistsDir() = %q, want %q", got, want)
	}
	if got, want := HostProjectOverridesDir(home, projectHash), filepath.Join(configRoot, "projects", projectHash); got != want {
		t.Fatalf("HostProjectOverridesDir() = %q, want %q", got, want)
	}
	if got, want := HostProjectConfigJSONPath(home, projectHash), filepath.Join(configRoot, "projects", projectHash, "config.json"); got != want {
		t.Fatalf("HostProjectConfigJSONPath() = %q, want %q", got, want)
	}
	if got, want := HostProjectNetworkPolicyPath(home, projectHash), filepath.Join(configRoot, "projects", projectHash, "network.jsonc"); got != want {
		t.Fatalf("HostProjectNetworkPolicyPath() = %q, want %q", got, want)
	}
	if got, want := HostProjectMemoryDir(home, projectHash, tool), filepath.Join(stateRoot, "projects", projectHash, tool, "memory"); got != want {
		t.Fatalf("HostProjectMemoryDir() = %q, want %q", got, want)
	}
	if got, want := HostProjectGeneratedConfigDir(home, projectHash, tool), filepath.Join(stateRoot, "projects", projectHash, tool, model.GeneratedConfigDirName); got != want {
		t.Fatalf("HostProjectGeneratedConfigDir() = %q, want %q", got, want)
	}
	if got, want := HostStoreConfigDir(home, tool, projectHash, "default"), filepath.Join(stateRoot, "projects", projectHash, tool, "config-store", "default"); got != want {
		t.Fatalf("HostStoreConfigDir() = %q, want %q", got, want)
	}
	if got, want := HostStoreConfigDir(home, tool, projectHash, "wt2"), filepath.Join(stateRoot, "projects", projectHash, tool, "config-store", "wt2"); got != want {
		t.Fatalf("HostStoreConfigDir() = %q, want %q", got, want)
	}
	if got, want := HostStoreEnvDir(home, tool, projectHash), filepath.Join(stateRoot, "projects", projectHash, tool, "env"); got != want {
		t.Fatalf("HostStoreEnvDir() = %q, want %q", got, want)
	}
	if got, want := HostStoreAuthDir(home, tool, ""), filepath.Join(stateRoot, "tools", tool, "auth", "default"); got != want {
		t.Fatalf("HostStoreAuthDir() = %q, want %q", got, want)
	}
	if got, want := HostStoreAuthDir(home, tool, "personal"), filepath.Join(stateRoot, "tools", tool, "auth", "personal"); got != want {
		t.Fatalf("HostStoreAuthDir(personal) = %q, want %q", got, want)
	}
	if got, want := HostStoreFeatureAuthDir(home, "myfeature"), filepath.Join(stateRoot, "features", "myfeature", "auth"); got != want {
		t.Fatalf("HostStoreFeatureAuthDir() = %q, want %q", got, want)
	}
}

func TestHostImageBuildLockPathKeysByImage(t *testing.T) {
	home := "/tmp/test-home"
	lockPath := HostImageBuildLockPath(home, "enclave-codex:latest")
	if filepath.Dir(lockPath) != HostLocksDir(home) {
		t.Fatalf("HostImageBuildLockPath() = %q, want a file under %q", lockPath, HostLocksDir(home))
	}
	if got := HostImageBuildLockPath(home, "enclave-codex:latest"); got != lockPath {
		t.Fatalf("HostImageBuildLockPath() = %q for the same image, want %q", got, lockPath)
	}
	if got := HostImageBuildLockPath(home, "enclave-gateway-codex:latest"); got == lockPath {
		t.Fatalf("HostImageBuildLockPath() = %q for a different image, want a distinct lock", got)
	}
}

func TestAcquireImageBuildLockCreatesLockFile(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	release, err := AcquireImageBuildLock(home, "enclave-codex:latest")
	if err != nil {
		t.Fatalf("AcquireImageBuildLock returned error: %v", err)
	}
	defer release()
	if _, err := os.Stat(HostImageBuildLockPath(home, "enclave-codex:latest")); err != nil {
		t.Fatalf("expected lock file to exist: %v", err)
	}
}
