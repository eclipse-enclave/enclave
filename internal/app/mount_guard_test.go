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
	"slices"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/mounts"
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

func cliAddDirs(dirs ...string) additionalDirs {
	return additionalDirs{dirs: dirs, flag: "--add-dir", key: "add_dirs", source: model.SourceCLI}
}

func TestCheckSensitiveMountsPassesProjectDirectories(t *testing.T) {
	home, project, _ := mountGuardHome(t)
	if err := checkSensitiveMounts(guardProject(project), []additionalDirs{cliAddDirs(t.TempDir())}, nil, home, false); err != nil {
		t.Fatalf("project directory refused: %v", err)
	}
}

func TestCheckSensitiveMountsRefusesWithoutOptIn(t *testing.T) {
	home, _, aws := mountGuardHome(t)
	m2 := filepath.Join(home, ".m2")
	if err := os.Mkdir(m2, 0o755); err != nil {
		t.Fatal(err)
	}
	globalConfig, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	additional := []additionalDirs{
		cliAddDirs(aws),
		{dirs: []string{m2}, flag: "--add-readonly-dir", key: "add_readonly_dirs", source: model.SourceGlobal},
	}
	err = checkSensitiveMounts(guardProject(home), additional, nil, home, false)
	if err == nil {
		t.Fatal("expected the home directory, ~/.aws, and ~/.m2 to be refused")
	}
	msg := err.Error()
	for _, want := range []string{
		home + " (project directory): is your home directory",
		aws + " (--add-dir): is ~/.aws",
		m2 + " (add_readonly_dirs from " + globalConfig + "): is ~/.m2",
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
		err = checkSensitiveMounts(guardProject(home), []additionalDirs{cliAddDirs(aws)}, nil, home, true)
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

// exceptionHome lays out a resolved temp home with the default XDG
// directories, Enclave's roots, a Homeshell workstream, and the SSH agent
// socket in ~/.local/state/ssh.
func exceptionHome(t *testing.T) string {
	t.Helper()
	home, _, _ := mountGuardHome(t)
	for _, dir := range []string{
		".local/share/homeshell/workstreams/p-x",
		".local/state/enclave",
		".local/state/host-connector",
		".local/state/ssh",
		".config/enclave",
		".cache/enclave",
		".cache/pip",
		".m2/repository",
		".dotfiles",
	} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(home, ".local", "state", "ssh", "agent.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", sock)
	return home
}

func globalConfigPath(t *testing.T) string {
	t.Helper()
	path, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// globalExceptions is opts with sensitive_dir_exceptions set in global config.
func globalExceptions(entries ...string) model.Options {
	opts := model.Options{}
	opts.SensitiveDirExceptions = entries
	opts.Sources.SensitiveDirExceptions = model.SourceGlobal
	return opts
}

type guardCase struct {
	name       string
	project    string
	additional []additionalDirs
	config     model.Options
	cli        []mounts.SensitiveDirException
	allowed    bool
	// refused holds fragments of the refusal; empty means the run starts.
	refused  []string
	warnings []string
	absent   []string
}

func runGuardCase(t *testing.T, home string, tc guardCase) {
	t.Helper()
	exceptions, warnings := configSensitiveDirExceptions(tc.config, home, tc.project)
	for _, warning := range warnings {
		t.Errorf("unexpected config warning: %s", warning)
	}
	exceptions = append(exceptions, tc.cli...)
	var err error
	_, stderr := captureOutput(t, func() {
		err = checkSensitiveMounts(guardProject(tc.project), tc.additional, exceptions, home, tc.allowed)
	})
	switch {
	case len(tc.refused) == 0 && err != nil:
		t.Fatalf("run refused: %v", err)
	case len(tc.refused) > 0 && err == nil:
		t.Fatalf("run started, want a refusal mentioning %q", tc.refused)
	}
	for _, want := range tc.refused {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	for _, want := range tc.warnings {
		if !strings.Contains(stderr, want) {
			t.Errorf("warnings %q do not mention %q", stderr, want)
		}
	}
	if len(tc.warnings) == 0 && strings.Contains(stderr, "Mounting sensitive host path") {
		t.Errorf("unexpected mount warning: %q", stderr)
	}
	for _, unwanted := range tc.absent {
		if strings.Contains(stderr, unwanted) || (err != nil && strings.Contains(err.Error(), unwanted)) {
			t.Errorf("output %q / %v mentions %q", stderr, err, unwanted)
		}
	}
}

func TestCheckSensitiveMountsHonorsSensitiveDirExceptions(t *testing.T) {
	home := exceptionHome(t)
	global := globalConfigPath(t)
	at := func(rel string) string { return filepath.Join(home, rel) }
	workstreams := ".local/share/homeshell/workstreams"
	workstream := at(workstreams + "/p-x")
	code := at("code/project")
	hardHint := ", which no sensitive-dir exception can cover; mount a narrower directory"
	exempted := func(entry string) string {
		return `; exempted by sensitive_dir_exceptions entry "` + entry + `" in ` + global
	}

	for _, tc := range []guardCase{
		{
			name:    "sensitive project without exceptions",
			project: workstream,
			refused: []string{
				workstream + " (project directory): is inside ~/.local",
				"To mount them anyway, pass them with --add-sensitive-dir or --add-readonly-sensitive-dir",
				"(this works for the project directory too), or list them in sensitive_dir_exceptions in\n" + global + ".",
				"Pass --allow-sensitive-mounts or set " + model.EnvAllowSensitiveMounts + "=1 to mount all sensitive paths anyway",
			},
			absent: []string{"Run enclave from a project directory"},
		},
		{
			name:    "home project without exceptions",
			project: home,
			refused: []string{
				home + " (project directory): is your home directory" + hardHint,
				"Run enclave from a project directory, or narrow the additional directories where they are set.\n",
				"Pass --allow-sensitive-mounts or set " + model.EnvAllowSensitiveMounts + "=1 to mount all sensitive paths anyway",
			},
			absent: []string{"--add-sensitive-dir", "sensitive_dir_exceptions"},
		},
		{
			name:    "project inside Enclave's config root without exceptions",
			project: at(".config/enclave"),
			refused: []string{
				at(".config/enclave") + " (project directory): is ~/.config/enclave" + hardHint,
				"Run enclave from a project directory",
			},
			absent: []string{"is inside ~/.config", "--add-sensitive-dir"},
		},
		{
			name:       "hard-protected and exemptable mounts without exceptions",
			project:    code,
			additional: []additionalDirs{cliAddDirs(at(".cache"), at(".m2"))},
			refused: []string{
				at(".cache") + " (--add-dir): contains ~/.cache/enclave" + hardHint,
				at(".m2") + " (--add-dir): is ~/.m2\n",
				"To mount the others anyway, pass them with --add-sensitive-dir",
			},
			absent: []string{"Run enclave from a project directory", "To mount them anyway"},
		},
		{
			name:     "config exception covering the project",
			project:  workstream,
			config:   globalExceptions("~/" + workstreams),
			warnings: []string{"Mounting sensitive host path " + workstream + " (project directory): is inside ~/.local" + exempted("~/"+workstreams)},
		},
		{
			name:     "flag exception for the project",
			project:  workstream,
			cli:      []mounts.SensitiveDirException{mounts.NewSensitiveDirException(workstream, "--add-sensitive-dir")},
			warnings: []string{"Mounting sensitive host path " + workstream + " (project directory): is inside ~/.local; exempted by --add-sensitive-dir"},
		},
		{
			name:       "mount is a parent of a config exception",
			project:    code,
			config:     globalExceptions("~/" + workstreams),
			additional: []additionalDirs{cliAddDirs(at(".local/share/homeshell"))},
			refused:    []string{at(".local/share/homeshell") + " (--add-dir): is inside ~/.local\n"},
		},
		{
			name:    "config exception for a configured mount",
			project: code,
			config:  globalExceptions("~/.m2"),
			additional: []additionalDirs{
				{dirs: []string{at(".m2")}, flag: "--add-readonly-dir", key: "add_readonly_dirs", source: model.SourceGlobal},
			},
			warnings: []string{"Mounting sensitive host path " + at(".m2") + " (add_readonly_dirs from " + global + "): is ~/.m2" + exempted("~/.m2")},
		},
		{
			name:    "read-only mount is a parent of a config exception",
			project: code,
			config:  globalExceptions("~/.m2/repository"),
			additional: []additionalDirs{
				{dirs: []string{at(".m2")}, flag: "--add-readonly-dir", key: "add_readonly_dirs", source: model.SourceCLI},
			},
			refused: []string{at(".m2") + " (--add-readonly-dir): is ~/.m2\n"},
		},
		{
			name:       "exception cannot cover Enclave's cache root",
			project:    code,
			config:     globalExceptions("~/.cache"),
			additional: []additionalDirs{cliAddDirs(at(".cache"))},
			refused:    []string{at(".cache") + " (--add-dir): contains ~/.cache/enclave" + hardHint},
		},
		{
			name:       "exception covers a sibling of Enclave's cache root",
			project:    code,
			config:     globalExceptions("~/.cache"),
			additional: []additionalDirs{cliAddDirs(at(".cache/pip"))},
			warnings:   []string{"Mounting sensitive host path " + at(".cache/pip") + " (--add-dir): is inside ~/.cache" + exempted("~/.cache")},
		},
		{
			name:    "flag exception cannot cover Enclave's config root",
			project: code,
			additional: []additionalDirs{
				{dirs: []string{at(".config")}, flag: "--add-sensitive-dir", source: model.SourceCLI},
			},
			cli:     []mounts.SensitiveDirException{mounts.NewSensitiveDirException(at(".config"), "--add-sensitive-dir")},
			refused: []string{at(".config") + " (--add-sensitive-dir): contains ~/.config/enclave" + hardHint},
		},
		{
			name:     "dotfiles project",
			project:  at(".dotfiles"),
			config:   globalExceptions("~/.dotfiles"),
			warnings: []string{"Mounting sensitive host path " + at(".dotfiles") + " (project directory): is ~/.dotfiles" + exempted("~/.dotfiles")},
		},
		{
			name:       "host socket directory",
			project:    code,
			config:     globalExceptions("~/.local/state"),
			additional: []additionalDirs{cliAddDirs(at(".local/state/host-connector"))},
			warnings:   []string{"Mounting sensitive host path " + at(".local/state/host-connector") + " (--add-dir): is inside ~/.local" + exempted("~/.local/state")},
		},
		{
			name:       "exception cannot cover the SSH agent socket",
			project:    code,
			config:     globalExceptions("~/.local/state"),
			additional: []additionalDirs{cliAddDirs(at(".local/state/ssh"))},
			refused:    []string{at(".local/state/ssh") + " (--add-dir): contains the SSH agent socket $SSH_AUTH_SOCK (~/.local/state/ssh/agent.sock)" + hardHint},
		},
		{
			name:    "flag exception cannot cover home",
			project: code,
			additional: []additionalDirs{
				{dirs: []string{home}, flag: "--add-sensitive-dir", source: model.SourceCLI},
			},
			cli:     []mounts.SensitiveDirException{mounts.NewSensitiveDirException(home, "--add-sensitive-dir")},
			refused: []string{home + " (--add-sensitive-dir): is your home directory" + hardHint},
		},
		{
			name:       "blanket opt-in mounts hard-protected locations without exemption warnings",
			project:    code,
			config:     globalExceptions("~/.cache"),
			additional: []additionalDirs{cliAddDirs(at(".cache"))},
			allowed:    true,
			warnings:   []string{"Mounting sensitive host path " + at(".cache") + " (--add-dir): contains ~/.cache/enclave\n"},
			absent:     []string{"exempted by"},
		},
		{
			name:    "blanket opt-in names Enclave's config root",
			project: code,
			additional: []additionalDirs{
				{dirs: []string{at(".config/enclave")}, flag: "--add-readonly-dir", key: "add_readonly_dirs", source: model.SourceCLI},
			},
			allowed:  true,
			warnings: []string{"Mounting sensitive host path " + at(".config/enclave") + " (--add-readonly-dir): is ~/.config/enclave\n"},
			absent:   []string{"is inside ~/.config"},
		},
		{
			name:       "flag for a directory that is not sensitive",
			project:    code,
			additional: []additionalDirs{{dirs: []string{code + "-other"}, flag: "--add-sensitive-dir", source: model.SourceCLI}},
			cli:        []mounts.SensitiveDirException{mounts.NewSensitiveDirException(code+"-other", "--add-sensitive-dir")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) { runGuardCase(t, home, tc) })
	}
}

// With ~/.config linked into ~/.dotfiles, the dotfiles repository contains
// Enclave's config root, which no exception covers.
func TestCheckSensitiveMountsRefusesDotfilesHoldingEnclaveConfig(t *testing.T) {
	home, _, _ := mountGuardHome(t)
	dotfiles := filepath.Join(home, ".dotfiles")
	if err := os.MkdirAll(filepath.Join(dotfiles, "config", "enclave"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dotfiles, "config"), filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	runGuardCase(t, home, guardCase{
		project: dotfiles,
		config:  globalExceptions("~/.dotfiles"),
		refused: []string{dotfiles + " (project directory): contains ~/.dotfiles/config/enclave (target of ~/.config/enclave), which no sensitive-dir exception can cover"},
	})
}

// Unusable entries are dropped with a warning, so the guard acts as if they
// were not there.
func TestConfigSensitiveDirExceptionsIgnoresUnusableEntries(t *testing.T) {
	home := exceptionHome(t)
	global := globalConfigPath(t)
	opts := globalExceptions("~", "/", filepath.Dir(home), "relative/x", " ", "~/.m2")

	exceptions, warnings := configSensitiveDirExceptions(opts, home, home)
	for _, want := range []string{
		`Ignoring sensitive_dir_exceptions entry "~" in ` + global + ": home and its parents cannot be exempted",
		`Ignoring sensitive_dir_exceptions entry "/" in ` + global + ": home and its parents cannot be exempted",
		`Ignoring sensitive_dir_exceptions entry "` + filepath.Dir(home) + `" in ` + global + ": home and its parents cannot be exempted",
		`Ignoring sensitive_dir_exceptions entry "relative/x" in ` + global + ": ",
		`Ignoring sensitive_dir_exceptions entry " " in ` + global + ": ",
	} {
		if !slices.ContainsFunc(warnings, func(w string) bool { return strings.HasPrefix(w, want) }) {
			t.Errorf("warnings %q lack %q", warnings, want)
		}
	}
	if len(warnings) != 5 {
		t.Errorf("got %d warnings, want 5: %q", len(warnings), warnings)
	}
	if len(exceptions) != 1 || exceptions[0].Path != filepath.Join(home, ".m2") {
		t.Fatalf("exceptions = %+v, want only ~/.m2", exceptions)
	}

	err := checkSensitiveMounts(guardProject(filepath.Join(home, ".aws")), nil, exceptions, home, false)
	if err == nil || !strings.Contains(err.Error(), "is ~/.aws") {
		t.Fatalf("guard changed by unusable entries: %v", err)
	}
}

// A tool-override entry can only come from global config, so its messages
// name the tool override and the global config file.
func TestConfigSensitiveDirExceptionsNameToolOverride(t *testing.T) {
	home := exceptionHome(t)
	global := globalConfigPath(t)
	opts := model.Options{}
	opts.Tool = "claude"
	opts.SensitiveDirExceptions = []string{"~/.m2", "~"}
	opts.Sources.SensitiveDirExceptions = model.SourceToolOverride

	exceptions, warnings := configSensitiveDirExceptions(opts, home, home)
	wantOrigin := `tool_overrides.claude.sensitive_dir_exceptions entry "~/.m2" in ` + global
	if len(exceptions) != 1 || exceptions[0].Origin != wantOrigin {
		t.Errorf("exceptions = %+v, want one with origin %q", exceptions, wantOrigin)
	}
	wantWarning := `Ignoring tool_overrides.claude.sensitive_dir_exceptions entry "~" in ` + global + ": home and its parents cannot be exempted"
	if !slices.Equal(warnings, []string{wantWarning}) {
		t.Errorf("warnings = %q, want %q", warnings, wantWarning)
	}
}
