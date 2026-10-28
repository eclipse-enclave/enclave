// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

// mountGuardHome returns a resolved temp home with a project and a credential
// directory, as the guard receives resolved paths.
func mountGuardHome(t *testing.T) (home, project, aws string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project = filepath.Join(home, "code", "project")
	aws = filepath.Join(home, ".aws")
	for _, dir := range []string{project, aws} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home, project, aws
}

func guardProject(dir string) model.Project {
	return model.Project{Dir: dir, RealDir: dir}
}

func cliAddDirs(dirs ...string) []additionalDir {
	var additional []additionalDir
	for _, dir := range dirs {
		additional = append(additional, additionalDir{path: dir, origin: "--add-dir"})
	}
	return additional
}

func TestCheckSensitiveMountsPassesProjectDirectories(t *testing.T) {
	home, project, _ := mountGuardHome(t)
	if err := checkSensitiveMounts(guardProject(project), cliAddDirs(t.TempDir()), home, false); err != nil {
		t.Fatalf("project directory refused: %v", err)
	}
}

func TestCheckSensitiveMountsRefusesWithoutOptIn(t *testing.T) {
	home, _, aws := mountGuardHome(t)
	m2 := filepath.Join(home, ".m2")
	if err := os.Mkdir(m2, 0o755); err != nil {
		t.Fatal(err)
	}
	additional := append(cliAddDirs(aws), additionalDir{path: m2, origin: "add_readonly_dirs in global config"})
	err := checkSensitiveMounts(guardProject(home), additional, home, false)
	if err == nil {
		t.Fatal("expected the home directory, ~/.aws, and ~/.m2 to be refused")
	}
	msg := err.Error()
	for _, want := range []string{
		home + " (project directory): is your home directory",
		aws + " (--add-dir): is ~/.aws",
		m2 + " (add_readonly_dirs in global config): is ~/.m2",
		"--allow-sensitive-mounts",
		model.EnvAllowSensitiveMounts + "=1",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q", msg, want)
		}
	}
}

func TestCheckSensitiveMountsWarnsWhenAllowed(t *testing.T) {
	home, _, aws := mountGuardHome(t)
	var err error
	_, stderr := captureOutput(t, func() {
		err = checkSensitiveMounts(guardProject(home), cliAddDirs(aws), home, true)
	})
	if err != nil {
		t.Fatalf("opted-in run refused: %v", err)
	}
	for _, want := range []string{"Mounting sensitive host path " + home, "Mounting sensitive host path " + aws} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warnings %q do not mention %q", stderr, want)
		}
	}
}

func TestSensitiveMountsAllowed(t *testing.T) {
	for _, tc := range []struct {
		flag    bool
		source  model.OptionSource
		env     string
		set     bool
		allowed bool
	}{
		{flag: true, source: model.SourceCLI, allowed: true},
		{env: "1", set: true, allowed: true},
		{env: "0", set: true, allowed: false},
		{source: model.SourceCLI, env: "1", set: true, allowed: false},
		{allowed: false},
	} {
		if tc.set {
			t.Setenv(model.EnvAllowSensitiveMounts, tc.env)
		} else {
			t.Setenv(model.EnvAllowSensitiveMounts, "")
			if err := os.Unsetenv(model.EnvAllowSensitiveMounts); err != nil {
				t.Fatal(err)
			}
		}
		if got := sensitiveMountsAllowed(tc.flag, tc.source); got != tc.allowed {
			t.Errorf("sensitiveMountsAllowed(flag=%v, source=%v, env=%q set=%v) = %v, want %v", tc.flag, tc.source, tc.env, tc.set, got, tc.allowed)
		}
	}
}

func TestValidateAdditionalDirsKeepsEachEntrysOrigin(t *testing.T) {
	project := t.TempDir()
	shared := t.TempDir()
	cliDir := t.TempDir()
	global := config.Defaults{AddDirs: []string{shared}, AddReadonlyDirs: []string{shared, filepath.Join(project, "missing")}}
	cliOpts := config.DefaultOptions()
	cliOpts.AddDirs = []string{cliDir}
	cliSources := model.OptionSources{}
	cliSources.AddDirs = model.SourceCLI
	opts, _, _ := config.ResolveOptionsForTool(cliOpts, cliSources, global, config.Defaults{}, "")

	_, _, err := validateAdditionalDirs(opts, project, "")
	if err == nil || !strings.Contains(err.Error(), "add_readonly_dirs in global config: directory not found") {
		t.Fatalf("error = %v, want the missing directory attributed to global config", err)
	}

	opts.AddReadonlyDirs = []string{shared}
	writable, readonly, err := validateAdditionalDirs(opts, project, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []additionalDir{{path: shared, origin: "add_dirs in global config"}, {path: cliDir, origin: "--add-dir"}}
	if !reflect.DeepEqual(writable, want) {
		t.Fatalf("writable = %#v, want %#v", writable, want)
	}
	if len(readonly) != 0 {
		t.Fatalf("readonly = %#v, want the writable duplicate skipped", readonly)
	}
}
