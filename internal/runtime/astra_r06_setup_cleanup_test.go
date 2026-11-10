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
	"strings"
	"testing"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/network"
	"enclave/internal/policy"
)

func TestAstraCentralViewCleanupAfterPostProjectionSetupFailure(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := bearerTokenRuntime(t, "claude")
	r.containerHome = model.ContainerHome
	writeFile(t, filepath.Join(r.host.Home, ".gitconfig"), "[user]\n name = Synthetic Test\n email = synthetic@example.invalid\n")
	r.project.Dir, r.project.RealDir = t.TempDir(), t.TempDir()
	r.run.ProtectAuth, r.run.Persist = true, true
	r.auth.AuthScope, r.handler = model.AuthScopeShared, noopRuntimeHandler{}
	actual := backenddocker.New(backenddocker.Options{Host: r.host})
	be := &endModeRuntimeBackend{home: r.host.Home}
	be.storage = actual.Storage()
	be.check = func(context.Context, backend.StoreKey, map[string]string) error { return nil }
	r.backend = be
	r.policyResolved = true
	r.policyResult = policy.ResolveResult{Effective: network.EffectivePolicy{Mode: model.NetworkModeUnrestricted}}
	native := hoststore.Dir(r.host.Home, backend.StoreKey{Owner: r.profile.Name}, backend.StoreKindAuth)
	configDir := hoststore.Dir(r.host.Home, backend.StoreKey{Owner: r.profile.Name, ProjectHash: r.project.Hash}, backend.StoreKindConfig)
	for _, dir := range []string{native, configDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	original := `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","expiresAt":4102444800000}}`
	file := filepath.Join(native, ".credentials.json")
	writeFile(t, file, original)
	if err := r.Execute(); err == nil || !strings.Contains(err.Error(), "requires restricted networking") {
		t.Fatalf("post-projection failure control: %v", err)
	}
	if be.view != "" {
		t.Fatal("backend ran despite setup failure")
	}
	views := filepath.Join(config.HostProjectToolDir(r.host.Home, r.project.Hash, r.profile.Name), "auth-views")
	if entries, err := os.ReadDir(views); err != nil || len(entries) != 0 {
		t.Fatalf("central cleanup did not remove the created view: entries=%v err=%v", entries, err)
	}
	if records, err := hoststore.ModeRecords(r.host.Home); err != nil || len(records) != 0 {
		t.Fatalf("setup holder survived central cleanup: %v %v", records, err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != original {
		t.Fatal("failed setup changed canonical credentials")
	}
}
