// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	"enclave/internal/model"
)

type endModeRuntimeBackend struct {
	admissionBackend
	home      string
	confirmed bool
	view      string
}

func (b *endModeRuntimeBackend) Run(_ context.Context, req backend.Request, _ backend.AttachIO) (backend.ExitStatus, error) {
	for _, mount := range req.Mounts {
		if mount.ContainerPath == model.ContainerHome+"/"+model.ContainerAuthDir {
			b.view = mount.Source
		}
	}
	if b.view == "" {
		return backend.ExitStatus{}, os.ErrNotExist
	}
	if err := os.WriteFile(filepath.Join(b.view, "config.json"), []byte(`{"oauthAccount":{"displayName":"client-final-name"}}`), 0600); err != nil {
		return backend.ExitStatus{}, err
	}
	hoststore.AdvanceRequestMode(b.home, req, "live", "cid")
	if b.confirmed {
		hoststore.AdvanceRequestMode(b.home, req, "finalizing", "cid")
	}
	return backend.ExitStatus{}, nil
}

func TestNativeSessionEndRequiresConfirmedExit(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown exit", true: "confirmed exit"}[confirmed], func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			r := bearerTokenRuntime(t, "claude")
			r.containerHome = model.ContainerHome
			writeFile(t, filepath.Join(r.host.Home, ".gitconfig"), "[user]\n name = Synthetic Test\n email = synthetic@example.invalid\n")
			r.project.Dir, r.project.RealDir = t.TempDir(), t.TempDir()
			r.run.ProtectAuth, r.run.Persist = true, true
			r.auth.AuthScope, r.handler = model.AuthScopeShared, noopRuntimeHandler{}
			actual := backenddocker.New(backenddocker.Options{Host: r.host})
			be := &endModeRuntimeBackend{home: r.host.Home, confirmed: confirmed}
			be.storage = actual.Storage()
			be.check = func(context.Context, backend.StoreKey, map[string]string) error { return nil }
			r.backend = be
			native := hoststore.Dir(r.host.Home, backend.StoreKey{Owner: r.profile.Name}, backend.StoreKindAuth)
			if err := os.MkdirAll(native, 0700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(native, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"end-access-synthetic","refreshToken":"end-refresh-synthetic","expiresAt":4102444800000}}`)
			configDir := hoststore.Dir(r.host.Home, backend.StoreKey{Owner: r.profile.Name, ProjectHash: r.project.Hash}, backend.StoreKindConfig)
			if err := os.MkdirAll(configDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := r.Execute(); err != nil {
				t.Fatal(err)
			}
			if be.view == "" {
				t.Fatal("backend never received own view")
			}
			_, err := os.Stat(be.view)
			if confirmed != os.IsNotExist(err) {
				t.Fatalf("view existence disagrees with exit proof: %v", err)
			}
			records, err := hoststore.ModeRecords(r.host.Home)
			if err != nil {
				t.Fatal(err)
			}
			if confirmed && len(records) != 0 || !confirmed && len(records) != 1 {
				t.Fatal("holder lifetime disagrees with view")
			}
			if !confirmed && !records[0].Holders[0].GatewayCorrelated {
				t.Fatal("creation correlation promise was not reserved before execution")
			}
			data, err := os.ReadFile(filepath.Join(native, "config.json"))
			if confirmed && (err != nil || string(data) != `{"oauthAccount":{"displayName":"client-final-name"}}`) {
				t.Fatal("confirmed session did not harvest metadata")
			}
			if !confirmed && !os.IsNotExist(err) {
				t.Fatal("unconfirmed session harvested metadata")
			}
		})
	}
}

func TestProtectedEphemeralIdentityUsesConfigSuffix(t *testing.T) {
	r := bearerTokenRuntime(t, "claude")
	r.run.ProtectAuth, r.run.Ephemeral = true, true
	r.setConfigVolumeSuffix("candidate", "candidate")
	_, stores := newVolumeManager(r).BuildPrep(r.configVolSuffix)
	configDir := hoststore.Dir(r.host.Home, stores.Config.Key, stores.Config.Kind)
	if r.configVolSuffix == "" || stores.Auth.Key.Suffix != "ephemeral-"+filepath.Base(configDir) {
		t.Fatal("cleanup would derive a different private identity than startup")
	}
}
