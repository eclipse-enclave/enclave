// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func nativeAuthFixture(t *testing.T) (*Runtime, nativeAuthPlanInput, string, string, string) {
	t.Helper()
	home, configDir, authDir := entrypointAuthDirs(t)
	input := nativeAuthPlanInput{RealDir: t.TempDir(), ViewsDir: t.TempDir(), ConfigDir: configDir, LockPath: filepath.Join(t.TempDir(), "auth.lock"), Session: "fixture"}
	writeFile(t, input.LockPath, "")
	writeFile(t, filepath.Join(input.RealDir, claudeCredentialsFile), `{"claudeAiOauth":{"accessToken":"synthetic-native-oauth-access","refreshToken":"synthetic-native-oauth-refresh","expiresAt":300}}`)
	r := &Runtime{containerHome: home, auth: model.AuthOptions{AuthScope: model.AuthScopeShared}, profile: model.Profile{Name: "claude", ConfigDir: ".claude", Providers: []model.ProviderConfig{{Name: "anthropic", AuthFiles: []string{claudeCredentialsFile}, SecurestorageDirEnv: "CLAUDE_SECURESTORAGE_CONFIG_DIR", AuthSecrets: []model.AuthFileSecret{{File: claudeCredentialsFile, Codec: "claude-credentials", Fields: []model.AuthSecretField{{Pointer: "/claudeAiOauth/accessToken", Kind: "access", Release: "oauth"}, {Pointer: "/claudeAiOauth/refreshToken", Kind: "refresh"}}}}}}, Secrets: map[string]model.SecretConfig{"oauth": {Release: &model.SecretReleaseConfig{HTTP: &model.HTTPSecretReleaseConfig{Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer %s"}}}}}}
	return r, input, home, configDir, authDir
}

func planEnv(plan nativeAuthMountPlan, name string) string {
	for _, env := range plan.Env {
		if env.Name == name {
			return env.Value
		}
	}
	return ""
}

func TestNativeAuthMountPlanSeparatesRealStoreFromAgent(t *testing.T) {
	r, input, _, _, authDir := nativeAuthFixture(t)
	configured := []model.SecretReleaseEntry{{SecretID: "other", Placeholder: "ENCLAVE_SECRET_existing-env", Value: "synthetic-env-value"}}
	plan, err := r.prepareNativeAuthPlan(input, configured)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Agent.Source == input.RealDir || plan.Agent.ContainerPath != authDir || plan.Agent.ReadOnly || plan.Agent.Type != backend.MountTypeBind {
		t.Fatalf("agent mount = %+v", plan.Agent)
	}
	if len(plan.Gateway) != 3 || plan.Gateway[0].Source != input.RealDir || plan.Gateway[0].ReadOnly || plan.Gateway[1].Source != input.LockPath || plan.Gateway[1].ReadOnly || plan.Gateway[2].Source != plan.Agent.Source || !plan.Gateway[2].ReadOnly {
		t.Fatalf("gateway mounts = %+v", plan.Gateway)
	}
	if planEnv(plan, model.EnvAuthDir) != authDir || planEnv(plan, "CLAUDE_SECURESTORAGE_CONFIG_DIR") != authDir || planEnv(plan, model.EnvNativeAuthOps) != claudeCredentialsFile+":create:" || len(plan.Releases) != 2 {
		t.Fatal("native auth mount environment or append-only release mapping incorrect")
	}
	ordinary := newMountAccumulator(nil, nil)
	r.addAuthMount(ordinary, &backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "claude"}})
	if len(ordinary.Mounts()) != 0 || len(ordinary.Stores()) != 1 || ordinary.Stores()[0].ContainerPath != plan.Agent.ContainerPath {
		t.Fatal("ordinary auth mount seam changed")
	}
}

func nativeEntrypoint(t *testing.T, home, configDir, authDir, ops string) ([]byte, error) {
	t.Helper()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "entrypoint.sh"), "true")
	cmd.Env = []string{"PATH=" + fakeJQDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home, "PROJECT_DIR=" + project, "TOOL=claude", "ENCLAVE_TOOLS_DIR=" + filepath.Join("..", "..", "extensions", "tools"), model.EnvToolConfigDir + "=" + configDir, model.EnvAuthDir + "=" + authDir, model.EnvAuthFiles + "=" + claudeCredentialsFile, model.EnvNativeAuthOps + "=" + ops, "ENCLAVE_AUTH_RECONCILE_LIB=" + filepath.Join("..", "..", "runtime-assets", "auth-reconcile.sh")}
	return cmd.CombinedOutput()
}

func TestNativeAuthPlanExecutesClassifiedEntrypointVerbs(t *testing.T) {
	for _, kind := range []string{"create", "keep link", "keep regular", "link idle", "changed regular", "raced create", "foreign link", "unknown verb"} {
		t.Run(kind, func(t *testing.T) {
			r, input, home, configDir, authDir := nativeAuthFixture(t)
			first, err := r.prepareNativeAuthPlan(input, nil)
			if err != nil {
				t.Fatal(err)
			}
			sanitized, err := os.ReadFile(filepath.Join(first.Agent.Source, claudeCredentialsFile))
			if err != nil {
				t.Fatal(err)
			}
			// Emulate the agent's view bind with only the sanitized synthetic fixture.
			writeFile(t, filepath.Join(authDir, claudeCredentialsFile), string(sanitized))
			native := filepath.Join(configDir, claudeCredentialsFile)
			switch kind {
			case "keep link", "foreign link":
				if err := os.Symlink(filepath.Join(authDir, claudeCredentialsFile), native); err != nil {
					t.Fatal(err)
				}
			case "keep regular", "changed regular", "link idle":
				writeFile(t, native, string(sanitized))
			}
			input.Session = "next"
			input.Idle = kind == "link idle"
			plan, err := r.prepareNativeAuthPlan(input, nil)
			if err != nil {
				t.Fatal(err)
			}
			ops := planEnv(plan, model.EnvNativeAuthOps)
			switch kind {
			case "changed regular", "raced create":
				writeFile(t, native, `{"claudeAiOauth":{"accessToken":"raw-after-classification"}}`)
			case "foreign link":
				if err := os.Remove(native); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/foreign", native); err != nil {
					t.Fatal(err)
				}
			case "unknown verb":
				ops = claudeCredentialsFile + ":unexpected:"
			}
			out, err := nativeEntrypoint(t, home, configDir, authDir, ops)
			refuse := kind == "changed regular" || kind == "raced create" || kind == "foreign link" || kind == "unknown verb"
			if refuse {
				if err == nil {
					t.Fatal("entrypoint accepted changed or unclassified native path")
				}
				return
			}
			if err != nil {
				t.Fatalf("entrypoint: %v\n%s", err, out)
			}
			if kind == "keep regular" {
				got, err := os.ReadFile(native)
				if err != nil || !bytes.Equal(got, sanitized) {
					t.Fatal("entrypoint replaced sibling's regular placeholder file")
				}
				info, err := os.Lstat(native)
				if err != nil || !info.Mode().IsRegular() {
					t.Fatal("keep regular was relinked")
				}
			} else {
				assertSymlinkTarget(t, native, filepath.Join(authDir, claudeCredentialsFile))
			}
			real, err := os.ReadFile(filepath.Join(input.RealDir, claudeCredentialsFile))
			if err != nil || strings.Contains(string(real), model.SecretPlaceholderMarker) {
				t.Fatal("entrypoint wrote placeholder into real store")
			}
		})
	}
}
