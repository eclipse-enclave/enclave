// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/model"
)

func writeFeatureFixture(t *testing.T, featuresDir, name, spec string, installMode os.FileMode, withInstall bool) {
	t.Helper()
	dir := filepath.Join(featuresDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec.yaml: %v", err)
	}
	if withInstall {
		if err := os.WriteFile(filepath.Join(dir, model.InstallScriptFilename), []byte("#!/usr/bin/env bash\n"), installMode); err != nil {
			t.Fatalf("write install.sh: %v", err)
		}
	}
}

func TestResolveFeatureInstalls(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	paths := model.Paths{FeaturesDir: featuresDir}

	// apt + executable script + needs root, explicit priority.
	writeFeatureFixture(t, featuresDir, "alpha",
		"schemaVersion: \"1\"\nkind: mixin\nname: alpha\naptPackages: [\"x\"]\nneedsRoot: true\npriority: 30\n", 0o755, true)
	// no apt, no script, no priority -> defaults to 100.
	writeFeatureFixture(t, featuresDir, "beta",
		"schemaVersion: \"1\"\nkind: mixin\nname: beta\n", 0, false)
	// apt + a non-executable install.sh -> HasScript must be false (matches runtime -x check).
	writeFeatureFixture(t, featuresDir, "gamma",
		"schemaVersion: \"1\"\nkind: mixin\nname: gamma\naptPackages: [\"y\"]\n", 0o644, true)

	installs, err := resolveFeatureInstalls(paths, []string{"alpha", "beta", "gamma", "missing"}, false)
	if err != nil {
		t.Fatalf("resolveFeatureInstalls: %v", err)
	}
	if len(installs) != 3 {
		t.Fatalf("expected 3 installs (missing skipped), got %d: %+v", len(installs), installs)
	}
	byName := map[string]featureInstall{}
	for _, fi := range installs {
		byName[fi.Name] = fi
	}

	if a := byName["alpha"]; a.Priority != 30 || !a.HasApt || !a.HasScript || !a.NeedsRoot {
		t.Fatalf("alpha: %+v", a)
	}
	if b := byName["beta"]; b.Priority != model.DefaultExtensionPriority || b.HasApt || b.HasScript || b.NeedsRoot {
		t.Fatalf("beta: %+v", b)
	}
	if g := byName["gamma"]; !g.HasApt || g.HasScript {
		t.Fatalf("gamma should have apt but no runnable script (install.sh not executable): %+v", g)
	}
}

func TestResolveFeatureInstallsInstallCommands(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	paths := model.Paths{FeaturesDir: featuresDir}

	// commands.install with an explicit non-root user -> user-phase install.
	writeFeatureFixture(t, featuresDir, "cmds",
		"schemaVersion: \"1\"\nkind: mixin\nname: cmds\ncommands:\n  install:\n    - { command: [echo, hi], user: \"1000\" }\n", 0, false)
	// commands.install with a root entry -> declarative install in the root phase.
	writeFeatureFixture(t, featuresDir, "rootcmds",
		"schemaVersion: \"1\"\nkind: mixin\nname: rootcmds\ncommands:\n  install:\n    - { command: [apt-get, install, x], user: root }\n", 0, false)
	// commands.install with an OMITTED user -> defaults to root (sbx §6.1), so
	// it routes to the root phase.
	writeFeatureFixture(t, featuresDir, "defaultcmds",
		"schemaVersion: \"1\"\nkind: mixin\nname: defaultcmds\ncommands:\n  install:\n    - { command: [apt-get, install, y] }\n", 0, false)
	// Both commands.install AND an executable install.sh -> install.sh wins.
	writeFeatureFixture(t, featuresDir, "both",
		"schemaVersion: \"1\"\nkind: mixin\nname: both\ncommands:\n  install:\n    - { command: [echo, hi] }\n", 0o755, true)

	installs, err := resolveFeatureInstalls(paths, []string{"cmds", "rootcmds", "defaultcmds", "both"}, true)
	if err != nil {
		t.Fatalf("resolveFeatureInstalls: %v", err)
	}
	byName := map[string]featureInstall{}
	for _, fi := range installs {
		byName[fi.Name] = fi
	}

	if c := byName["cmds"]; !c.HasInstallCommands || c.InstallCommandsNeedRoot || c.HasScript {
		t.Fatalf("cmds should be user-phase install-commands: %+v", c)
	}
	if r := byName["rootcmds"]; !r.HasInstallCommands || !r.InstallCommandsNeedRoot {
		t.Fatalf("rootcmds should be root-phase install-commands: %+v", r)
	}
	if d := byName["defaultcmds"]; !d.HasInstallCommands || !d.InstallCommandsNeedRoot {
		t.Fatalf("omitted user must default to root phase: %+v", d)
	}
	if b := byName["both"]; b.HasInstallCommands || !b.HasScript {
		t.Fatalf("install.sh must win over commands.install: %+v", b)
	}
}

func TestAnyRootInstallUser(t *testing.T) {
	cases := []struct {
		users []string
		want  bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"agent", "1000"}, false},
		{[]string{"", "root"}, true},
		{[]string{"0"}, true},
	}
	for _, c := range cases {
		if got := anyRootInstallUser(c.users); got != c.want {
			t.Fatalf("anyRootInstallUser(%#v) = %v, want %v", c.users, got, c.want)
		}
	}
}

func TestResolveFeatureInstallsEmpty(t *testing.T) {
	installs, err := resolveFeatureInstalls(model.Paths{}, nil, false)
	if err != nil {
		t.Fatalf("resolveFeatureInstalls: %v", err)
	}
	if installs != nil {
		t.Fatalf("expected nil for empty selection, got %+v", installs)
	}
}

func TestFeatureHasExecutableInstallTreatsBuiltInAppRootScriptAsRunnable(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "extensions", "features")
	writeFeatureFixture(t, featuresDir, "gamma",
		`{"name":"gamma","type":"feature"}`, 0o644, true)
	installPath := filepath.Join(featuresDir, "gamma", model.InstallScriptFilename)
	if err := os.Chmod(installPath, 0o644); err != nil {
		t.Fatalf("chmod install.sh: %v", err)
	}

	paths := model.Paths{AppRoot: root, FeaturesDir: featuresDir}
	if !featureHasExecutableInstall(paths, "gamma") {
		t.Fatal("built-in app-root install.sh should be treated as runnable even if its installed mode lost +x")
	}

	userFeaturesDir := filepath.Join(t.TempDir(), "features")
	writeFeatureFixture(t, userFeaturesDir, "gamma",
		`{"name":"gamma","type":"feature"}`, 0o644, true)
	userInstallPath := filepath.Join(userFeaturesDir, "gamma", model.InstallScriptFilename)
	if err := os.Chmod(userInstallPath, 0o644); err != nil {
		t.Fatalf("chmod user install.sh: %v", err)
	}
	paths.UserFeaturesDir = userFeaturesDir
	if featureHasExecutableInstall(paths, "gamma") {
		t.Fatal("non-executable user override install.sh should not be treated as runnable")
	}
}

func TestReuseRuntimeImageByContentHash(t *testing.T) {
	origFind := dockerFindImageByLabel
	origTag := dockerTagImage
	t.Cleanup(func() {
		dockerFindImageByLabel = origFind
		dockerTagImage = origTag
	})

	t.Run("retags matching image", func(t *testing.T) {
		var gotLabel, gotValue, taggedSrc, taggedDst string
		dockerFindImageByLabel = func(_ context.Context, label, value string) (string, bool, error) {
			gotLabel, gotValue = label, value
			return "enclave-claude:branch-old", true, nil
		}
		dockerTagImage = func(_ context.Context, src, dst string) error {
			taggedSrc, taggedDst = src, dst
			return nil
		}
		reused, err := reuseRuntimeImageByContentHash(context.Background(), "enclave-claude:branch-new", "hash123")
		if err != nil || !reused {
			t.Fatalf("reused=%v err=%v", reused, err)
		}
		if gotLabel != model.LabelHash || gotValue != "hash123" {
			t.Fatalf("looked up wrong label: %s=%s", gotLabel, gotValue)
		}
		if taggedSrc != "enclave-claude:branch-old" || taggedDst != "enclave-claude:branch-new" {
			t.Fatalf("unexpected retag: %s -> %s", taggedSrc, taggedDst)
		}
	})

	t.Run("no match does not tag", func(t *testing.T) {
		dockerFindImageByLabel = func(_ context.Context, _, _ string) (string, bool, error) { return "", false, nil }
		dockerTagImage = func(_ context.Context, _, _ string) error {
			t.Fatal("must not tag when no match")
			return nil
		}
		reused, err := reuseRuntimeImageByContentHash(context.Background(), "enclave-claude:x", "h")
		if err != nil || reused {
			t.Fatalf("reused=%v err=%v", reused, err)
		}
	})

	t.Run("self match is not retagged", func(t *testing.T) {
		dockerFindImageByLabel = func(_ context.Context, _, _ string) (string, bool, error) {
			return "enclave-claude:x", true, nil
		}
		dockerTagImage = func(_ context.Context, _, _ string) error {
			t.Fatal("must not retag an image onto itself")
			return nil
		}
		reused, err := reuseRuntimeImageByContentHash(context.Background(), "enclave-claude:x", "h")
		if err != nil || reused {
			t.Fatalf("reused=%v err=%v", reused, err)
		}
	})

	t.Run("empty hash short-circuits", func(t *testing.T) {
		dockerFindImageByLabel = func(_ context.Context, _, _ string) (string, bool, error) {
			t.Fatal("must not query docker for an empty hash")
			return "", false, nil
		}
		reused, err := reuseRuntimeImageByContentHash(context.Background(), "enclave-claude:x", "")
		if err != nil || reused {
			t.Fatalf("reused=%v err=%v", reused, err)
		}
	})

	t.Run("lookup error propagates", func(t *testing.T) {
		dockerFindImageByLabel = func(_ context.Context, _, _ string) (string, bool, error) {
			return "", false, fmt.Errorf("boom")
		}
		reused, err := reuseRuntimeImageByContentHash(context.Background(), "enclave-claude:x", "h")
		if err == nil || reused {
			t.Fatalf("expected error, reused=%v err=%v", reused, err)
		}
	})
}
