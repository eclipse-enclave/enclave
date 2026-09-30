// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/usercmd"
)

func TestBuildEnvironmentSessionCommands(t *testing.T) {
	t.Setenv(model.EnvNoRebuild, "1")
	commands := []usercmd.Command{
		{Name: "triage", Target: usercmd.TargetSession, Path: "/commands/session/triage"},
	}
	for _, args := range [][]string{
		nil, {"run"}, {"shell"}, {"continue"}, {"resume"},
		{"devcontainer", "run"}, {"devcontainer", "shell"},
		{"--backend", "qemu"}, {"--backend", "podman"}, {"triage"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			parsed, err := cli.Parse(args, config.DefaultOptions(), commands...)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.UserCommand != nil {
				prepareUserSessionCommand(&parsed, t.TempDir())
			}
			applyBuildEnvironment(&parsed)
			opts, _, _ := config.ResolveOptionsForTool(parsed.Options, parsed.Sources, config.Defaults{}, config.Defaults{}, "codex")
			if !opts.NoRebuild {
				t.Fatal("session lost environment-sourced rebuild suppression")
			}
			if !dockerBackendOptions(model.Host{}, model.Paths{}, opts.BuildOptions, opts.RunOptions).NoRebuild {
				t.Fatal("gateway backend lost rebuild suppression")
			}
			if opts.Backend == "qemu" && executionRequiresDocker(parsed.Action, opts) {
				t.Fatal("QEMU reuse must not require Docker")
			}
		})
	}
}

func TestBuildEnvironmentIgnoresNonBuildingCommands(t *testing.T) {
	t.Setenv(model.EnvNoRebuild, "1")
	commands := []usercmd.Command{{Name: "deploy", Target: usercmd.TargetHost, Path: "/commands/host/deploy"}}
	for _, args := range [][]string{
		{"ps"}, {"status"}, {"exec"}, {"attach"}, {"stop"},
		{"network", "status"}, {"network", "apply"}, {"network", "print"},
		{"info"}, {"devcontainer", "generate"},
		{"tools", "update", "codex", "--yes"}, {"features", "update", "--yes"},
		{"theia"}, {"deploy"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			parsed, err := cli.Parse(args, config.DefaultOptions(), commands...)
			if err != nil {
				t.Fatal(err)
			}
			applyBuildEnvironment(&parsed)
			if parsed.Options.NoRebuild {
				t.Fatal("non-building command must ignore rebuild environment")
			}
		})
	}
}

func TestBuildEnvironmentReportedByConfig(t *testing.T) {
	t.Setenv(model.EnvNoRebuild, "1")
	parsed, err := cli.Parse([]string{"config"}, config.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	applyBuildEnvironment(&parsed)
	if !parsed.Options.NoRebuild {
		t.Fatal("config must report environment-sourced rebuild suppression")
	}
}

func TestBuildEnvironmentDisabled(t *testing.T) {
	t.Setenv(model.EnvNoRebuild, "0")
	for _, args := range [][]string{nil, {"--no-rebuild"}, {"--rebuild"}, {"update"}} {
		parsed, err := cli.Parse(args, config.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		before := parsed.Options.NoRebuild
		applyBuildEnvironment(&parsed)
		if parsed.Options.NoRebuild != before {
			t.Fatalf("disabled environment changed --no-rebuild for %v", args)
		}
	}
}

func TestRebuildSuppressionCause(t *testing.T) {
	var cliOpts model.Options
	cliOpts.Sources.NoRebuild = model.SourceCLI
	for _, tc := range []struct {
		env  string
		opts model.Options
		want string
	}{
		{"0", cliOpts, "--no-rebuild"},
		{"1", model.Options{}, model.EnvNoRebuild},
		{"1", cliOpts, "--no-rebuild and " + model.EnvNoRebuild},
	} {
		t.Setenv(model.EnvNoRebuild, tc.env)
		if got := rebuildSuppressionCause(tc.opts); got != tc.want {
			t.Errorf("env=%s: got %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestBuildEnvironmentUsesExistingConflictChecks(t *testing.T) {
	t.Setenv(model.EnvNoRebuild, "1")
	for _, args := range [][]string{{"update"}, {"update", "codex"}, {"--rebuild"}, {"shell", "--rebuild"}} {
		parsed, err := cli.Parse(args, config.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		applyBuildEnvironment(&parsed)
		if parsed.Action == "update" {
			// A bare context ensures rejection precedes any image build.
			if code := runUpdate(&CommandInput{Options: parsed.Options}); code == 0 {
				t.Fatalf("update allowed a build with suppression enabled: %v", args)
			}
		} else if err := validateBuildControlConflicts(parsed.Options); err == nil || !strings.Contains(err.Error(), model.EnvNoRebuild) {
			t.Fatalf("%v: expected an environment-aware rebuild conflict, got %v", args, err)
		}
	}
}

func TestRunVersionWithoutWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not allow removing the current working directory")
	}

	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	deletedDir := filepath.Join(t.TempDir(), "deleted")
	if err := os.Mkdir(deletedDir, 0o755); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	if err := os.Chdir(deletedDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	if err := os.Remove(deletedDir); err != nil {
		t.Fatalf("remove working directory: %v", err)
	}

	out := captureStdout(t, func() {
		if code := Run([]string{"version"}); code != 0 {
			t.Fatalf("Run(version) returned %d", code)
		}
	})
	if !strings.HasPrefix(out, "enclave: ") || strings.Count(out, "\n") != 1 {
		t.Fatalf("unexpected version output %q", out)
	}

	flagOut := captureStdout(t, func() {
		if code := Run([]string{"--version"}); code != 0 {
			t.Fatalf("Run(--version) returned %d", code)
		}
	})
	if flagOut != out {
		t.Fatalf("--version output %q differs from version output %q", flagOut, out)
	}

	out = captureStdout(t, func() {
		if code := Run([]string{"version", "--json"}); code != 0 {
			t.Fatalf("Run(version --json) returned %d", code)
		}
	})
	var version struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"date"`
	}
	if err := json.Unmarshal([]byte(out), &version); err != nil {
		t.Fatalf("decode version JSON %q: %v", out, err)
	}
	if version.Version == "" || version.Commit == "" || version.Date == "" {
		t.Fatalf("incomplete version JSON: %+v", version)
	}
}
