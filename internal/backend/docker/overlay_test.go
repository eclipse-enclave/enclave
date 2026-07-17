// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"enclave/internal/model"
	yaruntime "enclave/internal/runtime"
)

func requireJQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected %s to be removed", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func TestHostConfigOverlayRunsBeforeClaudeSetup(t *testing.T) {
	t.Parallel()
	requireJQ(t)

	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	sourceDir := filepath.Join(home, "source")
	targetDir := filepath.Join(home, "target", ".claude")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte(`{"model":"claude-sonnet-4-20250514"}`), 0o644); err != nil {
		t.Fatalf("write source settings: %v", err)
	}

	if err := overlayConfigDir(targetDir, sourceDir, []string{".enclave-devcontainer/"}); err != nil {
		t.Fatalf("overlay failed: %v", err)
	}

	entrypointPath := filepath.Join("..", "..", "..", "entrypoint.sh")
	toolsDir := filepath.Join("..", "..", "..", "extensions", "tools")
	cmd := exec.Command("bash", entrypointPath, "true")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=claude",
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
		"ENCLAVE_YOLO=1",
		"ENCLAVE_TOOL_CONFIG_DIR=" + targetDir,
		"ENCLAVE_TOOL_SETTINGS_TARGET=" + filepath.Join(targetDir, "settings.json"),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, string(out))
	}

	settingsBytes, err := os.ReadFile(filepath.Join(targetDir, "settings.json"))
	if err != nil {
		t.Fatalf("read copied settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("parse copied settings: %v\ncontent:\n%s", err, string(settingsBytes))
	}
	if settings["model"] != "claude-sonnet-4-20250514" {
		t.Fatalf("expected model to survive overlay, got %#v", settings["model"])
	}
	if settings["skipDangerousModePermissionPrompt"] != true {
		t.Fatalf("expected Claude YOLO mutation after overlay, got %#v", settings["skipDangerousModePermissionPrompt"])
	}
}

func TestOverlayConfigDirRemovesStaleFiles(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	sourceDir := filepath.Join(home, "source")
	targetDir := filepath.Join(home, "target", ".claude")
	if err := os.MkdirAll(filepath.Join(sourceDir, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir source commands: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetDir, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir target commands: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte(`{"source":"fresh"}`), 0o644); err != nil {
		t.Fatalf("write source settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "commands", "fresh.md"), []byte("fresh"), 0o644); err != nil {
		t.Fatalf("write source command: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "stale.json"), []byte(`{"source":"stale"}`), 0o644); err != nil {
		t.Fatalf("write stale settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "commands", "stale.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale command: %v", err)
	}

	if err := overlayConfigDir(targetDir, sourceDir, []string{".enclave-devcontainer/"}); err != nil {
		t.Fatalf("overlay failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(targetDir, "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("expected stale settings to be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "commands", "stale.md")); !os.IsNotExist(err) {
		t.Fatalf("expected stale command to be removed, err=%v", err)
	}
	freshSettings, err := os.ReadFile(filepath.Join(targetDir, "settings.json"))
	if err != nil {
		t.Fatalf("read fresh settings: %v", err)
	}
	if string(freshSettings) != `{"source":"fresh"}` {
		t.Fatalf("unexpected fresh settings content: %s", string(freshSettings))
	}
	freshCommand, err := os.ReadFile(filepath.Join(targetDir, "commands", "fresh.md"))
	if err != nil {
		t.Fatalf("read fresh command: %v", err)
	}
	if string(freshCommand) != "fresh" {
		t.Fatalf("unexpected fresh command content: %s", string(freshCommand))
	}
}

func TestOverlayConfigDirPreservesDevcontainerStamps(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	sourceDir := filepath.Join(home, "source")
	targetDir := filepath.Join(home, "target", ".claude")
	stampDir := filepath.Join(targetDir, ".enclave-devcontainer")
	stampFile := filepath.Join(stampDir, "postCreateCommand-abc123")
	if err := os.MkdirAll(stampDir, 0o755); err != nil {
		t.Fatalf("mkdir stamp dir: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(stampFile, []byte("stamp"), 0o644); err != nil {
		t.Fatalf("write stamp file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "stale.json"), []byte(`{"source":"stale"}`), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte(`{"source":"fresh"}`), 0o644); err != nil {
		t.Fatalf("write source settings: %v", err)
	}

	if err := overlayConfigDir(targetDir, sourceDir, []string{".enclave-devcontainer/"}); err != nil {
		t.Fatalf("overlay failed: %v", err)
	}

	if _, err := os.Stat(stampFile); err != nil {
		t.Fatalf("expected devcontainer stamp to survive overlay, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("expected stale file to be removed, err=%v", err)
	}
	freshSettings, err := os.ReadFile(filepath.Join(targetDir, "settings.json"))
	if err != nil {
		t.Fatalf("read fresh settings: %v", err)
	}
	if string(freshSettings) != `{"source":"fresh"}` {
		t.Fatalf("unexpected fresh settings content: %s", string(freshSettings))
	}
}

func TestOverlayConfigDirPreservesRuntimeState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	targetDir := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(sourceDir, "agent"), 0o755); err != nil {
		t.Fatalf("mkdir source agent: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetDir, "agent"), 0o755); err != nil {
		t.Fatalf("mkdir target agent: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir target sessions: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetDir, ".enclave-devcontainer"), 0o755); err != nil {
		t.Fatalf("mkdir target devcontainer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte(`{"source":"fresh"}`), 0o644); err != nil {
		t.Fatalf("write source settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "agent", "settings.json"), []byte(`{"source":"generated"}`), 0o644); err != nil {
		t.Fatalf("write source nested settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "stale.json"), []byte(`{"source":"stale"}`), 0o644); err != nil {
		t.Fatalf("write stale top-level file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "agent", "stale.json"), []byte(`{"source":"stale"}`), 0o644); err != nil {
		t.Fatalf("write stale nested file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "agent", "auth.json"), []byte(`{"token":"keep"}`), 0o600); err != nil {
		t.Fatalf("write nested auth file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "sessions", "resume.json"), []byte(`{"id":"keep"}`), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, ".enclave-devcontainer", "stamp"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write devcontainer stamp: %v", err)
	}

	profile := model.Profile{
		Name: "pi",
		Providers: []model.ProviderConfig{
			{Name: "openai-codex", AuthFiles: []string{"agent/auth.json"}},
		},
	}

	if err := overlayConfigDir(targetDir, sourceDir, yaruntime.ConfigSourcePreservePaths(profile)); err != nil {
		t.Fatalf("overlay failed: %v", err)
	}

	assertPathMissing(t, filepath.Join(targetDir, "stale.json"))
	assertPathMissing(t, filepath.Join(targetDir, "agent", "stale.json"))
	freshSettings, err := os.ReadFile(filepath.Join(targetDir, "settings.json"))
	if err != nil {
		t.Fatalf("read fresh settings: %v", err)
	}
	if string(freshSettings) != `{"source":"fresh"}` {
		t.Fatalf("unexpected fresh settings content: %s", string(freshSettings))
	}
	nestedSettings, err := os.ReadFile(filepath.Join(targetDir, "agent", "settings.json"))
	if err != nil {
		t.Fatalf("read generated nested settings: %v", err)
	}
	if string(nestedSettings) != `{"source":"generated"}` {
		t.Fatalf("unexpected generated nested settings content: %s", string(nestedSettings))
	}
	nestedAuth, err := os.ReadFile(filepath.Join(targetDir, "agent", "auth.json"))
	if err != nil {
		t.Fatalf("read preserved nested auth: %v", err)
	}
	if string(nestedAuth) != `{"token":"keep"}` {
		t.Fatalf("unexpected preserved nested auth content: %s", string(nestedAuth))
	}
	sessionBytes, err := os.ReadFile(filepath.Join(targetDir, "sessions", "resume.json"))
	if err != nil {
		t.Fatalf("read preserved session file: %v", err)
	}
	if string(sessionBytes) != `{"id":"keep"}` {
		t.Fatalf("unexpected preserved session content: %s", string(sessionBytes))
	}
	stampBytes, err := os.ReadFile(filepath.Join(targetDir, ".enclave-devcontainer", "stamp"))
	if err != nil {
		t.Fatalf("read preserved devcontainer stamp: %v", err)
	}
	if string(stampBytes) != "keep" {
		t.Fatalf("unexpected preserved stamp content: %s", string(stampBytes))
	}
}

// TestOverlayConfigDirPreservesSessionsBesideAuth reproduces the pi session
// loss: agent/sessions/ and agent/auth.json are both preserved and share the
// agent/ parent. Staging the auth sibling first creates preserve/agent, which
// must not make the walk treat agent/sessions/ as already handled and skip it.
func TestOverlayConfigDirPreservesSessionsBesideAuth(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	targetDir := filepath.Join(root, "target")

	// config-generated repopulate source: fresh settings, no sessions.
	if err := os.MkdirAll(filepath.Join(sourceDir, "agent"), 0o755); err != nil {
		t.Fatalf("mkdir source agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "agent", "settings.json"), []byte(`{"source":"generated"}`), 0o644); err != nil {
		t.Fatalf("write source settings: %v", err)
	}

	// Store before wipe: auth.json (created first, so the walk tends to reach
	// it before sessions) beside the real pi session layout agent/sessions/.
	if err := os.MkdirAll(filepath.Join(targetDir, "agent", "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir target sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "agent", "auth.json"), []byte(`{"token":"keep"}`), 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "agent", "sessions", "resume.jsonl"), []byte(`{"id":"keep"}`), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	profile := model.Profile{
		Name: "pi",
		Providers: []model.ProviderConfig{
			{Name: "openai-codex", AuthFiles: []string{"agent/auth.json"}},
		},
	}

	if err := overlayConfigDir(targetDir, sourceDir, yaruntime.ConfigSourcePreservePaths(profile)); err != nil {
		t.Fatalf("overlay failed: %v", err)
	}

	sessionBytes, err := os.ReadFile(filepath.Join(targetDir, "agent", "sessions", "resume.jsonl"))
	if err != nil {
		t.Fatalf("agent/sessions/ was not preserved across the config-store wipe: %v", err)
	}
	if string(sessionBytes) != `{"id":"keep"}` {
		t.Fatalf("unexpected preserved session content: %s", string(sessionBytes))
	}
	authBytes, err := os.ReadFile(filepath.Join(targetDir, "agent", "auth.json"))
	if err != nil {
		t.Fatalf("read preserved auth: %v", err)
	}
	if string(authBytes) != `{"token":"keep"}` {
		t.Fatalf("unexpected preserved auth content: %s", string(authBytes))
	}
}
