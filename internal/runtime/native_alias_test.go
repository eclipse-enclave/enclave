// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/model"
	"os"
	"path/filepath"
	"testing"
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
		defer cleanup()
	}
	if err == nil {
		t.Fatal("protected preparation accepted a raw credential alias in its workload config mount")
	}
}
