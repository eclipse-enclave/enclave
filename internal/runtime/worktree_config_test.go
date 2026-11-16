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
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/network"
)

func TestWorktreeInheritancePreservesPersistenceLayout(t *testing.T) {
	for _, local := range []bool{false, true} {
		home := t.TempDir()
		source := "main"
		if local {
			source = "sibling"
		}
		r := newTemplateOverrideRuntime(home, model.Profile{Name: "tool", ConfigDir: ".tool", MemoryDir: ".tool/memory", MemoryScope: model.MemoryScopeProject, Providers: []model.ProviderConfig{{Name: "provider", AuthFiles: []string{"auth.json"}}}})
		r.project = model.Project{Hash: "sibling", ConfigHash: source}
		r.run.Persist, r.run.WorktreeInheritance = true, !local
		r.run.SessionName = "review"
		r.auth.AuthScope = model.AuthScopeProject
		r.setConfigVolumeSuffix("enclave-tool-sibling-review", "enclave-tool-sibling")
		prep, stores := newVolumeManager(r).BuildPrep(r.currentConfigVolumeSuffix("enclave-tool-sibling-review", "enclave-tool-sibling"))
		root := config.HostProjectToolDir(home, "sibling", "tool")
		if got := hoststore.Dir(home, stores.Config.Key, stores.Config.Kind); got != filepath.Join(root, "config-store", "review") {
			t.Fatalf("config store moved: %s", got)
		}
		if prep.Auth != nil || stores.AuthStorage() != stores.Config {
			t.Fatal("project auth no longer belongs to the checkout config store")
		}
		if got := hoststore.Dir(home, stores.Env.Key, stores.Env.Kind); got != filepath.Join(root, "env") {
			t.Fatalf("env store moved: %s", got)
		}
		acc := newMountAccumulator(nil, nil)
		if err := r.addCacheMounts(acc); err != nil {
			t.Fatal(err)
		}
		r.addHistoryMounts(acc)
		r.addToolConfigMounts(acc)
		r.addMemoryMounts(acc)
		for target, want := range map[string]string{
			"/home/agent/.npm":                         filepath.Join(config.HostCacheToolProjectDir(home, "tool", "sibling"), "npm"),
			"/home/agent/" + model.ContainerHistoryDir: filepath.Join(root, "history"),
			"/home/agent/.npmrc":                       filepath.Join(root, model.HomeConfigDirName, "npmrc"),
			"/home/agent/.tool/memory":                 config.HostProjectMemoryDir(home, source, "tool"),
		} {
			if got, ok := lookupMountSource(acc.Mounts(), target); !ok || got != want {
				t.Fatalf("local=%v mount %s = %s, want %s", local, target, got, want)
			}
		}
	}
}

func TestWorktreeSourcesSharedGeneratedStateIsLocal(t *testing.T) {
	home := t.TempDir()
	var generated []string
	for _, hash := range []string{"sibling-a", "sibling-b"} {
		r := newTemplateOverrideRuntime(home, model.Profile{Name: "tool", ConfigDir: ".tool", SkillsDir: ".tool/skills"})
		r.project = model.Project{Hash: hash, ConfigHash: "main", RealDir: "/" + hash, ConfigDir: "/main"}
		writeRuntimeTestFile(t, filepath.Join(config.HostToolConfigDir(home, "tool"), "settings.json"), `{"base":true}`)
		writeRuntimeTestFile(t, filepath.Join(config.HostProjectToolPatchesDir(home, "main", "tool"), "settings.json"), `{"inherited":true}`)
		writeRuntimeTestFile(t, filepath.Join(config.HostProjectConfigDir(home, "main", "tool"), "other.txt"), "inherited")
		writeRuntimeTestFile(t, filepath.Join(config.HostProjectConfigDir(home, hash, "tool"), "other.txt"), "ignored")
		writeRuntimeTestFile(t, filepath.Join(config.HostProjectSkillsDir(home, "main"), "example", "SKILL.md"), "---\nname: example\ndescription: Example skill\n---\nInherited skill\n")
		if err := r.prepareToolConfigSource(); err != nil {
			t.Fatal(err)
		}
		generated = append(generated, r.configSourceDir)
		if !strings.HasPrefix(r.configSourceDir, config.HostProjectGeneratedConfigDir(home, hash, "tool")) {
			t.Fatalf("shared generated output: %s", r.configSourceDir)
		}
		for name, contains := range map[string]string{"settings.json": "\"inherited\": true", "other.txt": "inherited", "skills/example/SKILL.md": "Inherited skill"} {
			data, err := os.ReadFile(filepath.Join(r.configSourceDir, name))
			if err != nil || !strings.Contains(string(data), contains) {
				t.Fatalf("%s: %s %v", name, data, err)
			}
		}
		if got := newVolumeManager(r).envStoreKey(); got.ProjectHash != hash {
			t.Fatalf("shared env store: %+v", got)
		}
	}
	if generated[0] == generated[1] {
		t.Fatal("siblings share generated state")
	}
}

func TestWorktreeSecretsReadConfigIdentityNotEnvStoreIdentity(t *testing.T) {
	r := runtimeWithProfile(t, model.Profile{Name: "tool", Secrets: map[string]model.SecretConfig{"key": secretConfig([]string{"API_KEY"}, nil)}})
	r.project = model.Project{Hash: "sibling", ConfigHash: "main"}
	writeRuntimeTestFile(t, config.HostSecretsProjectFile(r.host.Home, "main", "tool"), "API_KEY=inherited-secret\n")
	writeRuntimeTestFile(t, config.HostSecretsProjectFile(r.host.Home, "sibling", "tool"), "API_KEY=ignored-secret\n")
	manager := newAuthManager(r)
	layers, err := manager.projectSecretsLayers()
	if err != nil {
		t.Fatal(err)
	}
	env := []string{}
	_, err = manager.injectDeclaredSecrets(stubHooks{}, authContextForRuntime(r), &env, map[string]string{}, layers, nil, mustActiveSecrets(t, r))
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(env, "API_KEY"); got != "inherited-secret" {
		t.Fatalf("API_KEY = %s", got)
	}
	if key := manager.envStoreKey(); key.ProjectHash != "sibling" {
		t.Fatalf("shared env store: %+v", key)
	}
}

func TestWorktreeNetworkSourcesUseConfigIdentity(t *testing.T) {
	r := newTemplateOverrideRuntime(t.TempDir(), model.Profile{Name: "tool"})
	r.paths.AllowlistsDir = t.TempDir()
	r.project = model.Project{Dir: "/sibling", Hash: "sibling", ConfigHash: "main"}
	for hash, domain := range map[string]string{"main": "main.example.com", "sibling": "ignored.example.com"} {
		if err := network.SavePolicy(config.HostProjectNetworkPolicyPath(r.host.Home, hash), network.Policy{Domains: network.PolicyDomains{Global: []string{domain}}}); err != nil {
			t.Fatal(err)
		}
	}
	allowlist := filepath.Join(config.HostProjectGatewayAllowlistsDir(r.host.Home, "main"), "tool.conf")
	writeRuntimeTestFile(t, allowlist, "allowlist.example.com\n")
	resolved, err := r.resolveEffectivePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AllowlistPath != allowlist || !slices.Equal(resolved.ProjectPolicy.Domains.Global, []string{"main.example.com"}) {
		t.Fatalf("wrong sources: %+v", resolved)
	}
}

func TestWorktreeMemoryScope(t *testing.T) {
	for _, scope := range []string{model.MemoryScopeProject, model.MemoryScopeSession} {
		t.Run(scope, func(t *testing.T) {
			r := newTemplateOverrideRuntime(t.TempDir(), model.Profile{Name: "tool", ConfigDir: ".tool", MemoryDir: ".tool/memory", MemoryScope: scope})
			r.project = model.Project{Hash: "sibling", ConfigHash: "main"}
			acc := newMountAccumulator(nil, nil)
			r.addMemoryMounts(acc)
			source, ok := lookupMountSource(acc.Mounts(), "/home/agent/.tool/memory")
			want := config.HostProjectMemoryDir(r.host.Home, "main", "tool")
			if scope == model.MemoryScopeSession {
				want = config.HostProjectMemorySessionDir(r.host.Home, "sibling", "tool", "default")
			}
			if !ok || source != want {
				t.Fatalf("memory = %s, want %s", source, want)
			}
		})
	}
}
