// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	_ "enclave/extensions/tools/claude/go"
	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	"enclave/internal/model"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUbuntuProtectedOnboardingPreservesGlobalMetadata(t *testing.T) {
	for _, protected := range []bool{false, true} {
		name := "ordinary-control"
		if protected {
			name = "protected"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			r := bearerTokenRuntime(t, "claude")
			r.run.ProtectAuth, r.run.Persist = protected, true
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
			writeFile(t, filepath.Join(configDir, "config.json"), `{"hasCompletedOnboarding":true}`)
			global := `{"projects":{"/synthetic/project":{"hasTrustDialogAccepted":true}},"customApiKeyResponses":{"approved":["synthetic-suffix"]}}`
			writeFile(t, filepath.Join(configDir, ".claude.json"), global)
			mode := &backend.SessionMode{ConfigStore: stores.Config.Key, HolderID: "metadata-candidate"}
			if err := hoststore.ReserveMode(r.host.Home, configDir, hoststore.ModeHolder{ID: mode.HolderID, Session: "candidate", Protected: protected, Engine: "docker"}, nil); err != nil {
				t.Fatal(err)
			}
			mounts := newMountAccumulator(nil, nil)
			prepared, err := r.prepareVolumes("candidate", "candidate", mounts, mode)
			if prepared.Cleanup != nil {
				defer prepared.Cleanup()
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(configDir, ".claude.json"))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if doc["projects"] == nil || doc["customApiKeyResponses"] == nil {
				t.Fatalf("onboarding erased existing global metadata; remaining keys=%v", doc)
			}
		})
	}
}
