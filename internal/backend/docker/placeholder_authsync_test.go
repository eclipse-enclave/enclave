// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestSharedAuthSyncSkipsPlaceholderFiles(t *testing.T) {
	for i, tc := range []struct{ tool, file, payload, placeholder string }{
		{"codex", "auth.json", `{"OPENAI_API_KEY":"ENCLAVE_SECRET_old-session"}`, ""},
		{"pi", "agent/auth.json", `{"anthropic":{"key":"sk-ant-oat01-ENCLAVE_SECRET_old-session"}}`, ""},
		{"codex", "auth.json", `{"token":"at-ENCLAVE_SECRET_old-session"}`, ""},
		{"claude", claudeCredentialsFile, `{"claudeAiOauth":{"accessToken":"ENCLAVE_SECRET_old-session","expiresAt":999}}`, ""},
		{"mistral-vibe", ".env", "MISTRAL_API_KEY=ENCLAVE_SECRET_old-session\n", ""},
		{"custom", "auth.json", `{"key":"vendor_random_end"}`, "vendor_random_end"},
		{"custom", "auth.json", "prefix.a[b]*suffix", "prefix.a[b]*suffix"},
	} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%d/%s", tc.tool, i, authDestinationName(existing)), func(t *testing.T) {
				configDir, authDir := authSyncTempDirs(t)
				src, dst := filepath.Join(configDir, tc.file), filepath.Join(authDir, tc.file)
				writeFile(t, src, tc.payload)
				if existing {
					writeFile(t, dst, claudeCreds(100))
				}
				for range 2 {
					out := runSharedAuthSyncCommandOutput(t, tc.tool, []string{tc.file}, configDir, authDir, "unrelated_placeholder", tc.placeholder)
					if !strings.Contains(out, "contains a gateway placeholder") || !strings.Contains(out, tc.file) {
						t.Fatalf("missing warning: %q", out)
					}
					if tc.placeholder != "" && strings.Contains(out, tc.placeholder) {
						t.Fatal("warning prints the placeholder")
					}
					if existing {
						assertFileContent(t, dst, claudeCreds(100))
					} else if _, err := os.Stat(dst); !os.IsNotExist(err) {
						t.Fatalf("contaminated auth copied: %v", err)
					}
					assertFileContent(t, src, tc.payload)
				}
				matches, err := filepath.Glob(dst + ".tmp.*")
				if err != nil || len(matches) != 0 {
					t.Fatalf("temporary copies remain: %v, %v", matches, err)
				}
			})
		}
	}
}

func authDestinationName(v bool) string {
	if v {
		return "existing-auth"
	}
	return "missing-auth"
}

func TestSharedAuthSyncContinuesAfterContaminatedFile(t *testing.T) {
	configDir, authDir := authSyncTempDirs(t)
	writeFile(t, filepath.Join(configDir, "bad.json"), `{"key":"ENCLAVE_SECRET_old"}`)
	writeFile(t, filepath.Join(configDir, "good.json"), `{"key":"real-key"}`)
	runSharedAuthSyncCommand(t, "tool", []string{"bad.json", "good.json"}, configDir, authDir)
	assertFileContent(t, filepath.Join(authDir, "good.json"), `{"key":"real-key"}`)
	if _, err := os.Stat(filepath.Join(authDir, "bad.json")); !os.IsNotExist(err) {
		t.Fatal("placeholder copied")
	}
}

func TestCopyAuthFileChecksStagedCopy(t *testing.T) {
	configDir, authDir := authSyncTempDirs(t)
	src, dst := filepath.Join(configDir, "auth.json"), filepath.Join(authDir, "auth.json")
	writeFile(t, src, `{"key":"ENCLAVE_SECRET_old"}`)
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "runtime-assets", "auth-reconcile.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "set -e\n"+string(script)+"\nenclave_copy_auth_file \"$1\" \"$2\"", "test", src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "contains a gateway placeholder") {
		t.Fatalf("copy error = %v, output = %s", err, out)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("staged placeholder copied")
	}
	matches, err := filepath.Glob(dst + ".tmp.*")
	if err != nil || len(matches) != 0 {
		t.Fatal("staged copy was not removed")
	}
}

func TestFeatureAuthSyncSkipsPlaceholderFiles(t *testing.T) {
	for _, placeholder := range []string{"ENCLAVE_SECRET_previous", "sk-ant-oat01-ENCLAVE_SECRET_previous", "vendor_custom_end"} {
		for _, existing := range []bool{false, true} {
			t.Run(placeholder+"/"+authDestinationName(existing), func(t *testing.T) {
				root := t.TempDir()
				src, dst := filepath.Join(root, "source"), filepath.Join(root, "target")
				writeFile(t, src, `{"key":"`+placeholder+`"}`)
				if existing {
					writeFile(t, dst, "valid credential")
				}
				if err := copyFeatureAuthFileIfMissing(src, dst, "vendor_custom_end"); err != nil {
					t.Fatal(err)
				}
				if existing {
					assertFileContent(t, dst, "valid credential")
				} else if _, err := os.Stat(dst); !os.IsNotExist(err) {
					t.Fatal("placeholder copied")
				}
			})
		}
	}
}

func TestFinalizeManagedAuthCarriesCustomPlaceholders(t *testing.T) {
	configDir, authDir := authSyncTempDirs(t)
	inspect, err := json.Marshal(dockercmd.InspectResponse{
		Config: &dockercmd.ContainerConfig{Image: "test-image", Labels: map[string]string{model.LabelAgent: "tool"}, Env: []string{
			model.EnvToolConfigDir + "=/config", model.EnvAuthDir + "=/auth", model.EnvAuthFiles + "=auth.json", model.EnvSecretPlaceholders + "=vendor_custom_end",
		}},
		Mounts: []dockercmd.MountPoint{{Type: "bind", Source: configDir, Destination: "/config"}, {Type: "bind", Source: authDir, Destination: "/auth"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := "#!/bin/sh\nif [ \"$1 $2\" = \"container inspect\" ]; then printf '%s' " + util.ShellQuote(string(inspect)) + "; else printf '%s\\n' \"$@\" > " + util.ShellQuote(argsPath) + "; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	b := New(Options{Host: model.Host{Home: t.TempDir()}, ReconcileScriptPath: filepath.Join("..", "..", "..", "runtime-assets", "auth-reconcile.sh")})
	if err := b.finalizeManagedContainerAuth(context.Background(), "managed"); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "ENCLAVE_SECRET_PLACEHOLDERS='vendor_custom_end'") {
		t.Fatalf("custom placeholder missing from reconcile command: %s", args)
	}
}

func TestSharedAuthSyncReportsHelperWarnings(t *testing.T) {
	dir := t.TempDir()
	const warning = "Warning: Not syncing auth file /config/auth.json: contains a gateway placeholder."
	const pullProgress = "Unable to find image locally\nPulling from library/alpine\nPull complete"
	script := "#!/bin/sh\nprintf '%s\\n' " + util.ShellQuote(pullProgress+"\n"+warning+"\nStatus: Downloaded newer image") + " >&2\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	b := New(Options{Host: model.Host{Home: t.TempDir()}, ReconcileScriptPath: filepath.Join("..", "..", "..", "runtime-assets", "auth-reconcile.sh")})
	configDir, authDir := authSyncTempDirs(t)
	var syncErr error
	out := captureStderr(t, func() {
		syncErr = b.syncSharedAuthStores(context.Background(), "", "tool", []string{"auth.json"}, configDir, authDir)
	})
	if syncErr != nil {
		t.Fatal(syncErr)
	}
	want := "Not syncing auth file " + filepath.Join(configDir, "auth.json") + ": contains a gateway placeholder."
	if !strings.Contains(out, want) || strings.Contains(out, "Warning: ") || strings.Contains(out, "auth file /config/") {
		t.Fatalf("incorrect helper warning: %q", out)
	}
	for _, noise := range []string{"Unable to find image", "Pulling", "Pull complete", "Downloaded newer image"} {
		if strings.Contains(out, noise) {
			t.Fatalf("Docker pull progress logged as a warning: %q", out)
		}
	}
}

func TestSharedAuthSyncHelperFailurePreservesDiagnostics(t *testing.T) {
	dir := t.TempDir()
	const diagnostics = "cp: /config/auth.json: Permission denied\nmv: /auth/auth.json: Read-only file system"
	script := "#!/bin/sh\nprintf '%s\\n' " + util.ShellQuote(diagnostics) + " >&2\nexit 7\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	b := New(Options{Host: model.Host{Home: t.TempDir()}, ReconcileScriptPath: filepath.Join("..", "..", "..", "runtime-assets", "auth-reconcile.sh")})
	configDir, authDir := authSyncTempDirs(t)
	var syncErr error
	out := captureStderr(t, func() {
		syncErr = b.syncSharedAuthStores(context.Background(), "", "tool", []string{"auth.json"}, configDir, authDir)
	})
	var exitErr *dockercmd.ExitError
	if !errors.As(syncErr, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("exit status lost: %v", syncErr)
	}
	for _, text := range []string{filepath.Join(configDir, "auth.json"), filepath.Join(authDir, "auth.json"), "Permission denied", "Read-only file system"} {
		if !strings.Contains(syncErr.Error(), text) {
			t.Fatalf("error missing %q: %v", text, syncErr)
		}
	}
	if out != "" {
		t.Fatalf("failure diagnostics logged separately instead of returned: %q", out)
	}
}

func TestMapAuthSyncPathsOnlyRewritesMountRoots(t *testing.T) {
	for _, tc := range []struct {
		name, message, want string
	}{
		{"path at start", "/config/auth.json", "/host/project/config/auth.json"},
		{"nested segments", "cp: /config/x/auth/y.json: denied", "cp: /host/project/config/x/auth/y.json: denied"},
		{"quoted paths", `cp: '/auth/x/config/y.json' to "/config/auth.json"`, `cp: '/host/shared/auth/x/config/y.json' to "/host/project/config/auth.json"`},
		{"multiple lines", "cp: /config/auth.json\nmv: /auth/auth.json", "cp: /host/project/config/auth.json\nmv: /host/shared/auth/auth.json"},
		{"unrelated absolute path", "cp: /other/config/x/auth/y.json: denied", "cp: /other/config/x/auth/y.json: denied"},
		{"image path", "daemon: registry.example/config/image: missing", "daemon: registry.example/config/image: missing"},
		{"relative path", "failed to open cache/auth/token", "failed to open cache/auth/token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mapAuthSyncPaths(tc.message, "/host/project/config", "/host/shared/auth")
			if got != tc.want {
				t.Fatalf("mapped diagnostics = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSharedAuthSyncIgnoresEmptyRegisteredPlaceholders(t *testing.T) {
	configDir, authDir := authSyncTempDirs(t)
	const payload = `{"key":"valid-token"}`
	writeFile(t, filepath.Join(configDir, "auth.json"), payload)
	out := runSharedAuthSyncCommandOutput(t, "tool", []string{"auth.json"}, configDir, authDir, "", "custom-placeholder", "")
	if out != "" {
		t.Fatalf("clean auth file rejected: %q", out)
	}
	assertFileContent(t, filepath.Join(authDir, "auth.json"), payload)
}

func TestRequestAuthSyncCarriesCustomPlaceholders(t *testing.T) {
	logPath := installFakeDocker(t)
	// Foreground sessions may already have auto-removed their container, so
	// the request itself must carry the placeholders to the helper.
	b := New(Options{Host: model.Host{Home: t.TempDir()}, ReconcileScriptPath: filepath.Join("..", "..", "..", "runtime-assets", "auth-reconcile.sh")})
	configKey := backend.StoreKey{Owner: "tool", ProjectHash: "project"}
	authKey := backend.StoreKey{Owner: "tool"}
	b.runRequestAuthSync(backend.Request{
		Session:  backend.SessionMeta{Tool: "tool"},
		Stores:   []backend.PersistentStore{{Key: configKey, Kind: backend.StoreKindConfig}, {Key: authKey, Kind: backend.StoreKindAuth}},
		AuthSync: &backend.AuthSyncSpec{AuthFiles: []string{"auth.json"}},
		Secrets:  []backend.SecretRelease{{Placeholder: "vendor_custom_end", Value: "real-secret"}},
	})
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "ENCLAVE_SECRET_PLACEHOLDERS='vendor_custom_end'") || strings.Contains(string(args), "real-secret") {
		t.Fatalf("incorrect helper args: %s", args)
	}
}
