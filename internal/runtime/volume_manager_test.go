// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestConfigVolumeRelativeDirsIncludesSettingsAndAuthParents(t *testing.T) {
	t.Parallel()

	m := volumeManager{
		Runtime: &Runtime{
			containerHome: model.ContainerHome,
			profile: model.Profile{
				Name:           "pi",
				ConfigDir:      ".pi",
				SettingsFile:   "pi-settings.json",
				SettingsTarget: ".pi/agent/settings.json",
				Providers: []model.ProviderConfig{
					{Name: "openai-codex", AuthFiles: []string{"agent/auth.json"}},
				},
			},
		},
	}

	got := m.configVolumeRelativeDirs()
	want := []string{"agent"}
	if len(got) != len(want) {
		t.Fatalf("configVolumeRelativeDirs() len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("configVolumeRelativeDirs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConfigVolumeRelativeDirsSkipsRootLevelFiles(t *testing.T) {
	t.Parallel()

	m := volumeManager{
		Runtime: &Runtime{
			containerHome: model.ContainerHome,
			profile: model.Profile{
				Name:           "claude",
				ConfigDir:      ".claude",
				SettingsFile:   "claude-settings.json",
				SettingsTarget: ".claude/settings.json",
				Providers: []model.ProviderConfig{
					{Name: "anthropic", AuthFiles: []string{"config.json"}},
				},
			},
		},
	}

	if got := m.configVolumeRelativeDirs(); len(got) != 0 {
		t.Fatalf("configVolumeRelativeDirs() = %v, want no nested directories", got)
	}
}

func TestBuildPrepFeatureStateGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		persist   bool
		state     bool
		authScope string
		want      bool
	}{
		{name: "persistent shared auth", persist: true, state: true, authScope: model.AuthScopeShared, want: true},
		{name: "persistent project auth", persist: true, state: true, authScope: model.AuthScopeProject, want: true},
		{name: "ephemeral", persist: false, state: true, authScope: model.AuthScopeShared, want: false},
		{name: "not opted in", persist: true, state: false, authScope: model.AuthScopeShared, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runtime{
				profile:  model.Profile{Name: "claude", ConfigDir: ".claude"},
				project:  model.Project{Hash: "abc123def456"},
				run:      model.RunOptions{Persist: tt.persist},
				auth:     model.AuthOptions{AuthScope: tt.authScope},
				features: []model.Extension{{Name: "state-probe", FeatureState: tt.state}},
			}

			prep, stores := newVolumeManager(r).BuildPrep("session-specific")
			state, ok := stores.FeatureState["state-probe"]
			if ok != tt.want {
				t.Fatalf("feature state present = %v, want %v", ok, tt.want)
			}
			var prepState *backend.StorePrepEntry
			for i := range prep.FeatureStores {
				if prep.FeatureStores[i].Kind == backend.StoreKindFeatureState {
					prepState = &prep.FeatureStores[i]
				}
			}
			if (prepState != nil) != tt.want {
				t.Fatalf("feature-state prep present = %v, want %v", prepState != nil, tt.want)
			}
			if !tt.want {
				return
			}
			wantKey := backend.StoreKey{Owner: "state-probe", ProjectHash: "abc123def456"}
			if state.Kind != backend.StoreKindFeatureState || state.Key != wantKey {
				t.Fatalf("feature state = %+v, want kind=%q key=%+v", state, backend.StoreKindFeatureState, wantKey)
			}
			if prepState.Key != wantKey {
				t.Fatalf("feature-state prep key = %+v, want %+v", prepState.Key, wantKey)
			}
		})
	}
}

func TestBuildPrepFeatureStateKeyIsToolIndependent(t *testing.T) {
	t.Parallel()

	build := func(tool string) backend.StoreRef {
		r := &Runtime{
			profile:  model.Profile{Name: tool},
			project:  model.Project{Hash: "abc123def456"},
			run:      model.RunOptions{Persist: true},
			features: []model.Extension{{Name: "state-probe", FeatureState: true}},
		}
		_, stores := newVolumeManager(r).BuildPrep("")
		return stores.FeatureState["state-probe"]
	}

	claude := build("claude")
	codex := build("codex")
	if claude != codex {
		t.Fatalf("feature state differs by tool: claude=%+v codex=%+v", claude, codex)
	}
}

func TestBuildPrepAllowsFeatureAuthAndStateTogether(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
		project: model.Project{Hash: "abc123def456"},
		run:     model.RunOptions{Persist: true},
		auth:    model.AuthOptions{AuthScope: model.AuthScopeShared},
		features: []model.Extension{{
			Name:         "github-cli",
			ConfigDir:    ".config/gh",
			AuthFiles:    []string{"hosts.yml"},
			FeatureState: true,
		}},
	}

	prep, stores := newVolumeManager(r).BuildPrep("")
	if _, ok := stores.FeatureAuth["github-cli"]; !ok {
		t.Fatal("feature auth store missing")
	}
	if _, ok := stores.FeatureState["github-cli"]; !ok {
		t.Fatal("feature state store missing")
	}
	if len(prep.FeatureStores) != 2 {
		t.Fatalf("feature prep entries = %d, want auth and state", len(prep.FeatureStores))
	}
}
