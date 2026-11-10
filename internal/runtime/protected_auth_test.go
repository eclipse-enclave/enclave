// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/auth"
	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/gateway/exchange"
	"enclave/internal/model"
	"enclave/internal/network"
	"enclave/internal/policy"
	"enclave/internal/util"
)

func TestProtectedAuthRefusesUnsupportedOptions(t *testing.T) {
	r := bearerTokenRuntime(t, "claude")
	r.run.ProtectAuth = true
	for _, tc := range []struct {
		name   string
		change func(*model.RunOptions, *model.AuthOptions, *model.BuildOptions, *model.Profile)
	}{
		// D29: the gid exemption depends on a non-admin, no-new-privileges agent.
		{"admin-D29", func(r *model.RunOptions, _ *model.AuthOptions, _ *model.BuildOptions, _ *model.Profile) {
			r.Admin = true
		}},
		{"network", func(r *model.RunOptions, _ *model.AuthOptions, _ *model.BuildOptions, _ *model.Profile) {
			r.AllowAllNetwork = true
		}},
		{"qemu", func(r *model.RunOptions, _ *model.AuthOptions, _ *model.BuildOptions, _ *model.Profile) {
			r.Backend = "qemu"
		}},
		{"project", func(_ *model.RunOptions, a *model.AuthOptions, _ *model.BuildOptions, _ *model.Profile) {
			a.AuthScope = model.AuthScopeProject
		}},
		{"devcontainer", func(_ *model.RunOptions, _ *model.AuthOptions, b *model.BuildOptions, _ *model.Profile) {
			b.Devcontainer = true
		}},
		{"base-image", func(_ *model.RunOptions, _ *model.AuthOptions, b *model.BuildOptions, _ *model.Profile) {
			b.BaseImage = "custom"
		}},
		{"other-tool", func(_ *model.RunOptions, _ *model.AuthOptions, _ *model.BuildOptions, p *model.Profile) {
			p.Name = "codex"
		}},
		{"no-declaration", func(_ *model.RunOptions, _ *model.AuthOptions, _ *model.BuildOptions, p *model.Profile) {
			p.Providers = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, options, build, profile := r.run, r.auth, r.build, r.profile
			tc.change(&run, &options, &build, &profile)
			if err := ValidateProtectedAuth(run, options, build, profile); err == nil {
				t.Fatal("unsupported protected combination accepted")
			}
			run.ProtectAuth = false
			if err := ValidateProtectedAuth(run, options, build, profile); err != nil {
				t.Fatalf("ordinary behavior changed: %v", err)
			}
		})
	}
	if err := ValidateProtectedAuth(r.run, r.auth, r.build, r.profile); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedViewMountAndDynamicReleaseContract(t *testing.T) {
	r := bearerTokenRuntime(t, "claude")
	r.run.ProtectAuth, r.run.Persist = true, true
	r.auth.AuthScope, r.containerHome = model.AuthScopeShared, model.ContainerHome
	r.features = []model.Extension{{Name: "github-cli", ConfigDir: ".config/gh", AuthFiles: []string{"hosts.yml"}}}
	vm := newVolumeManager(r)
	prep, stores := vm.BuildPrep("")
	if prep.Auth == nil || len(prep.Features) != 0 || vm.authSyncSpec(stores) != nil {
		t.Fatal("protected store intent retained ordinary auth mounts/sync")
	}
	realDir, err := hoststore.ResolveDir(r.host.Home, stores.Auth.Key, stores.Auth.Kind)
	if err != nil {
		t.Fatal(err)
	}
	configDir, err := hoststore.ResolveDir(r.host.Home, stores.Config.Key, stores.Config.Kind)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{realDir, configDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(realDir, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"native-access-synthetic","refreshToken":"native-refresh-synthetic","expiresAt":4102444800000}}`)
	writeFile(t, filepath.Join(realDir, "config.json"), `{"primaryApiKey":"native-api-synthetic"}`)
	var firstAlias string
	for _, id := range []string{"first", "second"} {
		mode := &backend.SessionMode{ConfigStore: stores.Config.Key, HolderID: id}
		holder := hoststore.ModeHolder{ID: id, Session: id, Engine: "docker", Protected: true}
		if err := hoststore.ReserveMode(r.host.Home, configDir, holder, nil); err != nil {
			t.Fatal(err)
		}
		placeholder, err := model.NewSecretPlaceholder(nil)
		if err != nil {
			t.Fatal(err)
		}
		mapping := SecretMapping{Entries: []model.SecretReleaseEntry{{SecretID: "anthropic-api-key", Placeholder: placeholder,
			Value: "configured-api-synthetic-" + id, Hosts: []string{"api.anthropic.com"}, Header: "x-api-key"}}}
		mounts := newMountAccumulator(nil, nil)
		r.addConfigMount(mounts, stores.Config)
		native, cleanup, err := r.prepareProtectedAuth(stores, mounts, &mapping, mode)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		if len(mounts.mounts) != 1 || mounts.mounts[0].Source == realDir || len(mounts.stores) != 1 || mounts.stores[0].Kind != backend.StoreKindConfig {
			t.Fatalf("canonical credentials reached workload mounts: %+v %+v", mounts.mounts, mounts.stores)
		}
		for _, file := range []string{".credentials.json", "config.json"} {
			data, err := os.ReadFile(filepath.Join(mounts.mounts[0].Source, file))
			if err != nil || bytes.Contains(data, []byte("native-")) || !bytes.Contains(data, []byte(model.SecretPlaceholderMarker)) {
				t.Fatalf("unsafe view %s: %v", file, err)
			}
		}
		if bytes.Contains(native.Config, []byte("native-access-synthetic")) {
			t.Fatal("native snapshot entered gateway declaration")
		}
		if id == "first" {
			firstAlias = placeholder
			if len(mapping.Entries) != 1 {
				t.Fatal("initial API key alias duplicated the env rule")
			}
		} else if len(mapping.Entries) != 2 || mapping.Entries[1].Placeholder != firstAlias || mapping.Entries[1].Value != mapping.Entries[0].Value {
			t.Fatal("stable native API alias did not track the current env release")
		}
		for _, entry := range mapping.Entries {
			if entry.SecretID != "anthropic-api-key" {
				t.Fatal("native OAuth snapshot entered static release rules")
			}
		}
		req := r.backendRequest(&ExecutionContext{NativeAuth: native, Mode: mode, Stores: mounts.Stores(), Mounts: mounts.Mounts(), Env: mounts.Env()}, false, false)
		if req.NativeAuth != native || req.NativeAuth.Store.Kind != backend.StoreKindAuth || req.Mode != mode {
			t.Fatal("backend lost the native-store intent")
		}
		// Startup Load takes the canonical lock before it can attest readiness.
		// Completing here proves projection released that lock before startup.
		file := filepath.Join(t.TempDir(), "native.json")
		if err := os.WriteFile(file, native.Config, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _, err = exchange.Load(ctx, file, realDir, hoststore.LockPath(r.host.Home, realDir), id, mapping.Entries)
		cancel()
		if err != nil {
			t.Fatalf("gateway Load/readiness lock: %v", err)
		}
		if envValue(mounts.env, model.EnvNativeAuthOps) == "" || !strings.Contains(envValue(mounts.env, model.EnvAuthDir), model.ContainerAuthDir) {
			t.Fatal("entrypoint classification/view metadata missing")
		}
	}
	r.run.Ephemeral, r.run.Persist = true, false
	_, ephemeral := newVolumeManager(r).BuildPrep("unique")
	if ephemeral.Auth == nil || ephemeral.Auth.Key.Suffix != "ephemeral-unique" || ephemeral.Env != nil {
		t.Fatal("protected ephemeral identity reused shared credentials/env")
	}
}

type nativeHookCounter struct{ calls int }

func (h *nativeHookCounter) OnAuthReady(auth.Context) (bool, error) { h.calls++; return false, nil }
func (h *nativeHookCounter) AfterEnvInjected(auth.Context, map[string]string) error {
	h.calls++
	return nil
}
func (h *nativeHookCounter) FinalizeAuth(auth.Context, model.AuthState) error { h.calls++; return nil }

func TestProtectedPrepareSkipsNativeAuthHooks(t *testing.T) {
	t.Setenv("ENCLAVE_TEST_NATIVE_HOOK", "synthetic-hook-only")
	r := runtimeWithProfile(t, model.Profile{Name: "native-hook-fixture", Secrets: map[string]model.SecretConfig{
		"fixture": secretConfig([]string{"ENCLAVE_TEST_NATIVE_HOOK"}, nil),
	}})
	hooks := &nativeHookCounter{}
	auth.RegisterHooks(r.profile.Name, hooks)
	if _, _, err := newAuthManager(r).Prepare(&[]string{}, storeSet{}); err != nil || hooks.calls != 3 {
		t.Fatalf("ordinary hook control: calls=%d err=%v", hooks.calls, err)
	}
	r.run.ProtectAuth = true
	if _, _, err := newAuthManager(r).Prepare(&[]string{}, storeSet{}); err != nil || hooks.calls != 3 {
		t.Fatalf("protected hooks invoked: calls=%d err=%v", hooks.calls, err)
	}
}

type admissionBackend struct {
	fakeBackend
	check func(context.Context, backend.StoreKey, map[string]string) error
}

func (b admissionBackend) CheckProtectedAuthAdmission(ctx context.Context, key backend.StoreKey, approved map[string]string) error {
	return b.check(ctx, key, approved)
}

func TestProtectedRuntimeAdmissionHoldsActualConfigStoreLock(t *testing.T) {
	r := bearerTokenRuntime(t, "claude")
	r.run.ProtectAuth, r.configVolSuffix = true, "named"
	_, stores := newVolumeManager(r).BuildPrep("named")
	dir, err := hoststore.ResolveDir(r.host.Home, stores.Config.Key, backend.StoreKindConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := hoststore.ReserveMode(r.host.Home, dir, hoststore.ModeHolder{ID: "sibling", Session: "sibling", Engine: "docker", Protected: true}, nil); err != nil {
		t.Fatal(err)
	}
	// A created sibling may still be pending before its live publication.
	called := false
	r.backend = &admissionBackend{check: func(_ context.Context, key backend.StoreKey, approved map[string]string) error {
		called = true
		if key != stores.Config.Key || approved["sibling"] != "docker" {
			t.Fatal("admission checked a different store or lost sibling identity")
		}
		release, acquired, err := util.TryAcquireFileLock(hoststore.LockPath(r.host.Home, dir))
		if release != nil {
			release()
		}
		if err != nil || acquired {
			t.Fatal("admission was not serialized by the config-store lock")
		}
		return nil
	}}
	mode, err := r.reserveSessionMode("candidate")
	if err != nil || !called {
		t.Fatalf("admission called=%t err=%v", called, err)
	}
	r.endSessionMode(mode, true)
	r.run.ProtectAuth = false
	if _, err := r.reserveSessionMode("ordinary"); err == nil {
		t.Fatal("ordinary runtime bypassed its protected sibling reservation")
	}
}

func TestProtectedAuthRefusesEffectivelyUnrestrictedPolicy(t *testing.T) {
	r := bearerTokenRuntime(t, "claude")
	r.run.ProtectAuth, r.policyResolved = true, true
	r.handler = noopRuntimeHandler{}
	r.policyResult = policy.ResolveResult{Effective: network.EffectivePolicy{Mode: model.NetworkModeUnrestricted}}
	if _, err := newNetworkManager(r).Prepare("session", model.AuthState{}, SecretMapping{}); err == nil {
		t.Fatal("effective unrestricted policy bypassed protection without --allow-all-network")
	}
}
