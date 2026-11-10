// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "enclave/extensions/tools/claude/go"
	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	"enclave/internal/model"
)

func TestProtectedNativeClaudeAliasCannotRemainRaw(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := bearerTokenRuntime(t, "claude")
	r.run.ProtectAuth, r.run.Persist = true, true
	r.auth.AuthScope, r.containerHome = model.AuthScopeShared, model.ContainerHome
	_, stores := newVolumeManager(r).BuildPrep("")
	real := hoststore.Dir(r.host.Home, stores.Auth.Key, stores.Auth.Kind)
	config := hoststore.Dir(r.host.Home, stores.Config.Key, stores.Config.Kind)
	for _, dir := range []string{real, config} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Main's writeClaudeConfig writes these API-key fields to both config.json
	// and .claude.json. ResetAuthFiles currently omits the latter alias.
	writeFile(t, filepath.Join(config, ".claude.json"), `{"primaryApiKey":"synthetic-raw-native-alias"}`)
	mode := &backend.SessionMode{ConfigStore: stores.Config.Key, HolderID: "candidate"}
	if err := hoststore.ReserveMode(r.host.Home, config, hoststore.ModeHolder{ID: mode.HolderID, Session: "candidate", Protected: true, Engine: "docker"}, nil); err != nil {
		t.Fatal(err)
	}
	mounts := newMountAccumulator(nil, nil)
	r.addConfigMount(mounts, stores.Config)
	_, cleanup, err := r.prepareProtectedAuth(stores, mounts, &SecretMapping{}, mode)
	if cleanup != nil {
		defer func() { _ = cleanup() }()
	}
	if err == nil {
		t.Fatal("protected preparation accepted a raw credential alias in its workload config mount")
	}
}

func TestProtectedReadinessFollowsRawGateAndPreservesNativeDigest(t *testing.T) {
	for _, tc := range []struct {
		name, file, content string
		refuse              bool
	}{
		{"raw legacy config", "config.json", `{"primaryApiKey":"synthetic-markerless-raw"}`, true},
		{"raw global alias", ".claude.json", `{"anthropicApiKey":"synthetic-markerless-raw"}`, true},
		{"clean login", "", "", false},
		{"kept placeholder", "config.json", `{"primaryApiKey":"ENCLAVE_SECRET_idle_residue"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			r := bearerTokenRuntime(t, "claude")
			r.run.ProtectAuth, r.run.Persist = true, true
			r.auth.AuthScope, r.containerHome = model.AuthScopeShared, model.ContainerHome
			r.configVolReady, r.handler = true, noopRuntimeHandler{}
			actual := backenddocker.New(backenddocker.Options{Host: r.host})
			r.backend = &fakeBackend{storage: actual.Storage()}
			_, stores := newVolumeManager(r).BuildPrep("")
			real := hoststore.Dir(r.host.Home, stores.Auth.Key, stores.Auth.Kind)
			configDir := hoststore.Dir(r.host.Home, stores.Config.Key, stores.Config.Kind)
			for _, dir := range []string{real, configDir} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, filepath.Join(real, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"synthetic-ready-access","refreshToken":"synthetic-ready-refresh","expiresAt":4102444800000}}`)
			if tc.file != "" {
				writeFile(t, filepath.Join(configDir, tc.file), tc.content)
			}
			mode := &backend.SessionMode{ConfigStore: stores.Config.Key, HolderID: "ready-candidate"}
			if err := hoststore.ReserveMode(r.host.Home, configDir, hoststore.ModeHolder{ID: mode.HolderID, Session: "candidate", Protected: true, Engine: "docker"}, nil); err != nil {
				t.Fatal(err)
			}
			mounts := newMountAccumulator(nil, nil)
			prepared, err := r.prepareVolumes("candidate", "candidate", mounts, mode)
			if prepared.Cleanup != nil {
				defer prepared.Cleanup()
			}
			if (err != nil) != tc.refuse {
				t.Fatalf("prepare error=%v", err)
			}
			if tc.file != "" {
				data, e := os.ReadFile(filepath.Join(configDir, tc.file))
				if e != nil || string(data) != tc.content {
					t.Fatal("readiness changed a raw or classified native file")
				}
			}
			if tc.refuse {
				if !strings.Contains(err.Error(), tc.file) {
					t.Fatal(err)
				}
				if tc.file == "config.json" {
					if _, e := os.Lstat(filepath.Join(configDir, ".claude.json")); !os.IsNotExist(e) {
						t.Fatal("readiness ran before raw refusal")
					}
				}
				return
			}
			data, e := os.ReadFile(filepath.Join(configDir, ".claude.json"))
			if e != nil || !strings.Contains(string(data), `"hasCompletedOnboarding":true`) {
				t.Fatal("protected login lost onboarding marker")
			}
			if tc.file == "config.json" {
				digest := sha256.Sum256([]byte(tc.content))
				want := fmt.Sprintf("config.json:link:%x", digest)
				if !strings.Contains(strings.Join(mounts.Env(), "\n"), want) {
					t.Fatal("entrypoint digest no longer matches classified config")
				}
			}
		})
	}
}
