// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestAddSkillMountsSkipsWhenSkillsDirUnset(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		profile: model.Profile{Name: "codex"},
	}
	acc := newMountAccumulator(nil, nil)
	if err := r.addSkillMounts(acc); err != nil {
		t.Fatalf("addSkillMounts returned error: %v", err)
	}
	if len(acc.Mounts()) != 0 {
		t.Fatalf("expected no mounts, got %d", len(acc.Mounts()))
	}
	if _, ok := lookupEnv(acc.Env(), model.EnvToolSkillsDir); ok {
		t.Fatalf("did not expect %s env var", model.EnvToolSkillsDir)
	}
}

func TestAddSkillMountsMergesGlobalAndProjectSkills(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"
	globalSkillsDir := config.HostSkillsDir(home)
	projectSkillsDir := config.HostProjectSkillsDir(home, projectHash)

	writeSkill(t, filepath.Join(globalSkillsDir, "global-only"), "global")
	writeSkill(t, filepath.Join(globalSkillsDir, "shared"), "global-shared")
	writeSkill(t, filepath.Join(projectSkillsDir, "project-only"), "project")
	writeSkill(t, filepath.Join(projectSkillsDir, "shared"), "project-shared")
	if err := os.WriteFile(filepath.Join(globalSkillsDir, "README.md"), []byte("skip me"), 0o644); err != nil {
		t.Fatalf("write global non-skill file: %v", err)
	}

	r := &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: projectHash},
		profile:       model.Profile{Name: "codex", SkillsDir: ".codex/skills"},
		containerHome: "/home/agent",
	}

	acc := newMountAccumulator(nil, nil)
	if err := r.addSkillMounts(acc); err != nil {
		t.Fatalf("addSkillMounts returned error: %v", err)
	}

	target := "/home/agent/.codex/skills"
	source, ok := lookupMountSource(acc.Mounts(), target)
	if !ok {
		t.Fatalf("expected skills mount at %s", target)
	}
	if !strings.HasSuffix(source, filepath.Join(projectHash, "codex", model.GeneratedSkillsDirName, defaultConfigKey)) {
		t.Fatalf("unexpected skills source mount: %s", source)
	}
	if got, ok := lookupEnv(acc.Env(), model.EnvToolSkillsDir); !ok || got != target {
		t.Fatalf("expected %s=%s, got %q (present=%v)", model.EnvToolSkillsDir, target, got, ok)
	}

	assertSkillBody(t, filepath.Join(source, "global-only"), "global")
	assertSkillBody(t, filepath.Join(source, "project-only"), "project")
	assertSkillBody(t, filepath.Join(source, "shared"), "project-shared")

	if _, err := os.Stat(filepath.Join(source, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("expected non-directory entry to be skipped in generated skills, err=%v", err)
	}

	mountFound := false
	for _, m := range acc.Mounts() {
		if m.ContainerPath == target {
			mountFound = true
			if !m.ReadOnly {
				t.Fatalf("expected skills mount to be read-only")
			}
		}
	}
	if !mountFound {
		t.Fatalf("expected mount metadata for %s", target)
	}
}

func TestAddSkillMountsMergesBuiltinUserExtGlobalAndProjectSkills(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	toolsDir := t.TempDir()
	userToolsDir := t.TempDir()
	projectHash := "projhash"

	// Built-in skills (lowest priority)
	builtinSkillsDir := filepath.Join(toolsDir, "claude", "skills")
	writeSkill(t, filepath.Join(builtinSkillsDir, "builtin-only"), "builtin")
	writeSkill(t, filepath.Join(builtinSkillsDir, "shared-bu"), "builtin-shared")
	writeSkill(t, filepath.Join(builtinSkillsDir, "shared-all"), "builtin-all")

	// User-extension skills (override built-in)
	userExtSkillsDir := filepath.Join(userToolsDir, "claude", "skills")
	writeSkill(t, filepath.Join(userExtSkillsDir, "user-ext-only"), "user-ext")
	writeSkill(t, filepath.Join(userExtSkillsDir, "shared-bu"), "user-ext-shared")
	writeSkill(t, filepath.Join(userExtSkillsDir, "shared-all"), "user-ext-all")

	globalSkillsDir := config.HostSkillsDir(home)
	writeSkill(t, filepath.Join(globalSkillsDir, "global-shared-only"), "global-shared")
	writeSkill(t, filepath.Join(globalSkillsDir, "shared-all"), "global-shared-all")
	writeSkill(t, filepath.Join(globalSkillsDir, "scope-order"), "global-shared")

	projectSkillsDir := config.HostProjectSkillsDir(home, projectHash)
	writeSkill(t, filepath.Join(projectSkillsDir, "project-shared-only"), "project-shared")
	writeSkill(t, filepath.Join(projectSkillsDir, "shared-all"), "project-shared-all")
	writeSkill(t, filepath.Join(projectSkillsDir, "scope-order"), "project-shared")

	r := &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: projectHash},
		profile:       model.Profile{Name: "claude", ConfigDir: ".claude", SkillsDir: ".claude/skills"},
		paths:         model.Paths{ToolsDir: toolsDir, UserToolsDir: userToolsDir},
		containerHome: "/home/agent",
	}

	acc := newMountAccumulator(nil, nil)
	if err := r.addSkillMounts(acc); err != nil {
		t.Fatalf("addSkillMounts returned error: %v", err)
	}

	target := "/home/agent/.claude/skills"
	source, ok := lookupMountSource(acc.Mounts(), target)
	if !ok {
		t.Fatalf("expected skills mount at %s", target)
	}

	// Each tier's unique skill present
	assertSkillBody(t, filepath.Join(source, "builtin-only"), "builtin")
	assertSkillBody(t, filepath.Join(source, "user-ext-only"), "user-ext")
	assertSkillBody(t, filepath.Join(source, "global-shared-only"), "global-shared")
	assertSkillBody(t, filepath.Join(source, "project-shared-only"), "project-shared")
	assertSkillBody(t, filepath.Join(source, "shared-bu"), "user-ext-shared")
	assertSkillBody(t, filepath.Join(source, "scope-order"), "project-shared")
	assertSkillBody(t, filepath.Join(source, "shared-all"), "project-shared-all")
}

func TestAddSkillMountsIncludesEnabledFeatureSkillsOnly(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	featuresDir := t.TempDir()
	projectHash := "projhash"

	// An enabled feature ships skills; a present-but-not-enabled feature must
	// not contribute (off-by-default gating).
	writeSkill(t, filepath.Join(featuresDir, "example", "skills", "example-skill"), "feature-skill")
	writeSkill(t, filepath.Join(featuresDir, "other-feature", "skills", "other-skill"), "should-not-appear")
	// A feature skill name also present as a project skill: project wins.
	writeSkill(t, filepath.Join(featuresDir, "example", "skills", "shared"), "feature-shared")
	projectSkillsDir := config.HostProjectSkillsDir(home, projectHash)
	writeSkill(t, filepath.Join(projectSkillsDir, "shared"), "project-shared")

	r := &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: projectHash},
		profile:       model.Profile{Name: "claude", SkillsDir: ".claude/skills"},
		paths:         model.Paths{FeaturesDir: featuresDir},
		features:      []model.Extension{{Name: "example"}}, // enabled set
		containerHome: "/home/agent",
	}

	acc := newMountAccumulator(nil, nil)
	if err := r.addSkillMounts(acc); err != nil {
		t.Fatalf("addSkillMounts returned error: %v", err)
	}

	source, ok := lookupMountSource(acc.Mounts(), "/home/agent/.claude/skills")
	if !ok {
		t.Fatalf("expected skills mount")
	}

	assertSkillBody(t, filepath.Join(source, "example-skill"), "feature-skill")
	// Project skill overrides a same-named feature skill.
	assertSkillBody(t, filepath.Join(source, "shared"), "project-shared")
	// A feature that is not enabled contributes nothing.
	if _, err := os.Stat(filepath.Join(source, "other-skill")); !os.IsNotExist(err) {
		t.Fatalf("skill from a non-enabled feature must be absent, err=%v", err)
	}
}

func TestAddSkillMountsIsolatesConcurrentFeatureSelections(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	featuresDir := t.TempDir()
	projectHash := "projhash"

	// Two features ship a same-named skill with different content. Concurrent
	// sessions selecting different features must not clobber each other's
	// read-only mount, so their generated directories must be keyed apart.
	writeSkill(t, filepath.Join(featuresDir, "alpha", "skills", "feature-skill"), "alpha-body")
	writeSkill(t, filepath.Join(featuresDir, "beta", "skills", "feature-skill"), "beta-body")

	runSession := func(feature string, configKey string) string {
		r := &Runtime{
			host:          model.Host{Home: home},
			project:       model.Project{Hash: projectHash},
			profile:       model.Profile{Name: "claude", SkillsDir: ".claude/skills"},
			paths:         model.Paths{FeaturesDir: featuresDir},
			features:      []model.Extension{{Name: feature}},
			containerHome: "/home/agent",
		}
		r.configVolSuffix = configKey
		r.configVolReady = true

		acc := newMountAccumulator(nil, nil)
		if err := r.addSkillMounts(acc); err != nil {
			t.Fatalf("addSkillMounts returned error: %v", err)
		}
		source, ok := lookupMountSource(acc.Mounts(), "/home/agent/.claude/skills")
		if !ok {
			t.Fatalf("expected skills mount for feature %q", feature)
		}
		return source
	}

	// Session A composes first, then session B composes with a different feature
	// selection under a distinct config key.
	alphaSource := runSession("alpha", "session-a")
	betaSource := runSession("beta", "session-b")

	if alphaSource == betaSource {
		t.Fatalf("sessions with different config keys shared a skills directory: %s", alphaSource)
	}
	// Session B must not have overwritten session A's already-mounted skills.
	assertSkillBody(t, filepath.Join(alphaSource, "feature-skill"), "alpha-body")
	assertSkillBody(t, filepath.Join(betaSource, "feature-skill"), "beta-body")
}

func TestAddSkillMountsCreatesManagedDirectories(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"

	r := &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: projectHash},
		profile:       model.Profile{Name: "claude", SkillsDir: ".claude/skills"},
		containerHome: "/home/agent",
	}

	acc := newMountAccumulator(nil, nil)
	if err := r.addSkillMounts(acc); err != nil {
		t.Fatalf("addSkillMounts returned error: %v", err)
	}

	expectedDirs := []string{
		config.HostSkillsDir(home),
		config.HostProjectSkillsDir(home, projectHash),
		filepath.Join(config.HostProjectToolDir(home, projectHash, "claude"), model.GeneratedSkillsDirName),
	}
	for _, dir := range expectedDirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("expected directory %s to exist: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", dir)
		}
	}
}

func TestAddSkillMountsSkipsNonPortableSharedSkills(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"
	globalSkillsDir := config.HostSkillsDir(home)
	portableDir := filepath.Join(globalSkillsDir, "portable")
	writeSkill(t, portableDir, "portable")
	externalFile := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(externalFile, []byte("external"), 0o644); err != nil {
		t.Fatalf("write external skill resource: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(portableDir, "external.txt")); err != nil {
		t.Fatalf("symlink external skill resource: %v", err)
	}
	writeSkill(t, filepath.Join(globalSkillsDir, "claude", "old-layout"), "old")

	invalidDir := filepath.Join(globalSkillsDir, "harness-specific")
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("mkdir invalid shared skill: %v", err)
	}
	invalid := "---\nname: harness-specific\ndescription: Invalid shared skill\nallowed-tools: Read\n---\ninvalid"
	if err := os.WriteFile(filepath.Join(invalidDir, "SKILL.md"), []byte(invalid), 0o644); err != nil {
		t.Fatalf("write invalid shared skill: %v", err)
	}

	r := &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: projectHash},
		profile:       model.Profile{Name: "claude", ConfigDir: ".claude", SkillsDir: ".claude/skills"},
		containerHome: "/home/agent",
	}
	acc := newMountAccumulator(nil, nil)
	if err := r.addSkillMounts(acc); err != nil {
		t.Fatalf("addSkillMounts returned error: %v", err)
	}

	source, ok := lookupMountSource(acc.Mounts(), "/home/agent/.claude/skills")
	if !ok {
		t.Fatal("expected generated skills mount")
	}
	assertSkillBody(t, filepath.Join(source, "portable"), "portable")
	assertPathMissing(t, filepath.Join(source, "portable", "external.txt"))
	assertPathMissing(t, filepath.Join(source, "harness-specific"))
	assertPathMissing(t, filepath.Join(source, "claude"))
}

func writeSkill(t *testing.T, skillDir string, body string) {
	t.Helper()
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	name := filepath.Base(skillDir)
	content := fmt.Sprintf("---\nname: %s\ndescription: Test skill\n---\n%s", name, body)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func assertSkillBody(t *testing.T, skillDir string, expected string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read %s: %v", skillDir, err)
	}
	parts := strings.SplitN(string(content), "---\n", 3)
	if len(parts) != 3 || strings.TrimPrefix(parts[2], "\n") != expected {
		t.Fatalf("skill %s content = %q, want body %q", skillDir, string(content), expected)
	}
}

func TestAddSkillMountsConcurrentRuns(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"
	globalSkillsDir := config.HostSkillsDir(home)
	projectSkillsDir := config.HostProjectSkillsDir(home, projectHash)

	writeSkill(t, filepath.Join(globalSkillsDir, "shared"), "global")
	writeSkill(t, filepath.Join(globalSkillsDir, "global-only"), "global-only")
	writeSkill(t, filepath.Join(projectSkillsDir, "shared"), "project")
	writeSkill(t, filepath.Join(projectSkillsDir, "project-only"), "project-only")

	const goroutines = 8
	const iterationsPerGoroutine = 15
	errCh := make(chan error, goroutines)

	for range goroutines {
		go func() {
			r := &Runtime{
				host:          model.Host{Home: home},
				project:       model.Project{Hash: projectHash},
				profile:       model.Profile{Name: "codex", SkillsDir: ".codex/skills"},
				containerHome: "/home/agent",
			}
			for i := 0; i < iterationsPerGoroutine; i++ {
				acc := newMountAccumulator(nil, nil)
				if err := r.addSkillMounts(acc); err != nil {
					errCh <- err
					return
				}
			}
			errCh <- nil
		}()
	}

	for range goroutines {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent addSkillMounts failed: %v", err)
		}
	}

	generated := filepath.Join(config.HostProjectToolDir(home, projectHash, "codex"), model.GeneratedSkillsDirName, defaultConfigKey)
	assertSkillBody(t, filepath.Join(generated, "global-only"), "global-only")
	assertSkillBody(t, filepath.Join(generated, "project-only"), "project-only")
	assertSkillBody(t, filepath.Join(generated, "shared"), "project")
}
