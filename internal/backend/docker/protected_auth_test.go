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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

func protectedAuthBackend(t *testing.T) (*Backend, backend.StoreKey, backend.StoreKey, string, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})
	b.reconcileScript = "enclave_sync_shared_auth() { :; }\n"
	b.reconcileScriptErr = nil
	authKey := backend.StoreKey{Owner: "claude", Suffix: "protected"}
	configKey := backend.StoreKey{Owner: "claude", ProjectHash: "project"}
	authDir := b.resolveStoreDir(authKey, backend.StoreKindAuth)
	configDir := b.resolveStoreDir(configKey, backend.StoreKindConfig)
	if err := hoststore.ProtectIdentity(b.opts.Host.Home, authDir, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(authDir, "oauth-grant.json"), "real-grant-must-not-be-mounted")
	writeFile(t, filepath.Join(configDir, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"forged","expiresAt":9999999999999}}`)
	return b, authKey, configKey, authDir, configDir
}

func TestPrepareStoresRejectsProtectedIdentityBeforeReset(t *testing.T) {
	b, authKey, configKey, authDir, configDir := protectedAuthBackend(t)
	_, err := b.PrepareStores(context.Background(), backend.StorePrep{
		Config:         &backend.ConfigStorePrep{Key: configKey, Recreate: true},
		Auth:           &backend.StorePrepEntry{Key: authKey},
		ResetAuthFiles: []string{".credentials.json", "oauth-grant.json"},
	})
	if err == nil || !strings.Contains(err.Error(), "gateway-protected") {
		t.Fatalf("PrepareStores accepted protected identity: %v", err)
	}
	assertFileContent(t, filepath.Join(authDir, "oauth-grant.json"), "real-grant-must-not-be-mounted")
	if _, err := os.Stat(filepath.Join(configDir, ".credentials.json")); err != nil {
		t.Fatalf("changed config before protected identity rejection: %v", err)
	}
}

func TestPrepareRunRefusesPreviouslyConstructedProtectedStoreRequest(t *testing.T) {
	b, authKey, _, _, _ := protectedAuthBackend(t)
	calls := stubCLI(t, "docker", "exit 98\n")
	_, err := b.prepareRun(context.Background(), backend.Request{Stores: []backend.PersistentStore{{Kind: backend.StoreKindAuth, Key: authKey, ContainerPath: "/auth"}}})
	if err == nil || !strings.Contains(err.Error(), "gateway-protected") {
		t.Fatalf("prepareRun accepted protected identity: %v", err)
	}
	if got := stubCalls(t, calls); len(got) != 0 {
		t.Fatalf("contacted engine before rejecting protected store: %v", got)
	}
}

func TestRequestDrivenAuthSyncNeverMountsProtectedStore(t *testing.T) {
	b, authKey, configKey, authDir, _ := protectedAuthBackend(t)
	calls := stubCLI(t, "docker", "exit 98\n")
	b.runRequestAuthSync(backend.Request{
		Session: backend.SessionMeta{Tool: "claude"},
		Stores: []backend.PersistentStore{
			{Kind: backend.StoreKindConfig, Key: configKey},
			{Kind: backend.StoreKindAuth, Key: authKey},
		},
		AuthSync: &backend.AuthSyncSpec{AuthFiles: []string{".credentials.json"}},
	})
	if got := stubCalls(t, calls); len(got) != 0 {
		t.Fatalf("foreground finalizer launched a protected-store helper: %v", got)
	}
	assertFileContent(t, filepath.Join(authDir, "oauth-grant.json"), "real-grant-must-not-be-mounted")
}

func TestInspectedAuthFinalizationNeverMountsProtectedStore(t *testing.T) {
	b, _, _, authDir, configDir := protectedAuthBackend(t)
	info := dockercmd.InspectResponse{
		Config: &dockercmd.ContainerConfig{
			Image:  "test-image",
			Labels: map[string]string{model.LabelAgent: "claude"},
			Env:    []string{model.EnvAuthDir + "=/auth", model.EnvToolConfigDir + "=/config", model.EnvAuthFiles + "=.credentials.json"},
		},
		Mounts: []dockercmd.MountPoint{
			{Type: dockercmd.MountTypeBind, Source: authDir, Destination: "/auth"},
			{Type: dockercmd.MountTypeBind, Source: configDir, Destination: "/config"},
		},
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	inspectFile := filepath.Join(t.TempDir(), "inspect.json")
	if err := os.WriteFile(inspectFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROTECTED_TEST_INSPECT", inspectFile)
	calls := stubCLI(t, "docker", "cat \"$PROTECTED_TEST_INSPECT\"\n")
	err = b.finalizeManagedContainerAuth(context.Background(), "session")
	if err == nil || !strings.Contains(err.Error(), "gateway-protected") {
		t.Fatalf("finalizer accepted protected store: %v", err)
	}
	if got := stubCalls(t, calls); len(got) != 1 || !strings.HasPrefix(got[0], "container inspect ") {
		t.Fatalf("inspected finalizer launched protected-store helper: %v", got)
	}
	assertFileContent(t, filepath.Join(authDir, "oauth-grant.json"), "real-grant-must-not-be-mounted")
}

func TestLegacyAuthSyncIgnoresWorkloadPlantedIdentityMarkers(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})
	b.reconcileScript = "enclave_sync_shared_auth() { :; }\n"
	b.reconcileScriptErr = nil
	authKey := backend.StoreKey{Owner: "claude"}
	configKey := backend.StoreKey{Owner: "claude", ProjectHash: "project"}
	authDir := b.resolveStoreDir(authKey, backend.StoreKindAuth)
	configDir := b.resolveStoreDir(configKey, backend.StoreKindConfig)
	writeFile(t, filepath.Join(authDir, ".enclave-oauth-identity.json"), `{"schemaVersion":1,"protected":true}`)
	writeFile(t, filepath.Join(configDir, ".enclave-oauth-identity.json"), "corrupt workload marker")
	calls := stubCLI(t, "docker", "exit 0\n")
	if err := b.syncSharedAuthStores(context.Background(), "test-image", "claude", []string{".credentials.json"}, configDir, authDir); err != nil {
		t.Fatalf("workload-planted metadata blocked reconcile: %v", err)
	}
	if got := stubCalls(t, calls); len(got) == 0 {
		t.Fatal("legacy reconcile helper did not run")
	}
	if err := b.storage.Remove(context.Background(), authKey, backend.StoreKindAuth); err != nil {
		t.Fatalf("workload-planted metadata blocked legacy removal: %v", err)
	}
}

func TestAuthSyncNamesProtectedConfigSource(t *testing.T) {
	b, _, _, protectedDir, legacyDir := protectedAuthBackend(t)
	calls := stubCLI(t, "docker", "exit 98\n")
	err := b.syncSharedAuthStores(context.Background(), "test-image", "claude", []string{".credentials.json"}, protectedDir, legacyDir)
	if err == nil || !strings.Contains(err.Error(), "auth reconcile config store") || !strings.Contains(err.Error(), protectedDir) {
		t.Fatalf("missing protected config source context: %v", err)
	}
	if got := stubCalls(t, calls); len(got) != 0 {
		t.Fatalf("launched helper with protected config source: %v", got)
	}
}

func TestExecSelectsTargetSessionPinnedGitHubCLI(t *testing.T) {
	for _, tc := range []struct {
		name        string
		env         []string
		user        string
		inspectFail bool
		wantGitHub  bool
		wantError   bool
	}{
		{name: "ordinary session", env: []string{"HOME=/home/agent"}},
		{name: "ordinary admin", env: []string{"HOME=/home/agent"}, user: "root"},
		{name: "protected admin", env: []string{"ENCLAVE_PROTECTED_AUTH=1"}, user: "root", wantError: true},
		{name: "protected Claude only", env: []string{"ENCLAVE_PROTECTED_AUTH=1"}},
		{name: "protected GitHub", env: []string{"ENCLAVE_PROTECTED_AUTH=1", model.EnvProtectedGitHub + "=1"}, wantGitHub: true},
		{name: "uninspectable target", inspectFail: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := json.Marshal(dockercmd.InspectResponse{Name: "/session", Config: &dockercmd.ContainerConfig{Env: tc.env}})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("STUB_INSPECT", string(info))
			script := `[ "$1 $2" = "container inspect" ] && printf '%s\n' "$STUB_INSPECT"` + "\n"
			if tc.inspectFail {
				script = "echo 'Cannot connect to the Docker daemon' >&2\nexit 1\n"
			}
			stubCLI(t, "docker", script)
			var execArgv []string
			orig := execInteractive
			execInteractive = func(_ context.Context, _ string, argv []string, _ string, _ bool) error {
				execArgv = argv
				return nil
			}
			t.Cleanup(func() { execInteractive = orig })

			requested := []string{"gh", "auth", "status"}
			err = New(Options{}).Exec(context.Background(), backend.SessionRef{Name: "session"}, backend.ExecRequest{Argv: requested, User: tc.user, TTY: true}, backend.AttachIO{})
			if tc.wantError {
				if err == nil || execArgv != nil {
					t.Fatalf("exec ran without verified client selection: err=%v argv=%v", err, execArgv)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantGitHub {
				if strings.Join(execArgv, " ") != strings.Join(requested, " ") {
					t.Fatalf("exec argv changed without a GitHub pin: %v", execArgv)
				}
				return
			}
			if len(execArgv) != 4+len(requested) || execArgv[0] != "bash" || !strings.Contains(execArgv[2], "/opt/enclave/gh-2.87.3/bin") || strings.Join(execArgv[4:], " ") != strings.Join(requested, " ") {
				t.Fatalf("protected exec argv: %v", execArgv)
			}
		})
	}
}
