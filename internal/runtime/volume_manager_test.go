// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"testing"

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
