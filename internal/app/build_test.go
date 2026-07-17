// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/config"
	"enclave/internal/docker"
	"enclave/internal/model"
)

func TestPlanAgentUpdatesForTools(t *testing.T) {
	now := time.Date(2026, time.March, 20, 12, 0, 0, 0, time.UTC)

	t.Run("skips automatic hook checks before interval expires", func(t *testing.T) {
		t.Setenv(model.EnvAgentUpdateIntervalHours, "24")
		home := t.TempDir()
		existing := "2026-03-20T10:00:00Z"
		writeTestAgentUpdateStamp(t, home, existing, now.Add(-time.Hour))
		resolverCalls := 0

		plan, err := planAgentUpdatesForTools(false, []string{"codex"}, home, now, func(tool string) automaticToolUpdateResult {
			resolverCalls++
			return automaticToolUpdateResult{Fingerprint: "1.2.3", Known: true, Changed: true}
		})
		if err != nil {
			t.Fatalf("planAgentUpdatesForTools returned error: %v", err)
		}
		if plan.NeedsRebuild {
			t.Fatal("expected no rebuild before interval expiry")
		}
		if got := plan.Stamps["codex"]; got != existing {
			t.Fatalf("expected existing stamp %q, got %q", existing, got)
		}
		if len(plan.PendingWrites) != 0 {
			t.Fatalf("expected no pending writes, got %v", plan.PendingWrites)
		}
		if resolverCalls != 0 {
			t.Fatalf("expected no resolver calls before interval expiry, got %d", resolverCalls)
		}
	})

	t.Run("changed fingerprint triggers rebuild when interval expires", func(t *testing.T) {
		t.Setenv(model.EnvAgentUpdateIntervalHours, "24")
		home := t.TempDir()
		existing := "2026-03-19T10:00:00Z"
		writeTestAgentUpdateStamp(t, home, existing, now.Add(-25*time.Hour))

		plan, err := planAgentUpdatesForTools(false, []string{"codex"}, home, now, func(tool string) automaticToolUpdateResult {
			return automaticToolUpdateResult{Fingerprint: "1.2.3", Known: true, Changed: true}
		})
		if err != nil {
			t.Fatalf("planAgentUpdatesForTools returned error: %v", err)
		}
		want := formatAgentUpdateStamp(now)
		if !plan.NeedsRebuild {
			t.Fatal("expected rebuild when fingerprint changes")
		}
		if got := plan.Stamps["codex"]; got != want {
			t.Fatalf("expected planned stamp %q, got %q", want, got)
		}
		if got := plan.PendingWrites["codex"]; got != want {
			t.Fatalf("expected pending write %q, got %q", want, got)
		}
		if got := plan.PendingFingerprintWrites["codex"]; got != "1.2.3" {
			t.Fatalf("expected pending fingerprint %q, got %q", "1.2.3", got)
		}
		if !plan.ForceTools["codex"] {
			t.Fatal("expected auto-detected fingerprint change to force online install, not reuse stale cache")
		}
	})

	t.Run("unchanged fingerprint does not rebuild after interval expires", func(t *testing.T) {
		t.Setenv(model.EnvAgentUpdateIntervalHours, "24")
		home := t.TempDir()
		existing := "2026-03-20T10:00:00Z"
		writeTestAgentUpdateStamp(t, home, existing, now.Add(-25*time.Hour))

		plan, err := planAgentUpdatesForTools(false, []string{"codex"}, home, now, func(tool string) automaticToolUpdateResult {
			return automaticToolUpdateResult{Fingerprint: "1.2.3", Known: true, Changed: false}
		})
		if err != nil {
			t.Fatalf("planAgentUpdatesForTools returned error: %v", err)
		}
		if plan.NeedsRebuild {
			t.Fatal("expected no rebuild when fingerprint is unchanged")
		}
		if got := plan.Stamps["codex"]; got != existing {
			t.Fatalf("expected existing stamp %q, got %q", existing, got)
		}
		if len(plan.PendingWrites) != 0 {
			t.Fatalf("expected no pending writes, got %v", plan.PendingWrites)
		}
		if len(plan.PendingFingerprintWrites) != 0 {
			t.Fatalf("expected no pending fingerprints, got %v", plan.PendingFingerprintWrites)
		}
	})

	t.Run("hook failure does not rebuild after interval expires", func(t *testing.T) {
		t.Setenv(model.EnvAgentUpdateIntervalHours, "24")
		home := t.TempDir()
		existing := "2026-03-20T10:00:00Z"
		writeTestAgentUpdateStamp(t, home, existing, now.Add(-25*time.Hour))

		plan, err := planAgentUpdatesForTools(false, []string{"codex"}, home, now, func(tool string) automaticToolUpdateResult {
			return automaticToolUpdateResult{}
		})
		if err != nil {
			t.Fatalf("planAgentUpdatesForTools returned error: %v", err)
		}
		if plan.NeedsRebuild {
			t.Fatal("expected no rebuild when hook result is unknown")
		}
		if got := plan.Stamps["codex"]; got != existing {
			t.Fatalf("expected existing stamp %q, got %q", existing, got)
		}
	})

	t.Run("disabled automatic resolver does not rebuild on missing stamp", func(t *testing.T) {
		t.Setenv(model.EnvAgentUpdateIntervalHours, "24")
		home := t.TempDir()

		plan, err := planAgentUpdatesForTools(false, []string{"codex"}, home, now, nil)
		if err != nil {
			t.Fatalf("planAgentUpdatesForTools returned error: %v", err)
		}
		if plan.NeedsRebuild {
			t.Fatal("expected no rebuild without automatic resolver")
		}
		if got := plan.Stamps["codex"]; got != "unknown" {
			t.Fatalf("expected unknown stamp, got %q", got)
		}
		if len(plan.PendingWrites) != 0 {
			t.Fatalf("expected no pending writes, got %v", plan.PendingWrites)
		}
	})

	t.Run("interval zero checks hooks on every run", func(t *testing.T) {
		t.Setenv(model.EnvAgentUpdateIntervalHours, "0")
		home := t.TempDir()
		existing := "2026-03-20T10:00:00Z"
		writeTestAgentUpdateStamp(t, home, existing, now.Add(-time.Minute))

		plan, err := planAgentUpdatesForTools(false, []string{"codex"}, home, now, func(tool string) automaticToolUpdateResult {
			return automaticToolUpdateResult{Fingerprint: "2.0.0", Known: true, Changed: true}
		})
		if err != nil {
			t.Fatalf("planAgentUpdatesForTools returned error: %v", err)
		}
		want := formatAgentUpdateStamp(now)
		if !plan.NeedsRebuild {
			t.Fatal("expected rebuild when interval is zero and fingerprint changed")
		}
		if got := plan.Stamps["codex"]; got != want {
			t.Fatalf("expected planned stamp %q, got %q", want, got)
		}
	})

	t.Run("force-all updates every tool regardless of interval", func(t *testing.T) {
		t.Setenv(model.EnvAgentUpdateIntervalHours, "24")
		home := t.TempDir()
		// A fresh stamp would normally suppress an update; forceAll (the
		// `update` command path) bypasses the interval and the online probe
		// entirely, so a nil resolver must not matter.
		writeTestAgentUpdateStamp(t, home, "2026-03-20T10:00:00Z", now.Add(-time.Minute))

		plan, err := planAgentUpdatesForTools(true, []string{"claude", "codex"}, home, now, nil)
		if err != nil {
			t.Fatalf("planAgentUpdatesForTools returned error: %v", err)
		}
		want := formatAgentUpdateStamp(now)
		if !plan.NeedsRebuild {
			t.Fatal("expected rebuild for forced update")
		}
		for _, tool := range []string{"claude", "codex"} {
			if got := plan.Stamps[tool]; got != want {
				t.Fatalf("expected forced stamp %q for %s, got %q", want, tool, got)
			}
			if got := plan.PendingWrites[tool]; got != want {
				t.Fatalf("expected pending write %q for %s, got %q", want, tool, got)
			}
			if !plan.ForceTools[tool] {
				t.Fatalf("expected %s to be forced", tool)
			}
		}
	})
}

func TestResolveAutomaticToolUpdate(t *testing.T) {
	t.Run("no hook returns unknown without probing", func(t *testing.T) {
		paths := writeTestCodexToolPaths(t, false)
		probeCalls := 0

		result := resolveAutomaticToolUpdate(paths, buildConfig{ImageName: "enclave:test"}, t.TempDir(), "codex", func(paths model.Paths, buildCfg buildConfig, tool string) (string, bool, error) {
			probeCalls++
			return "1.2.3", true, nil
		})
		if result.Known || result.Changed || result.Fingerprint != "" {
			t.Fatalf("expected unknown result, got %+v", result)
		}
		if probeCalls != 0 {
			t.Fatalf("expected probe not to run without a hook, got %d calls", probeCalls)
		}
	})

	t.Run("same fingerprint does not trigger update", func(t *testing.T) {
		paths := writeTestCodexToolPaths(t, true)
		home := t.TempDir()
		writeTestAgentUpdateFingerprint(t, home, "codex", "1.2.3")

		result := resolveAutomaticToolUpdate(paths, buildConfig{ImageName: "enclave:test"}, home, "codex", func(paths model.Paths, buildCfg buildConfig, tool string) (string, bool, error) {
			return "1.2.3", true, nil
		})
		if !result.Known {
			t.Fatal("expected known fingerprint result")
		}
		if result.Changed {
			t.Fatalf("expected unchanged fingerprint, got %+v", result)
		}
	})

	t.Run("changed fingerprint triggers update", func(t *testing.T) {
		paths := writeTestCodexToolPaths(t, true)
		home := t.TempDir()
		writeTestAgentUpdateFingerprint(t, home, "codex", "1.2.3")

		result := resolveAutomaticToolUpdate(paths, buildConfig{ImageName: "enclave:test"}, home, "codex", func(paths model.Paths, buildCfg buildConfig, tool string) (string, bool, error) {
			return "2.0.0", true, nil
		})
		if !result.Known || !result.Changed {
			t.Fatalf("expected changed fingerprint result, got %+v", result)
		}
		if result.Fingerprint != "2.0.0" {
			t.Fatalf("expected fingerprint %q, got %q", "2.0.0", result.Fingerprint)
		}
	})

	t.Run("probe failure is treated as unknown", func(t *testing.T) {
		paths := writeTestCodexToolPaths(t, true)

		result := resolveAutomaticToolUpdate(paths, buildConfig{ImageName: "enclave:test"}, t.TempDir(), "codex", func(paths model.Paths, buildCfg buildConfig, tool string) (string, bool, error) {
			return "", false, errors.New("probe failed")
		})
		if result.Known || result.Changed || result.Fingerprint != "" {
			t.Fatalf("expected unknown result, got %+v", result)
		}
	})
}

func TestBackfillMissingAgentUpdateFingerprints(t *testing.T) {
	t.Run("seeds missing fingerprints after a successful build", func(t *testing.T) {
		paths := writeTestCodexToolPaths(t, true)
		home := t.TempDir()
		plan := agentUpdatePlan{
			Tools:                    []string{"codex"},
			PendingFingerprintWrites: map[string]string{},
		}
		probeCalls := 0

		err := backfillMissingAgentUpdateFingerprints(&plan, paths, buildConfig{ImageName: "enclave:test"}, home, func(paths model.Paths, buildCfg buildConfig, tool string) (string, bool, error) {
			probeCalls++
			return "1.2.3", true, nil
		})
		if err != nil {
			t.Fatalf("backfillMissingAgentUpdateFingerprints returned error: %v", err)
		}
		if probeCalls != 1 {
			t.Fatalf("expected one probe call, got %d", probeCalls)
		}
		if got := plan.PendingFingerprintWrites["codex"]; got != "1.2.3" {
			t.Fatalf("expected seeded fingerprint %q, got %q", "1.2.3", got)
		}
	})

	t.Run("skips tools that already have stored fingerprints", func(t *testing.T) {
		paths := writeTestCodexToolPaths(t, true)
		home := t.TempDir()
		writeTestAgentUpdateFingerprint(t, home, "codex", "1.2.3")
		plan := agentUpdatePlan{
			Tools:                    []string{"codex"},
			PendingFingerprintWrites: map[string]string{},
		}
		probeCalls := 0

		err := backfillMissingAgentUpdateFingerprints(&plan, paths, buildConfig{ImageName: "enclave:test"}, home, func(paths model.Paths, buildCfg buildConfig, tool string) (string, bool, error) {
			probeCalls++
			return "2.0.0", true, nil
		})
		if err != nil {
			t.Fatalf("backfillMissingAgentUpdateFingerprints returned error: %v", err)
		}
		if probeCalls != 0 {
			t.Fatalf("expected no probe calls when fingerprint already exists, got %d", probeCalls)
		}
		if len(plan.PendingFingerprintWrites) != 0 {
			t.Fatalf("expected no new pending fingerprints, got %v", plan.PendingFingerprintWrites)
		}
	})

	t.Run("preserves planned fingerprint writes", func(t *testing.T) {
		paths := writeTestCodexToolPaths(t, true)
		home := t.TempDir()
		plan := agentUpdatePlan{
			Tools:                    []string{"codex"},
			PendingFingerprintWrites: map[string]string{"codex": "2.0.0"},
		}
		probeCalls := 0

		err := backfillMissingAgentUpdateFingerprints(&plan, paths, buildConfig{ImageName: "enclave:test"}, home, func(paths model.Paths, buildCfg buildConfig, tool string) (string, bool, error) {
			probeCalls++
			return "3.0.0", true, nil
		})
		if err != nil {
			t.Fatalf("backfillMissingAgentUpdateFingerprints returned error: %v", err)
		}
		if probeCalls != 0 {
			t.Fatalf("expected no probe calls when fingerprint write is already pending, got %d", probeCalls)
		}
		if got := plan.PendingFingerprintWrites["codex"]; got != "2.0.0" {
			t.Fatalf("expected pending fingerprint to stay %q, got %q", "2.0.0", got)
		}
	})
}

func TestBuildImageDoesNotCommitAgentUpdateStampsOnFailure(t *testing.T) {
	stubBuildTestHooks(t,
		func(context.Context, docker.BuildRequest, io.Writer) error {
			return errors.New("build failed")
		},
		func(context.Context, string) (bool, error) {
			return false, nil
		},
		func(context.Context, string) (docker.PruneReport, error) {
			return docker.PruneReport{}, nil
		},
	)

	paths := writeTestBuildPaths(t)
	home := t.TempDir()
	stamp := "2026-03-20T12:00:00Z"
	fingerprint := "2.0.0"

	err := buildImage(
		context.Background(),
		paths,
		model.Host{Home: home, UID: "1000", GID: "1000"},
		"structural-hash",
		buildConfig{ImageName: "enclave:test", Target: "standard"},
		model.BuildOptions{},
		"codex",
		agentUpdatePlan{
			Tools:                    []string{"codex"},
			Stamps:                   map[string]string{"codex": stamp},
			ForceTools:               map[string]bool{},
			PendingWrites:            map[string]string{"codex": stamp},
			PendingFingerprintWrites: map[string]string{"codex": fingerprint},
		},
	)
	if err == nil {
		t.Fatal("expected buildImage to fail")
	}
	if !strings.Contains(err.Error(), "failed to build image") {
		t.Fatalf("expected build failure message, got %q", err.Error())
	}
	if _, statErr := os.Stat(agentUpdateStampFile(config.HostBuildDir(home), "codex")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no committed stamp after failed build, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(agentUpdateFingerprintFile(config.HostBuildDir(home), "codex")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no committed fingerprint after failed build, stat err=%v", statErr)
	}
}

func TestBuildImageCommitsAgentUpdateStampsOnSuccess(t *testing.T) {
	stubBuildTestHooks(t,
		func(context.Context, docker.BuildRequest, io.Writer) error {
			return nil
		},
		func(context.Context, string) (bool, error) {
			return false, nil
		},
		func(context.Context, string) (docker.PruneReport, error) {
			return docker.PruneReport{}, nil
		},
	)

	paths := writeTestBuildPaths(t)
	home := t.TempDir()
	stamp := "2026-03-20T12:00:00Z"
	fingerprint := "2.0.0"

	err := buildImage(
		context.Background(),
		paths,
		model.Host{Home: home, UID: "1000", GID: "1000"},
		"structural-hash",
		buildConfig{ImageName: "enclave:test", Target: "standard"},
		model.BuildOptions{},
		"codex",
		agentUpdatePlan{
			Tools:                    []string{"codex"},
			Stamps:                   map[string]string{"codex": stamp},
			ForceTools:               map[string]bool{},
			PendingWrites:            map[string]string{"codex": stamp},
			PendingFingerprintWrites: map[string]string{"codex": fingerprint},
		},
	)
	if err != nil {
		t.Fatalf("buildImage returned error: %v", err)
	}
	got, ok, err := readAgentUpdateStateValue(agentUpdateStampFile(config.HostBuildDir(home), "codex"))
	if err != nil {
		t.Fatalf("readAgentUpdateStateValue returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected committed stamp after successful build")
	}
	if got != stamp {
		t.Fatalf("expected committed stamp %q, got %q", stamp, got)
	}
	gotFingerprint, ok, err := readAgentUpdateStateValue(agentUpdateFingerprintFile(config.HostBuildDir(home), "codex"))
	if err != nil {
		t.Fatalf("readAgentUpdateStateValue returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected committed fingerprint after successful build")
	}
	if gotFingerprint != fingerprint {
		t.Fatalf("expected committed fingerprint %q, got %q", fingerprint, gotFingerprint)
	}
}

func TestBuildImageUsesExplicitBuildIdentityAndBuildxCache(t *testing.T) {
	var captured docker.BuildRequest
	stubBuildTestHooks(t,
		func(_ context.Context, req docker.BuildRequest, _ io.Writer) error {
			captured = req
			return nil
		},
		func(context.Context, string) (bool, error) {
			return false, nil
		},
		func(context.Context, string) (docker.PruneReport, error) {
			return docker.PruneReport{}, nil
		},
	)

	paths := writeTestBuildPaths(t)
	cacheDir := t.TempDir()
	err := buildImage(
		context.Background(),
		paths,
		model.Host{Home: t.TempDir(), UID: "2000", GID: "2000"},
		"structural-hash",
		buildConfig{ImageName: "enclave:test", Target: "standard"},
		model.BuildOptions{
			BuildUID:        "1000",
			BuildGID:        "1000",
			BuildxCacheDir:  cacheDir,
			BuildxCacheFrom: []string{"type=registry,ref=example.test/cache"},
		},
		"codex",
		agentUpdatePlan{
			Tools:      []string{"codex"},
			Stamps:     map[string]string{"codex": "unknown"},
			ForceTools: map[string]bool{},
		},
	)
	if err != nil {
		t.Fatalf("buildImage returned error: %v", err)
	}
	if captured.BuildArgs["USER_ID"] != "1000" || captured.BuildArgs["GROUP_ID"] != "1000" {
		t.Fatalf("unexpected build identity args: %v", captured.BuildArgs)
	}
	if got := captured.BuildxCacheFrom; len(got) != 1 || got[0] != "type=registry,ref=example.test/cache" {
		t.Fatalf("unexpected buildx cache-from: %v", got)
	}
	wantCacheTo := "type=local,dest=" + cacheDir + ",mode=max"
	if got := captured.BuildxCacheTo; len(got) != 1 || got[0] != wantCacheTo {
		t.Fatalf("unexpected buildx cache-to: %v", got)
	}
}

func TestPrepareBuildContextMergesUserAndBuiltinExtensions(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")
	userExtensions := filepath.Join(tmp, "user-ext")

	writeAppFile(t, filepath.Join(appRoot, "Dockerfile"), "FROM scratch\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "entrypoint.sh"), "#!/usr/bin/env bash\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "docs", "tools.md"), "bundled docs\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "README.md"), "builtin extensions\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", "spec.yaml"), "schemaVersion: \"1\"\nkind: sandbox\nname: claude\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", model.InstallScriptFilename), "builtin\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", "templates", "base.json"), "builtin-template\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "features", "python-dev", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: python-dev\n", 0o644)

	writeAppFile(t, filepath.Join(userExtensions, "tools", "claude", model.InstallScriptFilename), "user\n", 0o755)
	writeAppFile(t, filepath.Join(userExtensions, "tools", "claude", "templates", "custom.json"), "user-template\n", 0o644)

	paths := model.Paths{
		AppRoot:           appRoot,
		ExtensionsDir:     filepath.Join(appRoot, "extensions"),
		ToolsDir:          filepath.Join(appRoot, "extensions", "tools"),
		FeaturesDir:       filepath.Join(appRoot, "extensions", "features"),
		UserExtensionsDir: userExtensions,
		UserToolsDir:      filepath.Join(userExtensions, "tools"),
		UserFeaturesDir:   filepath.Join(userExtensions, "features"),
	}

	contextDir, cleanup, err := prepareBuildContext(paths, runtimeImageSelection{})
	if err != nil {
		t.Fatalf("prepareBuildContext: %v", err)
	}
	if contextDir == appRoot {
		t.Fatalf("expected staged context, got app root: %s", contextDir)
	}

	if _, err := os.Stat(filepath.Join(contextDir, "Dockerfile")); err != nil {
		t.Fatalf("expected top-level app file in staged context: %v", err)
	}
	if gotDocs := readAppFile(t, filepath.Join(contextDir, "docs", "tools.md")); gotDocs != "bundled docs\n" {
		t.Fatalf("expected docs file in staged context, got %q", gotDocs)
	}

	gotInstall := readAppFile(t, filepath.Join(contextDir, "extensions", "tools", "claude", model.InstallScriptFilename))
	if gotInstall != "user\n" {
		t.Fatalf("expected user install.sh override, got %q", gotInstall)
	}
	gotSpec := readAppFile(t, filepath.Join(contextDir, "extensions", "tools", "claude", "spec.yaml"))
	if gotSpec != "schemaVersion: \"1\"\nkind: sandbox\nname: claude\n" {
		t.Fatalf("expected builtin spec.yaml fallback, got %q", gotSpec)
	}
	if _, err := os.Stat(filepath.Join(contextDir, "extensions", "tools", "claude", "templates", "base.json")); err != nil {
		t.Fatalf("expected builtin template in merged context: %v", err)
	}
	if _, err := os.Stat(filepath.Join(contextDir, "extensions", "tools", "claude", "templates", "custom.json")); err != nil {
		t.Fatalf("expected user template in merged context: %v", err)
	}
	info, err := os.Lstat(filepath.Join(contextDir, "extensions", "tools", "claude", model.InstallScriptFilename))
	if err != nil {
		t.Fatalf("lstat merged install.sh: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected merged install.sh to be a real file, not a symlink")
	}

	cleanup()
	if _, err := os.Stat(contextDir); !os.IsNotExist(err) {
		t.Fatalf("expected staged context cleanup to remove %s", contextDir)
	}
}

func TestPrepareBuildContextCopiesSpecYAMLIntoContext(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")

	const specYAML = `schemaVersion: "1"
kind: mixin
name: specfeat
priority: 40
failOnInstallError: true
needsRoot: true
aptPackages: [vim, ripgrep]
`
	writeAppFile(t, filepath.Join(appRoot, "Dockerfile"), "FROM scratch\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "features", "specfeat", "spec.yaml"), specYAML, 0o644)

	paths := model.Paths{
		AppRoot:           appRoot,
		ExtensionsDir:     filepath.Join(appRoot, "extensions"),
		ToolsDir:          filepath.Join(appRoot, "extensions", "tools"),
		FeaturesDir:       filepath.Join(appRoot, "extensions", "features"),
		UserExtensionsDir: filepath.Join(tmp, "user-ext"),
		UserToolsDir:      filepath.Join(tmp, "user-ext", "tools"),
		UserFeaturesDir:   filepath.Join(tmp, "user-ext", "features"),
	}

	contextDir, cleanup, err := prepareBuildContext(paths, runtimeImageSelection{})
	if err != nil {
		t.Fatalf("prepareBuildContext: %v", err)
	}
	defer cleanup()

	// The image build reads extension metadata directly from spec.yaml, so the
	// context must carry the verbatim file without synthesizing a compatibility
	// manifest.
	got := readAppFile(t, filepath.Join(contextDir, "extensions", "features", "specfeat", "spec.yaml"))
	if got != specYAML {
		t.Fatalf("expected verbatim spec.yaml in staged context, got %q", got)
	}
	assertPathMissing(t, filepath.Join(contextDir, "extensions", "features", "specfeat", "extension.json"))
}

func TestHashMergedExtensionFilesChangesForUserAndBuiltinMutations(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")
	userExtensions := filepath.Join(tmp, "user-ext")

	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", model.InstallScriptFilename), "builtin-v1\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", "spec.yaml"), "schemaVersion: \"1\"\nkind: sandbox\nname: claude\n", 0o644)

	paths := model.Paths{
		AppRoot:           appRoot,
		ExtensionsDir:     filepath.Join(appRoot, "extensions"),
		ToolsDir:          filepath.Join(appRoot, "extensions", "tools"),
		FeaturesDir:       filepath.Join(appRoot, "extensions", "features"),
		UserExtensionsDir: userExtensions,
		UserToolsDir:      filepath.Join(userExtensions, "tools"),
		UserFeaturesDir:   filepath.Join(userExtensions, "features"),
	}

	hash1, err := hashMergedExtensionFiles(paths, runtimeImageSelection{})
	if err != nil {
		t.Fatalf("hashMergedExtensionFiles baseline: %v", err)
	}

	writeAppFile(t, filepath.Join(userExtensions, "tools", "claude", model.InstallScriptFilename), "user-v1\n", 0o755)
	hash2, err := hashMergedExtensionFiles(paths, runtimeImageSelection{})
	if err != nil {
		t.Fatalf("hashMergedExtensionFiles user add: %v", err)
	}
	if hash2 == hash1 {
		t.Fatal("expected hash to change after adding user override file")
	}

	writeAppFile(t, filepath.Join(userExtensions, "tools", "claude", model.InstallScriptFilename), "user-v2\n", 0o755)
	hash3, err := hashMergedExtensionFiles(paths, runtimeImageSelection{})
	if err != nil {
		t.Fatalf("hashMergedExtensionFiles user modify: %v", err)
	}
	if hash3 == hash2 {
		t.Fatal("expected hash to change after modifying user override file")
	}

	if err := os.Remove(filepath.Join(userExtensions, "tools", "claude", model.InstallScriptFilename)); err != nil {
		t.Fatalf("remove user override: %v", err)
	}
	hash4, err := hashMergedExtensionFiles(paths, runtimeImageSelection{})
	if err != nil {
		t.Fatalf("hashMergedExtensionFiles user remove: %v", err)
	}
	if hash4 != hash1 {
		t.Fatalf("expected hash to return to baseline after removing override, got baseline=%s now=%s", hash1, hash4)
	}

	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", model.InstallScriptFilename), "builtin-v2\n", 0o755)
	hash5, err := hashMergedExtensionFiles(paths, runtimeImageSelection{})
	if err != nil {
		t.Fatalf("hashMergedExtensionFiles builtin modify: %v", err)
	}
	if hash5 == hash4 {
		t.Fatal("expected hash to change after modifying builtin extension file")
	}
}

func TestHashRuntimeImageStaticFilesTracksCopiedInputs(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")
	paths := model.Paths{
		AppRoot:         appRoot,
		BuildScriptsDir: filepath.Join(appRoot, "runtime-assets", "build-scripts"),
	}

	writeAppFile(t, filepath.Join(appRoot, ".dockerignore"), "docs/security\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "runtime-assets", "build-scripts", "install-agent-helper-bins.sh"), "helpers-v1\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "runtime-assets", "auth-reconcile.sh"), "auth-v1\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "runtime-assets", "net.sh"), "net-v1\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "docs", "README.md"), "docs-v1\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "docs", "assets", "appicon.txt"), "asset-v1\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "docs", "security", "README.md"), "ignored-v1\n", 0o644)

	hash1, err := hashRuntimeImageStaticFiles(paths)
	if err != nil {
		t.Fatalf("hashRuntimeImageStaticFiles baseline: %v", err)
	}

	writeAppFile(t, filepath.Join(appRoot, "runtime-assets", "build-scripts", "install-agent-helper-bins.sh"), "helpers-v2\n", 0o755)
	hash2, err := hashRuntimeImageStaticFiles(paths)
	if err != nil {
		t.Fatalf("hashRuntimeImageStaticFiles build script: %v", err)
	}
	if hash2 == hash1 {
		t.Fatal("expected hash to change after modifying copied build script")
	}

	writeAppFile(t, filepath.Join(appRoot, "runtime-assets", "auth-reconcile.sh"), "auth-v2\n", 0o755)
	hash3, err := hashRuntimeImageStaticFiles(paths)
	if err != nil {
		t.Fatalf("hashRuntimeImageStaticFiles auth reconcile: %v", err)
	}
	if hash3 == hash2 {
		t.Fatal("expected hash to change after modifying copied auth reconcile script")
	}

	writeAppFile(t, filepath.Join(appRoot, "runtime-assets", "net.sh"), "net-v2\n", 0o644)
	hash4, err := hashRuntimeImageStaticFiles(paths)
	if err != nil {
		t.Fatalf("hashRuntimeImageStaticFiles net helper: %v", err)
	}
	if hash4 == hash3 {
		t.Fatal("expected hash to change after modifying copied net helper")
	}

	writeAppFile(t, filepath.Join(appRoot, "docs", "README.md"), "docs-v2\n", 0o644)
	hash5, err := hashRuntimeImageStaticFiles(paths)
	if err != nil {
		t.Fatalf("hashRuntimeImageStaticFiles docs: %v", err)
	}
	if hash5 == hash4 {
		t.Fatal("expected hash to change after modifying copied docs")
	}

	writeAppFile(t, filepath.Join(appRoot, "docs", "assets", "appicon.txt"), "asset-v2\n", 0o644)
	hash6, err := hashRuntimeImageStaticFiles(paths)
	if err != nil {
		t.Fatalf("hashRuntimeImageStaticFiles docs asset: %v", err)
	}
	if hash6 == hash5 {
		t.Fatal("expected hash to change after modifying a nested docs asset")
	}

	writeAppFile(t, filepath.Join(appRoot, "docs", "security", "README.md"), "ignored-v2\n", 0o644)
	hash7, err := hashRuntimeImageStaticFiles(paths)
	if err != nil {
		t.Fatalf("hashRuntimeImageStaticFiles ignored docs: %v", err)
	}
	if hash7 != hash6 {
		t.Fatalf("expected .dockerignore-excluded docs to keep hash stable, got %s then %s", hash6, hash7)
	}

	writeAppFile(t, filepath.Join(appRoot, ".dockerignore"), "", 0o644)
	hash8, err := hashRuntimeImageStaticFiles(paths)
	if err != nil {
		t.Fatalf("hashRuntimeImageStaticFiles dockerignore: %v", err)
	}
	if hash8 == hash7 {
		t.Fatal("expected hash to change after modifying .dockerignore")
	}
}

func TestCheckRuntimeImageBuildPreflightFailsWithoutBuildx(t *testing.T) {
	stubBuildxAvailable(t, false)

	err := checkRuntimeImageBuildPreflight(context.Background())
	if err == nil {
		t.Fatal("expected missing buildx to fail")
	}
	if !strings.Contains(err.Error(), "docker buildx is unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckRuntimeImageBuildPreflightFailsOnCriticalLowDockerStorage(t *testing.T) {
	stubBuildxAvailable(t, true)
	stubDockerRootFreeSpace(t, func(context.Context) (string, uint64, error) {
		return "/var/lib/docker", 512 * 1024 * 1024, nil
	})

	err := checkRuntimeImageBuildPreflight(context.Background())
	if err == nil {
		t.Fatal("expected critical low storage to fail")
	}
	if !strings.Contains(err.Error(), "docker storage is critically low") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckRuntimeImageBuildPreflightContinuesOnWarningThreshold(t *testing.T) {
	stubBuildxAvailable(t, true)
	stubDockerRootFreeSpace(t, func(context.Context) (string, uint64, error) {
		return "/var/lib/docker", 5 * 1024 * 1024 * 1024, nil
	})

	if err := checkRuntimeImageBuildPreflight(context.Background()); err != nil {
		t.Fatalf("expected low-but-noncritical storage to warn and continue, got %v", err)
	}
}

func TestCheckRuntimeImageBuildPreflightContinuesWhenStorageUnknown(t *testing.T) {
	stubBuildxAvailable(t, true)
	stubDockerRootFreeSpace(t, func(context.Context) (string, uint64, error) {
		return "", 0, errors.New("docker info unavailable")
	})

	if err := checkRuntimeImageBuildPreflight(context.Background()); err != nil {
		t.Fatalf("expected unavailable storage inspection to warn and continue, got %v", err)
	}
}

func TestAvailableBytesAtPathReportsPositiveFreeSpace(t *testing.T) {
	// Exercises the real syscall.Statfs path that the preflight tests stub out,
	// keeping the cross-platform Bavail*Bsize formula honest on every OS that
	// `go test` runs on.
	freeBytes, err := availableBytesAtPath(t.TempDir())
	if err != nil {
		t.Fatalf("availableBytesAtPath returned error: %v", err)
	}
	if freeBytes == 0 {
		t.Fatal("expected a positive amount of free space for the temp dir filesystem")
	}
}

func TestRenderDockerfileIncludesOnlySelectedTools(t *testing.T) {
	paths := writeTestBuildPaths(t)

	got, err := renderDockerfile(paths.Dockerfile, []string{"codex"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("renderDockerfile returned error: %v", err)
	}
	if !strings.Contains(got, "FROM tool-base AS tool-codex") {
		t.Fatalf("expected selected tool stage in Dockerfile, got:\n%s", got)
	}
	if strings.Contains(got, "tool-claude") {
		t.Fatalf("did not expect unselected tool stage in Dockerfile, got:\n%s", got)
	}
}

func TestRenderDockerfileEmptyToolsProducesValidStandardStage(t *testing.T) {
	paths := writeTestBuildPaths(t)

	got, err := renderDockerfile(paths.Dockerfile, []string{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("renderDockerfile returned error: %v", err)
	}
	if !strings.Contains(got, "FROM feature-base AS standard") {
		t.Fatalf("expected a standard stage even with no selected tools, got:\n%s", got)
	}
	if strings.Contains(got, "FROM tool-base AS tool-") {
		t.Fatalf("did not expect any per-tool stage for an empty selection, got:\n%s", got)
	}
	// An empty selection must not emit a bare glob that fails (or warns) when
	// /tmp/installed-tools.d has no files; it must use the robust find form.
	if strings.Contains(got, "cat /tmp/installed-tools.d/*") {
		t.Fatalf("empty selection must not emit an unmatched-glob cat, got:\n%s", got)
	}
	if !strings.Contains(got, "find /tmp/installed-tools.d -type f -exec cat {} +") {
		t.Fatalf("expected robust find-based manifest assembly, got:\n%s", got)
	}
}

func TestPrepareBuildContextScopesToSelectedExtensions(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")
	userExtensions := filepath.Join(tmp, "user-ext")

	writeAppFile(t, filepath.Join(appRoot, "Dockerfile"), "FROM scratch\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "entrypoint.sh"), "#!/usr/bin/env bash\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "docs", "tools.md"), "bundled docs\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", "spec.yaml"), "schemaVersion: \"1\"\nkind: sandbox\nname: claude\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", model.InstallScriptFilename), "claude\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "codex", "spec.yaml"), "schemaVersion: \"1\"\nkind: sandbox\nname: codex\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "codex", model.InstallScriptFilename), "codex\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "features", "python-dev", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: python-dev\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "features", "shell-extras", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: shell-extras\n", 0o644)
	writeAppFile(t, filepath.Join(userExtensions, "tools", "claude", "templates", "custom.json"), "user-template\n", 0o644)
	writeAppFile(t, filepath.Join(userExtensions, "tools", "codex", "templates", "custom.json"), "unselected-template\n", 0o644)

	paths := model.Paths{
		AppRoot:           appRoot,
		ExtensionsDir:     filepath.Join(appRoot, "extensions"),
		ToolsDir:          filepath.Join(appRoot, "extensions", "tools"),
		FeaturesDir:       filepath.Join(appRoot, "extensions", "features"),
		UserExtensionsDir: userExtensions,
		UserToolsDir:      filepath.Join(userExtensions, "tools"),
		UserFeaturesDir:   filepath.Join(userExtensions, "features"),
	}

	selection := runtimeImageSelection{
		Tools:    []string{"claude"},
		Features: []string{"python-dev"},
	}
	contextDir, cleanup, err := prepareBuildContext(paths, selection)
	if err != nil {
		t.Fatalf("prepareBuildContext: %v", err)
	}
	defer cleanup()

	assertPathExists(t, filepath.Join(contextDir, "extensions", "tools", "claude", model.InstallScriptFilename))
	assertPathExists(t, filepath.Join(contextDir, "extensions", "tools", "claude", "templates", "custom.json"))
	assertPathMissing(t, filepath.Join(contextDir, "extensions", "tools", "codex"))
	assertPathExists(t, filepath.Join(contextDir, "extensions", "features", "python-dev", "spec.yaml"))
	assertPathMissing(t, filepath.Join(contextDir, "extensions", "features", "shell-extras"))
}

func TestHashMergedExtensionFilesIgnoresUnselectedExtensions(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")
	userExtensions := filepath.Join(tmp, "user-ext")

	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", "spec.yaml"), "schemaVersion: \"1\"\nkind: sandbox\nname: claude\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", model.InstallScriptFilename), "claude-v1\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "codex", "spec.yaml"), "schemaVersion: \"1\"\nkind: sandbox\nname: codex\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "codex", model.InstallScriptFilename), "codex-v1\n", 0o755)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "features", "python-dev", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: python-dev\n", 0o644)
	writeAppFile(t, filepath.Join(appRoot, "extensions", "features", "shell-extras", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: shell-extras\n", 0o644)

	paths := model.Paths{
		AppRoot:           appRoot,
		ExtensionsDir:     filepath.Join(appRoot, "extensions"),
		ToolsDir:          filepath.Join(appRoot, "extensions", "tools"),
		FeaturesDir:       filepath.Join(appRoot, "extensions", "features"),
		UserExtensionsDir: userExtensions,
		UserToolsDir:      filepath.Join(userExtensions, "tools"),
		UserFeaturesDir:   filepath.Join(userExtensions, "features"),
	}

	selection := runtimeImageSelection{
		Tools:    []string{"claude"},
		Features: []string{"python-dev"},
	}

	hash1, err := hashMergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("hashMergedExtensionFiles baseline: %v", err)
	}

	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "codex", model.InstallScriptFilename), "codex-v2\n", 0o755)
	hash2, err := hashMergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("hashMergedExtensionFiles unselected tool mutate: %v", err)
	}
	if hash2 != hash1 {
		t.Fatalf("expected unselected tool mutation to keep hash stable, got %s then %s", hash1, hash2)
	}

	writeAppFile(t, filepath.Join(appRoot, "extensions", "features", "shell-extras", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: shell-extras\ndefaultEnabled: true\n", 0o644)
	hash3, err := hashMergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("hashMergedExtensionFiles unselected feature mutate: %v", err)
	}
	if hash3 != hash1 {
		t.Fatalf("expected unselected feature mutation to keep hash stable, got %s then %s", hash1, hash3)
	}

	writeAppFile(t, filepath.Join(appRoot, "extensions", "tools", "claude", model.InstallScriptFilename), "claude-v2\n", 0o755)
	hash4, err := hashMergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("hashMergedExtensionFiles selected tool mutate: %v", err)
	}
	if hash4 == hash1 {
		t.Fatal("expected selected tool mutation to change hash")
	}
}

func writeTestAgentUpdateStamp(t *testing.T, home string, stamp string, modTime time.Time) {
	t.Helper()
	stampDir := config.HostBuildDir(home)
	stampFile := agentUpdateStampFile(stampDir, "codex")
	if err := os.MkdirAll(stampDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(stampFile, []byte(stamp+"\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	if err := os.Chtimes(stampFile, modTime, modTime); err != nil {
		t.Fatalf("os.Chtimes returned error: %v", err)
	}
}

func writeTestAgentUpdateFingerprint(t *testing.T, home string, tool string, fingerprint string) {
	t.Helper()
	fingerprintDir := config.HostBuildDir(home)
	fingerprintFile := agentUpdateFingerprintFile(fingerprintDir, tool)
	if err := os.MkdirAll(fingerprintDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(fingerprintFile, []byte(fingerprint+"\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
}

func writeTestBuildPaths(t *testing.T) model.Paths {
	t.Helper()
	return writeTestToolsPaths(t, []string{"claude", "codex"}, false)
}

func writeTestCodexToolPaths(t *testing.T, withCheckUpdate bool) model.Paths {
	t.Helper()
	return writeTestToolsPaths(t, []string{"codex"}, withCheckUpdate)
}

func writeTestToolsPaths(t *testing.T, tools []string, withCheckUpdate bool) model.Paths {
	t.Helper()
	root := t.TempDir()
	dockerfile := filepath.Join(root, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM debian:trixie-slim AS tool-base\nFROM tool-base AS feature-base\n# BEGIN ENCLAVE_FEATURE_INSTALLS\n# END ENCLAVE_FEATURE_INSTALLS\n# BEGIN ENCLAVE_TOOL_INSTALLS\n# END ENCLAVE_TOOL_INSTALLS\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	for _, tool := range tools {
		toolDir := filepath.Join(root, "extensions", "tools", tool)
		if err := os.MkdirAll(toolDir, 0o755); err != nil {
			t.Fatalf("os.WriteFile returned error: %v", err)
		}
		if err := os.WriteFile(filepath.Join(toolDir, "spec.yaml"), []byte("schemaVersion: \"1\"\nkind: sandbox\nname: "+tool+"\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile returned error: %v", err)
		}
		if withCheckUpdate {
			if err := os.WriteFile(filepath.Join(toolDir, model.CheckUpdateScriptFilename), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
				t.Fatalf("os.WriteFile returned error: %v", err)
			}
		}
	}
	return model.Paths{
		AppRoot:       root,
		Dockerfile:    dockerfile,
		ExtensionsDir: filepath.Join(root, "extensions"),
		ToolsDir:      filepath.Join(root, "extensions", "tools"),
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path %s to exist: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected path %s to be missing, err=%v", path, err)
	}
}

func writeAppFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readAppFile(t *testing.T, path string) string {
	t.Helper()
	// #nosec G304 -- path is test-controlled under t.TempDir.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func stubBuildTestHooks(
	t *testing.T,
	build func(context.Context, docker.BuildRequest, io.Writer) error,
	exists func(context.Context, string) (bool, error),
	prune func(context.Context, string) (docker.PruneReport, error),
) {
	t.Helper()
	// buildImage runs the build preflight, whose buildx probe would otherwise
	// hit the real docker CLI.
	stubBuildxAvailable(t, true)
	origBuild := dockerBuildImage
	origExists := dockerImageExists
	origPrune := dockerImagePrune
	dockerBuildImage = build
	dockerImageExists = exists
	dockerImagePrune = prune
	t.Cleanup(func() {
		dockerBuildImage = origBuild
		dockerImageExists = origExists
		dockerImagePrune = origPrune
	})
}

func stubDockerRootFreeSpace(t *testing.T, fn func(context.Context) (string, uint64, error)) {
	t.Helper()
	orig := dockerRootFreeSpace
	dockerRootFreeSpace = fn
	t.Cleanup(func() {
		dockerRootFreeSpace = orig
	})
}

func TestCheckDockerClassifiesFailures(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pingErr       error
		want          string // empty means success expected
		preserveCause bool
	}{
		{name: "reachable", pingErr: nil, want: ""},
		{name: "cli missing", pingErr: &exec.Error{Name: "docker", Err: exec.ErrNotFound}, want: "docker CLI not found"},
		{name: "daemon down", pingErr: errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"), want: "docker daemon is not reachable", preserveCause: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubDockerPing(t, tc.pingErr)
			err := checkDocker()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
			if tc.preserveCause && !errors.Is(err, tc.pingErr) {
				t.Fatalf("expected error to preserve cause %v, got %v", tc.pingErr, err)
			}
		})
	}
}

func stubDockerPing(t *testing.T, err error) {
	t.Helper()
	orig := dockerPing
	dockerPing = func(context.Context) error { return err }
	t.Cleanup(func() {
		dockerPing = orig
	})
}

func stubBuildxAvailable(t *testing.T, available bool) {
	t.Helper()
	orig := dockerBuildxAvailable
	dockerBuildxAvailable = func(context.Context) bool { return available }
	t.Cleanup(func() {
		dockerBuildxAvailable = orig
	})
}
