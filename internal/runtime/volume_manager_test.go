// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"path/filepath"
	"slices"
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

func TestConfigVolumeRelativeDirsIncludesSkillsDirOutsideSettingsSubtree(t *testing.T) {
	t.Parallel()

	m := volumeManager{
		Runtime: &Runtime{
			containerHome: model.ContainerHome,
			profile: model.Profile{
				Name:           "antigravity",
				ConfigDir:      ".gemini",
				SkillsDir:      ".gemini/config/skills",
				SettingsFile:   "antigravity-settings.json",
				SettingsTarget: ".gemini/antigravity-cli/settings.json",
			},
		},
	}

	got := m.configVolumeRelativeDirs()
	want := []string{"antigravity-cli", filepath.Join("config", "skills")}
	if !slices.Equal(got, want) {
		t.Fatalf("configVolumeRelativeDirs() = %v, want %v", got, want)
	}
}

func TestConfigVolumeRelativeDirsIncludesNestedMemoryDir(t *testing.T) {
	t.Parallel()

	m := volumeManager{
		Runtime: &Runtime{
			containerHome: model.ContainerHome,
			profile: model.Profile{
				Name:           "tool",
				ConfigDir:      ".tool",
				MemoryDir:      ".tool/state/memory",
				SettingsFile:   "settings.json",
				SettingsTarget: ".tool/settings.json",
			},
		},
	}

	got := m.configVolumeRelativeDirs()
	want := []string{filepath.Join("state", "memory")}
	if !slices.Equal(got, want) {
		t.Fatalf("configVolumeRelativeDirs() = %v, want %v", got, want)
	}
}

func TestConfigVolumeRelativeDirsMatchesClaudeProfile(t *testing.T) {
	t.Parallel()

	m := volumeManager{
		Runtime: &Runtime{
			containerHome: model.ContainerHome,
			profile: model.Profile{
				Name:           "claude",
				ConfigDir:      ".claude",
				SkillsDir:      ".claude/skills",
				MemoryDir:      ".claude/memory",
				SettingsFile:   "claude-settings.json",
				SettingsTarget: ".claude/settings.json",
				Providers: []model.ProviderConfig{
					{Name: "anthropic", AuthFiles: []string{"config.json"}},
				},
			},
		},
	}

	got := m.configVolumeRelativeDirs()
	want := []string{"memory", "skills"}
	if !slices.Equal(got, want) {
		t.Fatalf("configVolumeRelativeDirs() = %v, want %v", got, want)
	}
}

func TestConfigVolumeRelativeDirsSkipsRootLevelFiles(t *testing.T) {
	t.Parallel()

	m := volumeManager{
		Runtime: &Runtime{
			containerHome: model.ContainerHome,
			profile: model.Profile{
				Name:           "tool",
				ConfigDir:      ".tool",
				SettingsFile:   "settings.json",
				SettingsTarget: ".tool/settings.json",
				Providers: []model.ProviderConfig{
					{Name: "provider", AuthFiles: []string{"config.json"}},
				},
			},
		},
	}

	if got := m.configVolumeRelativeDirs(); len(got) != 0 {
		t.Fatalf("configVolumeRelativeDirs() = %v, want no nested directories", got)
	}
}
