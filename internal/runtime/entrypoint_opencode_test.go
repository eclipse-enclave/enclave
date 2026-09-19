// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEntrypointOpencodePersistsDataAndStateAndSeedsAuth(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "opencode")
	dataDir := filepath.Join(home, ".local", "share", "opencode")
	stateDir := filepath.Join(home, ".local", "state", "opencode")
	stateStoreDir := filepath.Join(configDir, "xdg-state")
	sharedAuthDir := filepath.Join(home, "shared-auth")

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	// OpenCode creates both directories on every start, so the image build's
	// `opencode --version` probe bakes them in as real directories. Reproduce
	// that here: a guard that only symlinks when the path is absent would
	// silently leave state in container-local storage.
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.MkdirAll(sharedAuthDir, 0o755); err != nil {
		t.Fatalf("mkdir shared auth: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dataDir, "session.db"), []byte("persist me"), 0o600); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "model.json"), []byte("favorite model"), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sharedAuthDir, "auth.json"), []byte(`{"github-copilot":{"token":"abc"}}`), 0o600); err != nil {
		t.Fatalf("write shared auth: %v", err)
	}

	scriptPath := filepath.Join("..", "..", "extensions", "tools", "opencode", "entrypoint.d", "setup.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"ENCLAVE_AUTH_DIR=" + sharedAuthDir,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup.sh failed: %v\noutput:\n%s", err, string(out))
	}

	for link, wantTarget := range map[string]string{
		dataDir:  configDir,
		stateDir: stateStoreDir,
	} {
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("stat %s: %v", link, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is not a symlink", link)
		}
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("readlink %s: %v", link, err)
		}
		if target != wantTarget {
			t.Fatalf("symlink target for %s = %q, want %q", link, target, wantTarget)
		}
	}

	dataBytes, err := os.ReadFile(filepath.Join(configDir, "session.db"))
	if err != nil {
		t.Fatalf("read migrated data: %v", err)
	}
	if string(dataBytes) != "persist me" {
		t.Fatalf("migrated data = %q, want %q", string(dataBytes), "persist me")
	}

	stateBytes, err := os.ReadFile(filepath.Join(stateStoreDir, "model.json"))
	if err != nil {
		t.Fatalf("read migrated state: %v", err)
	}
	if string(stateBytes) != "favorite model" {
		t.Fatalf("migrated state = %q, want %q", string(stateBytes), "favorite model")
	}

	if err := os.WriteFile(filepath.Join(stateDir, "tui.json"), []byte("recent models"), 0o600); err != nil {
		t.Fatalf("write state through symlink: %v", err)
	}
	tuiBytes, err := os.ReadFile(filepath.Join(stateStoreDir, "tui.json"))
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
	if string(tuiBytes) != "recent models" {
		t.Fatalf("persisted state = %q, want %q", string(tuiBytes), "recent models")
	}

	authBytes, err := os.ReadFile(filepath.Join(configDir, "auth.json"))
	if err != nil {
		t.Fatalf("read seeded auth: %v", err)
	}
	var gotAuth map[string]json.RawMessage
	if err := json.Unmarshal(authBytes, &gotAuth); err != nil {
		t.Fatalf("parse seeded auth: %v", err)
	}
	if len(gotAuth) != 1 {
		t.Fatalf("seeded auth has %d keys, want 1", len(gotAuth))
	}
	if _, ok := gotAuth["github-copilot"]; !ok {
		t.Fatalf("seeded auth missing github-copilot key, got %s", string(authBytes))
	}
}
