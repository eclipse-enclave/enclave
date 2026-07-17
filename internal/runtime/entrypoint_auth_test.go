// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEntrypointClaudeCredentialsNewerConfigUpdatesSharedAndLinks(t *testing.T) {
	t.Parallel()
	home, configDir, authDir := entrypointAuthDirs(t)
	configPath := filepath.Join(configDir, claudeCredentialsFile)
	authPath := filepath.Join(authDir, claudeCredentialsFile)
	writeFile(t, configPath, claudeCreds(300))
	writeFile(t, authPath, claudeCreds(100))

	runEntrypointAuth(t, home, configDir, authDir)

	assertFileContent(t, authPath, claudeCreds(300))
	assertSymlinkTarget(t, configPath, authPath)
}

func TestEntrypointClaudeCredentialsNewerSharedKeepsSharedAndLinks(t *testing.T) {
	t.Parallel()
	home, configDir, authDir := entrypointAuthDirs(t)
	configPath := filepath.Join(configDir, claudeCredentialsFile)
	authPath := filepath.Join(authDir, claudeCredentialsFile)
	writeFile(t, configPath, claudeCreds(100))
	writeFile(t, authPath, claudeCreds(300))

	runEntrypointAuth(t, home, configDir, authDir)

	assertFileContent(t, authPath, claudeCreds(300))
	assertSymlinkTarget(t, configPath, authPath)
}

func TestEntrypointClaudeJsonRootPathPersistsThroughConfigVolume(t *testing.T) {
	t.Parallel()
	requireJQ(t)
	home, configDir, authDir := entrypointAuthDirs(t)
	writeFile(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(300))

	runEntrypointAuth(t, home, configDir, authDir, "ENCLAVE_YOLO=1")

	assertSymlinkTarget(t, filepath.Join(home, ".claude.json"), filepath.Join(configDir, ".claude.json"))
	if _, err := os.Stat(filepath.Join(configDir, ".claude.json")); err != nil {
		t.Fatalf("expected persisted .claude.json target: %v", err)
	}
}

func entrypointAuthDirs(t *testing.T) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".claude")
	authDir := filepath.Join(home, ".enclave-auth")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("mkdir auth: %v", err)
	}
	return home, configDir, authDir
}

func runEntrypointAuth(t *testing.T, home string, configDir string, authDir string, extraEnv ...string) {
	t.Helper()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	entrypointPath := filepath.Join("..", "..", "entrypoint.sh")
	toolsDir := filepath.Join("..", "..", "extensions", "tools")
	reconcileLib := filepath.Join("..", "..", "runtime-assets", "auth-reconcile.sh")
	cmd := exec.Command("bash", entrypointPath, "true")
	env := []string{
		"PATH=" + fakeJQDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=claude",
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
		"ENCLAVE_TOOL_CONFIG_DIR=" + configDir,
		"ENCLAVE_AUTH_DIR=" + authDir,
		"ENCLAVE_AUTH_FILES=" + claudeCredentialsFile,
		"ENCLAVE_AUTH_RECONCILE_LIB=" + reconcileLib,
	}
	env = append(env, extraEnv...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, string(out))
	}
}

func assertSymlinkTarget(t *testing.T, path string, want string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", path)
	}
	got, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	if got != want {
		t.Fatalf("symlink %s -> %q, want %q", path, got, want)
	}
}
