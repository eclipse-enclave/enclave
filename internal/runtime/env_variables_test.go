// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

// TestBaseMountsIncludesSpecEnvVariables covers environment.variables from a
// spec.yaml extension being injected into the container's base env, while
// invalid keys and reserved/protected keys are skipped rather than
// overriding the runtime's own env.
func TestBaseMountsIncludesSpecEnvVariables(t *testing.T) {
	r := &Runtime{
		project: model.Project{Dir: "/tmp/project"},
		profile: model.Profile{
			Name: "demo",
			EnvVariables: map[string]string{
				"LOG_LEVEL":           "info",
				"1BAD":                "skip-invalid-key",
				"PROJECT_DIR":         "skip-reserved",
				"TOOL":                "skip-reserved",
				model.EnvPrefix + "X": "skip-prefixed",
			},
		},
	}

	_, env := r.baseMounts()

	if !envSliceContainsKV(env, "LOG_LEVEL", "info") {
		t.Fatalf("expected LOG_LEVEL=info in base env, got %v", env)
	}
	if envSliceHasKey(env, "1BAD") {
		t.Fatalf("did not expect invalid key 1BAD in base env, got %v", env)
	}
	if !envSliceContainsKV(env, "PROJECT_DIR", r.projectDir) {
		t.Fatalf("expected PROJECT_DIR to keep its runtime value, got %v", env)
	}
	if !envSliceContainsKV(env, "TOOL", "demo") {
		t.Fatalf("expected TOOL to keep its runtime value, got %v", env)
	}
	if envSliceHasKey(env, model.EnvPrefix+"X") {
		t.Fatalf("did not expect %s-prefixed key from environment.variables, got %v", model.EnvPrefix, env)
	}
}

// TestBaseMountsIncludesMixinEnvVariables covers a mixin's
// environment.variables reaching the container env alongside the tool spec's,
// with the tool spec winning on key conflicts.
func TestBaseMountsIncludesMixinEnvVariables(t *testing.T) {
	r := &Runtime{
		project: model.Project{Dir: "/tmp/project"},
		profile: model.Profile{
			Name:         "demo",
			EnvVariables: map[string]string{"SHARED": "from-tool"},
		},
		features: []model.Extension{
			{
				Name:         "svc-mixin",
				Type:         model.ExtensionKindMixin,
				EnvVariables: map[string]string{"MIXIN_VAR": "from-mixin", "SHARED": "from-mixin"},
			},
		},
	}

	_, env := r.baseMounts()

	if !envSliceContainsKV(env, "MIXIN_VAR", "from-mixin") {
		t.Fatalf("expected mixin environment.variables in base env, got %v", env)
	}
	if !envSliceContainsKV(env, "SHARED", "from-tool") {
		t.Fatalf("expected tool spec to win env conflicts, got %v", env)
	}
}

func TestAddCacheMountsConfiguresPnpmStore(t *testing.T) {
	r := &Runtime{
		host:          model.Host{Home: t.TempDir()},
		project:       model.Project{Hash: "project-hash"},
		profile:       model.Profile{Name: "demo"},
		containerHome: "/home/agent",
	}

	mounts := newMountAccumulator(nil, nil)
	if err := r.addCacheMounts(mounts); err != nil {
		t.Fatalf("addCacheMounts: %v", err)
	}

	want := "/home/agent/.local/share/pnpm/store"
	if !envSliceContainsKV(mounts.Env(), "PNPM_CONFIG_STORE_DIR", want) {
		t.Fatalf("expected PNPM_CONFIG_STORE_DIR=%s, got %v", want, mounts.Env())
	}
}

func TestAddCacheMountsUsesEphemeralPnpmStoreWithoutCache(t *testing.T) {
	r := &Runtime{
		run:           model.RunOptions{NoCache: true},
		containerHome: "/home/agent",
	}

	mounts := newMountAccumulator(nil, nil)
	if err := r.addCacheMounts(mounts); err != nil {
		t.Fatalf("addCacheMounts: %v", err)
	}

	want := "/home/agent/.local/share/pnpm/store"
	if !envSliceContainsKV(mounts.Env(), "PNPM_CONFIG_STORE_DIR", want) {
		t.Fatalf("expected PNPM_CONFIG_STORE_DIR=%s, got %v", want, mounts.Env())
	}
	if len(mounts.Mounts()) != 0 {
		t.Fatalf("did not expect cache mounts with cache disabled, got %v", mounts.Mounts())
	}
}

// TestSpecProxyManagedUnionsMixins covers proxyManaged aliases declared by a
// mixin surviving into placeholder selection.
func TestSpecProxyManagedUnionsMixins(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{Name: "demo", ProxyManaged: []string{"TOOL_ALIAS"}},
		features: []model.Extension{
			{Name: "svc-mixin", Type: model.ExtensionKindMixin, ProxyManaged: []string{"MIXIN_ALIAS"}},
		},
	}
	m := newAuthManager(r)
	managed := m.proxyManagedEnvVars(activeSecret{EnvVars: []string{"MIXIN_ALIAS", "OTHER_ALIAS"}})
	if !managed["MIXIN_ALIAS"] {
		t.Fatalf("expected mixin proxyManaged alias to be placeholder-managed, got %v", managed)
	}
	if managed["OTHER_ALIAS"] {
		t.Fatalf("expected unlisted alias to receive the raw value, got %v", managed)
	}
}

// TestSpecNetworkDomainsUnionsMixins covers a mixin's allowed/denied domains
// entering the session's spec-domain union.
func TestSpecNetworkDomainsUnionsMixins(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{
			Name:           "demo",
			AllowedDomains: []string{"tool.example.org"},
		},
		features: []model.Extension{
			{
				Name:           "svc-mixin",
				Type:           model.ExtensionKindMixin,
				AllowedDomains: []string{"svc.example.org"},
				DeniedDomains:  []string{"telemetry.example.org"},
			},
		},
	}
	allowed, denied := r.sessionNetworkInputs().SpecDomains()
	if !containsString(allowed, "tool.example.org") || !containsString(allowed, "svc.example.org") {
		t.Fatalf("expected union of tool+mixin allowed domains, got %v", allowed)
	}
	if !containsString(denied, "telemetry.example.org") {
		t.Fatalf("expected mixin denied domains in union, got %v", denied)
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestProjectEnvCannotOverrideInternalEnvironment(t *testing.T) {
	for _, key := range []string{model.EnvPrefix + "RUNTIME_UID_REMAP_DONE", model.EnvNativeAuthOps} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=project-value\nVISIBLE=kept\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			r := &Runtime{project: model.Project{Dir: dir}}
			values, loaded := r.envFileValues()
			if !loaded || len(values) != 1 || values[0] != "VISIBLE=kept" {
				t.Fatalf("project environment overrides reserved %s: %v (loaded=%v)", key, values, loaded)
			}
			output, err := os.CreateTemp(t.TempDir(), "env-summary")
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Stderr
			os.Stderr = output
			r.logEnvFileLoaded(nil)
			os.Stderr = previous
			if info, err := output.Stat(); err != nil || info.Size() != 0 {
				t.Fatal("empty filtered environment produced a summary")
			}
			os.Stderr = output
			r.logEnvFileLoaded(values)
			os.Stderr = previous
			_ = output.Close()
			data, err := os.ReadFile(output.Name())
			if err != nil || !strings.Contains(string(data), "keys: VISIBLE") || strings.Contains(string(data), key) || strings.Contains(string(data), "kept") {
				t.Fatalf("summary must list only loaded keys, not ignored keys or values: %q (%v)", data, err)
			}
		})
	}
}
