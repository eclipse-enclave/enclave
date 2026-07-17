// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

const claudeCredentialsFile = ".credentials.json"

func TestMountedSourceDirResolvesBindMountSource(t *testing.T) {
	info := dockercmd.InspectResponse{
		Mounts: []dockercmd.MountPoint{
			{Type: dockercmd.MountTypeVolume, Name: "vol", Source: "/var/lib/docker/x", Destination: "/config"},
			{Type: dockercmd.MountTypeBind, Source: "/host/config", Destination: "/config"},
			{Type: dockercmd.MountTypeBind, Source: "/host/auth", Destination: "/auth/"},
			{Type: dockercmd.MountTypeBind, Source: "", Destination: "/empty"},
			{Type: dockercmd.MountTypeBind, Source: "/host/parent", Destination: "/data"},
		},
	}

	cases := []struct {
		name string
		dest string
		want string
	}{
		{"bind wins over volume at same dest", "/config", "/host/config"},
		{"trailing slash normalized", "/auth", "/host/auth"},
		{"empty source ignored", "/empty", ""},
		{"child destination does not match parent mount", "/data/child", ""},
		{"unknown destination", "/nope", ""},
		{"empty destination", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mountedSourceDir(info, tc.dest); got != tc.want {
				t.Fatalf("mountedSourceDir(%q) = %q, want %q", tc.dest, got, tc.want)
			}
		})
	}
}

func TestCopyFeatureAuthFileIfMissingIsAdditive(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "auth.json")
	dst := filepath.Join(root, "dst", "auth.json")
	if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(src, []byte(`{"token":"new"}`), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	// Missing destination: copied.
	if err := copyFeatureAuthFileIfMissing(src, dst); err != nil {
		t.Fatalf("copy into missing dst: %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != `{"token":"new"}` {
		t.Fatalf("dst after first copy = %q err=%v", data, err)
	}

	// Existing non-empty destination: never overwritten.
	if err := os.WriteFile(dst, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatalf("rewrite dst: %v", err)
	}
	if err := copyFeatureAuthFileIfMissing(src, dst); err != nil {
		t.Fatalf("copy over existing dst: %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != `{"token":"old"}` {
		t.Fatalf("dst after additive copy = %q err=%v, want kept old", data, err)
	}
}

func TestCopyFeatureAuthFileIfMissingSkipsMissingOrEmptySource(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "dst", "auth.json")

	// Missing source: no-op, no dst created.
	if err := copyFeatureAuthFileIfMissing(filepath.Join(root, "nope"), dst); err != nil {
		t.Fatalf("missing source should be a no-op, got %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dst should not exist after missing-source copy, err=%v", err)
	}

	// Empty source: no-op.
	empty := filepath.Join(root, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty src: %v", err)
	}
	if err := copyFeatureAuthFileIfMissing(empty, dst); err != nil {
		t.Fatalf("empty source should be a no-op, got %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dst should not exist after empty-source copy, err=%v", err)
	}
}

func TestSyncFeatureAuthStoreCopiesIntoFeatureStoreDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	b := New(Options{Host: model.Host{Home: home, UID: "1000", GID: "1000"}})

	configDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configDir, "pw"), 0o700); err != nil {
		t.Fatalf("mkdir config subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "pw", "auth.json"), []byte(`{"token":"keep"}`), 0o600); err != nil {
		t.Fatalf("write config auth: %v", err)
	}

	sync := backend.FeatureAuthSync{Feature: "playwright", ConfigDir: "pw", AuthFiles: []string{"auth.json"}}
	if err := b.syncFeatureAuthStore(context.Background(), configDir, sync); err != nil {
		t.Fatalf("syncFeatureAuthStore() error = %v", err)
	}

	dst := filepath.Join(config.HostStoreFeatureAuthDir(home, "playwright"), "auth.json")
	if data, err := os.ReadFile(dst); err != nil || string(data) != `{"token":"keep"}` {
		t.Fatalf("feature auth store file = %q err=%v", data, err)
	}
}

func TestSyncFeatureAuthStoreRejectsSymlinkedSource(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	b := New(Options{Host: model.Host{Home: home, UID: "1000", GID: "1000"}})

	configDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(configDir, "auth.json")); err != nil {
		t.Fatalf("symlink config auth: %v", err)
	}

	sync := backend.FeatureAuthSync{Feature: "playwright", ConfigDir: "", AuthFiles: []string{"auth.json"}}
	if err := b.syncFeatureAuthStore(context.Background(), configDir, sync); err == nil {
		t.Fatalf("expected symlinked source to be rejected")
	}

	if _, err := os.Stat(filepath.Join(config.HostStoreFeatureAuthDir(home, "playwright"), "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("symlinked source must not be copied into the feature store, err=%v", err)
	}
}

func TestSharedAuthSyncCommandClaudeCopiesMissingDestination(t *testing.T) {
	t.Parallel()
	configDir, authDir := authSyncTempDirs(t)
	writeFile(t, filepath.Join(configDir, claudeCredentialsFile), claudeCreds(200))

	runSharedAuthSyncCommand(t, "claude", []string{claudeCredentialsFile}, configDir, authDir)

	assertFileContent(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(200))
}

func TestSharedAuthSyncCommandClaudeNewerConfigWins(t *testing.T) {
	t.Parallel()
	configDir, authDir := authSyncTempDirs(t)
	writeFile(t, filepath.Join(configDir, claudeCredentialsFile), claudeCreds(300))
	writeFile(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(100))

	runSharedAuthSyncCommand(t, "claude", []string{claudeCredentialsFile}, configDir, authDir)

	assertFileContent(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(300))
}

func TestSharedAuthSyncCommandClaudeOlderConfigLoses(t *testing.T) {
	t.Parallel()
	configDir, authDir := authSyncTempDirs(t)
	writeFile(t, filepath.Join(configDir, claudeCredentialsFile), claudeCreds(100))
	writeFile(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(300))

	runSharedAuthSyncCommand(t, "claude", []string{claudeCredentialsFile}, configDir, authDir)

	assertFileContent(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(300))
}

func TestSharedAuthSyncCommandClaudeInvalidConfigDoesNotOverwriteValidShared(t *testing.T) {
	t.Parallel()
	configDir, authDir := authSyncTempDirs(t)
	writeFile(t, filepath.Join(configDir, claudeCredentialsFile), `{"claudeAiOauth": {`)
	writeFile(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(300))

	runSharedAuthSyncCommand(t, "claude", []string{claudeCredentialsFile}, configDir, authDir)

	assertFileContent(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(300))
}

func TestSharedAuthSyncCommandClaudeSymlinkConfigNoops(t *testing.T) {
	t.Parallel()
	configDir, authDir := authSyncTempDirs(t)
	sharedPath := filepath.Join(authDir, claudeCredentialsFile)
	writeFile(t, sharedPath, claudeCreds(300))
	if err := os.Symlink(sharedPath, filepath.Join(configDir, claudeCredentialsFile)); err != nil {
		t.Fatalf("symlink config credential: %v", err)
	}

	out := runSharedAuthSyncCommandOutput(t, "claude", []string{claudeCredentialsFile}, configDir, authDir)

	assertFileContent(t, sharedPath, claudeCreds(300))
	if out != "" {
		t.Fatalf("expected no drift warning for symlinked Claude credentials, got %q", out)
	}
}

func TestSharedAuthSyncCommandClaudeRealConfigWarnsAboutSecurestorageDrift(t *testing.T) {
	t.Parallel()
	configDir, authDir := authSyncTempDirs(t)
	writeFile(t, filepath.Join(configDir, claudeCredentialsFile), claudeCreds(300))
	writeFile(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(100))

	out := runSharedAuthSyncCommandOutput(t, "claude", []string{claudeCredentialsFile}, configDir, authDir)

	want := "Warning: Claude wrote a real .credentials.json in its config dir; shared secure storage may not be active.\n"
	if out != want {
		t.Fatalf("drift warning = %q, want %q", out, want)
	}
}

func TestSharedAuthSyncCommandClaudePrettyJSONUsesScopedExpiresAt(t *testing.T) {
	t.Parallel()
	configDir, authDir := authSyncTempDirs(t)
	writeFile(t, filepath.Join(configDir, claudeCredentialsFile), `{
  "other": {"expiresAt": 1},
  "claudeAiOauth": {
    "accessToken": "token",
    "refreshToken": "refresh",
    "expiresAt": 300
  }
}`)
	writeFile(t, filepath.Join(authDir, claudeCredentialsFile), claudeCreds(100))

	runSharedAuthSyncCommand(t, "claude", []string{claudeCredentialsFile}, configDir, authDir)

	assertFileContent(t, filepath.Join(authDir, claudeCredentialsFile), `{
  "other": {"expiresAt": 1},
  "claudeAiOauth": {
    "accessToken": "token",
    "refreshToken": "refresh",
    "expiresAt": 300
  }
}`)
}

func TestSharedAuthSyncCommandGenericRemainsAdditive(t *testing.T) {
	t.Parallel()
	configDir, authDir := authSyncTempDirs(t)
	writeFile(t, filepath.Join(configDir, "agent", "auth.json"), `{"token":"new"}`)
	writeFile(t, filepath.Join(authDir, "agent", "auth.json"), `{"token":"old"}`)

	runSharedAuthSyncCommand(t, "pi", []string{"agent/auth.json"}, configDir, authDir)

	assertFileContent(t, filepath.Join(authDir, "agent", "auth.json"), `{"token":"old"}`)
}

// A named auth identity (--auth-name) is just a different bind-mount source
// directory; reconcile must resolve exactly the mounted identity dir.
func TestMountedSourceDirResolvesNamedAuthIdentityMount(t *testing.T) {
	authDir := "/home/agent/.enclave-auth"
	info := dockercmd.InspectResponse{
		Mounts: []dockercmd.MountPoint{
			{Type: dockercmd.MountTypeBind, Source: "/host/dir", Destination: "/workspace"},
			{Type: dockercmd.MountTypeBind, Source: "/home/u/.local/state/enclave/tools/codex/auth/personal", Destination: authDir},
		},
	}
	if got := mountedSourceDir(info, authDir+"/"); got != "/home/u/.local/state/enclave/tools/codex/auth/personal" {
		t.Fatalf("mountedSourceDir() = %q, want the named identity dir", got)
	}
	// Unmounted path or missing mounts resolve to empty so sync no-ops rather
	// than guessing a source.
	if got := mountedSourceDir(info, "/somewhere/else"); got != "" {
		t.Fatalf("mountedSourceDir() for unmounted path = %q, want empty", got)
	}
	if got := mountedSourceDir(dockercmd.InspectResponse{}, authDir); got != "" {
		t.Fatalf("mountedSourceDir() with no mounts = %q, want empty", got)
	}
}

func authSyncTempDirs(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	authDir := filepath.Join(root, "auth")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("mkdir auth: %v", err)
	}
	return configDir, authDir
}

func runSharedAuthSyncCommand(t *testing.T, tool string, authFiles []string, configDir string, authDir string) {
	t.Helper()
	_ = runSharedAuthSyncCommandOutput(t, tool, authFiles, configDir, authDir)
}

func runSharedAuthSyncCommandOutput(t *testing.T, tool string, authFiles []string, configDir string, authDir string) string {
	t.Helper()
	scriptPath := filepath.Join("..", "..", "..", "runtime-assets", "auth-reconcile.sh")
	cmdText := sharedAuthSyncCommand(scriptPath, tool, authFiles, "", configDir, authDir)
	cmd := exec.Command("sh", "-c", cmdText)
	cmd.Env = append(os.Environ(), "PATH="+fakeJQDir(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shared auth sync command failed: %v\n%s\nscript:\n%s", err, string(out), cmdText)
	}
	return string(out)
}

func fakeJQDir(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available to provide test jq shim")
	}
	dir := t.TempDir()
	jqPath := filepath.Join(dir, "jq")
	script := `#!/bin/sh
file=""
for arg do
  file=$arg
done
exec ` + python + ` - "$file" <<'PY'
import json
import sys
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        payload = json.load(f)
    value = (payload.get("claudeAiOauth") or {}).get("expiresAt")
except Exception:
    value = None
if value is not None:
    print(value)
PY
`
	if err := os.WriteFile(jqPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write jq shim: %v", err)
	}
	return dir
}

func claudeCreds(expiresAt int) string {
	return `{"claudeAiOauth":{"expiresAt":` + strconv.Itoa(expiresAt) + `}}`
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}

func TestSharedAuthSyncHostConfigRelabelsForSELinux(t *testing.T) {
	hostConfig := sharedAuthSyncHostConfig("/host/config", "/host/auth", "/host/reconcile.sh", true)

	if len(hostConfig.Mounts) != 0 {
		t.Fatalf("Mounts = %v, want none (binds should carry the relabel flag)", hostConfig.Mounts)
	}
	want := []string{
		"/host/config:/config:ro,z",
		"/host/auth:/auth:z",
		"/host/reconcile.sh:/auth-reconcile.sh:ro,z",
	}
	if len(hostConfig.Binds) != len(want) {
		t.Fatalf("Binds = %v, want %v", hostConfig.Binds, want)
	}
	for i, bind := range hostConfig.Binds {
		if bind != want[i] {
			t.Errorf("Binds[%d] = %q, want %q", i, bind, want[i])
		}
	}
	if !hostConfig.AutoRemove {
		t.Error("AutoRemove = false, want true")
	}
}

func TestSharedAuthSyncHostConfigWithoutSELinux(t *testing.T) {
	hostConfig := sharedAuthSyncHostConfig("/host/config", "/host/auth", "/host/reconcile.sh", false)

	if len(hostConfig.Binds) != 0 {
		t.Fatalf("Binds = %v, want none without SELinux", hostConfig.Binds)
	}
	if len(hostConfig.Mounts) != 3 {
		t.Fatalf("Mounts = %v, want 3 bind mounts", hostConfig.Mounts)
	}
	script := hostConfig.Mounts[2]
	if script.Type != dockercmd.MountTypeBind || script.Source != "/host/reconcile.sh" ||
		script.Target != "/auth-reconcile.sh" || !script.ReadOnly {
		t.Errorf("script mount = %+v, want read-only bind /host/reconcile.sh -> /auth-reconcile.sh", script)
	}
}
