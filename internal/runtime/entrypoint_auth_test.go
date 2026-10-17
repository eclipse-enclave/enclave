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
	"strconv"
	"testing"

	"enclave/internal/model"
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

func TestEntrypointAuthDoesNotSeedPlaceholder(t *testing.T) {
	for _, token := range []string{"ENCLAVE_SECRET_previous-session", "vendor_custom_end"} {
		t.Run(token, func(t *testing.T) {
			home, configDir, authDir := entrypointAuthDirs(t)
			payload := `{"claudeAiOauth":{"accessToken":"` + token + `","expiresAt":999}}`
			writeFile(t, filepath.Join(configDir, claudeCredentialsFile), payload)
			runEntrypointAuth(t, home, configDir, authDir, model.EnvSecretPlaceholders+"=other_placeholder\nvendor_custom_end")
			if _, err := os.Stat(filepath.Join(authDir, claudeCredentialsFile)); !os.IsNotExist(err) {
				t.Fatal("placeholder seeded into shared auth")
			}
			assertFileContent(t, filepath.Join(configDir, claudeCredentialsFile), payload)
		})
	}
}

func TestEntrypointFeatureAuthDoesNotSeedPlaceholder(t *testing.T) {
	home, configDir, authDir := entrypointAuthDirs(t)
	src := filepath.Join(home, ".config", "gh", "hosts.yml")
	writeFile(t, src, "github.com:\n  oauth_token: ENCLAVE_SECRET_previous-session\n")
	runEntrypointAuth(t, home, configDir, authDir, model.EnvFeatureAuthMap+"=github-cli:.config/gh:hosts.yml")
	sharedPath := filepath.Join(home, ".enclave-feature-auth", "github-cli", "hosts.yml")
	assertFileContent(t, sharedPath, "")
	assertSymlinkTarget(t, src, sharedPath)
}

func TestEntrypointAuthRelinksContaminatedLocalToShared(t *testing.T) {
	for _, tc := range []struct{ tool, file string }{
		{"claude", claudeCredentialsFile}, {"codex", "auth.json"}, {"pi", "agent/auth.json"},
	} {
		for _, token := range []string{"ENCLAVE_SECRET_previous-session", "vendor_custom_end"} {
			t.Run(tc.tool+"/"+token, func(t *testing.T) {
				home, configDir, authDir := entrypointAuthDirs(t)
				localPath, sharedPath := filepath.Join(configDir, tc.file), filepath.Join(authDir, tc.file)
				writeFile(t, localPath, `{"claudeAiOauth":{"accessToken":"`+token+`","expiresAt":999}}`)
				shared := `{"claudeAiOauth":{"accessToken":"valid-token","expiresAt":100}}`
				writeFile(t, sharedPath, shared)
				runEntrypointAuth(t, home, configDir, authDir, "TOOL="+tc.tool, model.EnvAuthFiles+"="+tc.file, model.EnvSecretPlaceholders+"=vendor_custom_end")
				assertFileContent(t, sharedPath, shared)
				assertSymlinkTarget(t, localPath, sharedPath)
				assertFileContent(t, localPath, shared)
			})
		}
	}
}

func TestEntrypointFeatureAuthRelinksContaminatedLocalToShared(t *testing.T) {
	for _, token := range []string{"ENCLAVE_SECRET_previous-session", "vendor_custom_end"} {
		t.Run(token, func(t *testing.T) {
			home, configDir, authDir := entrypointAuthDirs(t)
			localPath := filepath.Join(home, ".config", "gh", "hosts.yml")
			sharedPath := filepath.Join(home, ".enclave-feature-auth", "github-cli", "hosts.yml")
			writeFile(t, localPath, "github.com:\n  oauth_token: "+token+"\n")
			const shared = "github.com:\n  oauth_token: valid-token\n"
			writeFile(t, sharedPath, shared)
			runEntrypointAuth(t, home, configDir, authDir, model.EnvFeatureAuthMap+"=github-cli:.config/gh:hosts.yml", model.EnvSecretPlaceholders+"=vendor_custom_end")
			assertFileContent(t, sharedPath, shared)
			assertSymlinkTarget(t, localPath, sharedPath)
			assertFileContent(t, localPath, shared)
		})
	}
}

func TestEntrypointCodexRemovesStaleLocalPlaceholder(t *testing.T) {
	for _, sharedScope := range []bool{false, true} {
		for _, token := range []string{"ENCLAVE_SECRET_previous-session", "at-ENCLAVE_SECRET_previous-session"} {
			t.Run(token+"/shared="+strconv.FormatBool(sharedScope), func(t *testing.T) {
				home, _, authDir := entrypointAuthDirs(t)
				configDir := filepath.Join(home, ".codex")
				authFile := filepath.Join(configDir, "auth.json")
				writeFile(t, authFile, `{"auth_mode":"apikey","OPENAI_API_KEY":"`+token+`"}`)
				if !sharedScope {
					authDir = ""
				}
				runEntrypointAuth(t, home, configDir, authDir, "TOOL=codex", model.EnvAuthFiles+"=auth.json")
				if _, err := os.Lstat(authFile); !os.IsNotExist(err) {
					t.Fatalf("stale local auth file remains: %v", err)
				}
			})
		}
	}
}

func TestEntrypointCodexPreservesStoredLogin(t *testing.T) {
	for _, sharedScope := range []bool{false, true} {
		t.Run(strconv.FormatBool(sharedScope), func(t *testing.T) {
			home, _, authDir := entrypointAuthDirs(t)
			configDir := filepath.Join(home, ".codex")
			authFile := filepath.Join(configDir, "auth.json")
			const shared = `{"auth_mode":"chatgpt","tokens":{"access_token":"stored-login"}}`
			if sharedScope {
				writeFile(t, authFile, `{"OPENAI_API_KEY":"ENCLAVE_SECRET_previous-session"}`)
				writeFile(t, filepath.Join(authDir, "auth.json"), shared)
			} else {
				authDir = ""
				writeFile(t, authFile, shared)
			}
			runEntrypointAuth(t, home, configDir, authDir, "TOOL=codex", model.EnvAuthFiles+"=auth.json", "CODEX_API_KEY=ENCLAVE_SECRET_current-session", "OPENAI_API_KEY=ENCLAVE_SECRET_current-session")
			assertFileContent(t, authFile, shared)
			if sharedScope {
				assertSymlinkTarget(t, authFile, filepath.Join(authDir, "auth.json"))
			}
		})
	}
}

func TestEntrypointCodexDoesNotRemoveContaminatedSharedAuth(t *testing.T) {
	home, _, authDir := entrypointAuthDirs(t)
	configDir := filepath.Join(home, ".codex")
	sharedFile := filepath.Join(authDir, "auth.json")
	const contaminated = `{"OPENAI_API_KEY":"ENCLAVE_SECRET_previous-session"}`
	writeFile(t, sharedFile, contaminated)
	runEntrypointAuth(t, home, configDir, authDir, "TOOL=codex", model.EnvAuthFiles+"=auth.json")
	assertFileContent(t, sharedFile, contaminated)
	assertSymlinkTarget(t, filepath.Join(configDir, "auth.json"), sharedFile)
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
