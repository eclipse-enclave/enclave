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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/runtime"
)

func TestRunExecutionRejectsUnknownFeaturesBeforeDocker(t *testing.T) {
	previousPing := dockerPing
	dockerPing = func(context.Context) error {
		t.Fatal("invalid feature selection must fail before contacting Docker")
		return nil
	}
	t.Cleanup(func() { dockerPing = previousPing })
	for _, mode := range []string{"rebuild", "no-rebuild", "slim"} {
		t.Run(mode, func(t *testing.T) {
			opts := config.DefaultOptions()
			opts.Tool = "claude"
			opts.Backend = backend.NameDocker
			opts.Features = []string{"unknown"}
			opts.ForceRebuild = mode == "rebuild"
			opts.NoRebuild = mode == "no-rebuild"
			opts.Slim = mode == "slim"
			sources := model.DefaultOptionSources()
			sources.Features = model.SourceCLI
			input := &CommandInput{
				Ctx: &AppContext{
					Paths:           writeTestBuildPaths(t),
					project:         model.Project{Dir: t.TempDir()},
					projectResolved: true,
				},
				Action:  "run",
				Options: opts,
				Sources: sources,
			}
			var code int
			_, stderr := captureOutput(t, func() { code = runExecutionCommand(input) })
			if code != 1 || !strings.Contains(stderr, "unknown features: unknown") || !strings.Contains(stderr, "enclave features list") {
				t.Fatalf("exit = %d, stderr = %q", code, stderr)
			}
		})
	}
}

func TestExecutionRequiresDocker(t *testing.T) {
	if !executionRequiresDocker("run", model.Options{}) {
		t.Fatal("docker backend should require Docker")
	}
	qemuPrebuilt := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameQEMU},
		BuildOptions: model.BuildOptions{NoRebuild: true, ImageNameSet: true},
	}
	if executionRequiresDocker("run", qemuPrebuilt) {
		t.Fatal("prebuilt qemu bundle run should not require Docker")
	}
	qemuNoRebuild := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameQEMU},
		BuildOptions: model.BuildOptions{NoRebuild: true},
	}
	if executionRequiresDocker("run", qemuNoRebuild) {
		t.Fatal("qemu --no-rebuild reuse run should not require Docker")
	}
	qemuImageName := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameQEMU},
		BuildOptions: model.BuildOptions{ImageNameSet: true},
	}
	if executionRequiresDocker("run", qemuImageName) {
		t.Fatal("qemu --image-name run should not require Docker")
	}
	qemuBuild := model.Options{RunOptions: model.RunOptions{Backend: backend.NameQEMU}}
	if !executionRequiresDocker("run", qemuBuild) {
		t.Fatal("qemu bundle builds should require Docker packaging helper")
	}
	if executionRequiresDocker("exec", qemuBuild) {
		t.Fatal("qemu unsupported exec path should not fail at Docker preflight")
	}
}

func TestAutoBackgroundForIDE(t *testing.T) {
	ideProfile := model.Profile{Name: "theia", PostStart: &model.PostStartActions{OpenIDE: "theia"}}

	cases := []struct {
		name    string
		action  string
		opts    model.Options
		profile model.Profile
		want    bool
	}{
		{"bare run of IDE profile is forced detached", "run", model.Options{}, ideProfile, true},
		{"already background is left alone", "run", model.Options{RunOptions: model.RunOptions{Background: true}}, ideProfile, false},
		{"explicit shell keeps the container shell", "shell", model.Options{RunOptions: model.RunOptions{Shell: true}}, ideProfile, false},
		{"exec is never auto-backgrounded", "exec", model.Options{}, ideProfile, false},
		{"non-IDE profile is untouched", "run", model.Options{}, model.Profile{Name: "claude"}, false},
		{"unsupported open_ide value is untouched", "run", model.Options{}, model.Profile{Name: "x", PostStart: &model.PostStartActions{OpenIDE: "vscode"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoBackgroundForIDE(tc.action, tc.opts, tc.profile); got != tc.want {
				t.Fatalf("autoBackgroundForIDE = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEnsureExistingRuntimeImageWith(t *testing.T) {
	t.Run("returns nil when image exists", func(t *testing.T) {
		err := ensureExistingRuntimeImageWith("enclave:latest", func(context.Context, string) (bool, error) {
			return true, nil
		})
		if err != nil {
			t.Fatalf("ensureExistingRuntimeImageWith returned error: %v", err)
		}
	})

	t.Run("returns actionable error when image is missing", func(t *testing.T) {
		err := ensureExistingRuntimeImageWith("enclave:latest", func(context.Context, string) (bool, error) {
			return false, nil
		})
		if err == nil {
			t.Fatal("expected missing-image error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "does not exist locally") || !strings.Contains(msg, "--no-rebuild") || !strings.Contains(msg, model.EnvNoRebuild) {
			t.Fatalf("unexpected error message: %q", msg)
		}
	})

	t.Run("propagates inspect errors", func(t *testing.T) {
		wantErr := errors.New("inspect failed")
		err := ensureExistingRuntimeImageWith("enclave:latest", func(context.Context, string) (bool, error) {
			return false, wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected wrapped inspect error %v, got %v", wantErr, err)
		}
	})
}

func TestCoordinateRuntimeImageBuildRechecksAfterWaiting(t *testing.T) {
	home := t.TempDir()
	var imageReady atomic.Bool
	var builds atomic.Int32
	var resolves atomic.Int32
	firstBuildStarted := make(chan struct{})
	releaseFirstBuild := make(chan struct{})
	secondResolveStarted := make(chan struct{})

	resolve := func() (runtimeImageBuildPlan, error) {
		if resolves.Add(1) == 2 {
			close(secondResolveStarted)
		}
		return runtimeImageBuildPlan{StructuralRebuild: !imageReady.Load()}, nil
	}
	execute := func(runtimeImageBuildPlan) error {
		if builds.Add(1) == 1 {
			close(firstBuildStarted)
			<-releaseFirstBuild
			imageReady.Store(true)
		}
		return nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs <- coordinateRuntimeImageBuild(home, "enclave-codex:latest", false, resolve, execute)
	}()
	<-firstBuildStarted

	secondStarted := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(secondStarted)
		errs <- coordinateRuntimeImageBuild(home, "enclave-codex:latest", false, resolve, execute)
	}()
	<-secondStarted
	serialized := true
	select {
	case <-secondResolveStarted:
		serialized = false
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirstBuild)
	wg.Wait()
	if !serialized {
		t.Fatal("second caller resolved its build plan while the first build held the lock")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("coordinateRuntimeImageBuild returned error: %v", err)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("build count = %d, want 1", got)
	}
	if got := resolves.Load(); got != 2 {
		t.Fatalf("in-lock build-plan resolution count = %d, want 2", got)
	}
}

func TestCoordinateRuntimeImageBuildPreservesForceRebuild(t *testing.T) {
	home := t.TempDir()
	var builds atomic.Int32
	resolve := func() (runtimeImageBuildPlan, error) {
		return runtimeImageBuildPlan{}, nil
	}
	execute := func(runtimeImageBuildPlan) error {
		builds.Add(1)
		return nil
	}

	for range 2 {
		if err := coordinateRuntimeImageBuild(home, "enclave-codex:latest", true, resolve, execute); err != nil {
			t.Fatalf("coordinateRuntimeImageBuild returned error: %v", err)
		}
	}
	if got := builds.Load(); got != 2 {
		t.Fatalf("build count = %d, want 2", got)
	}
}

// execOptions returns the options of `enclave exec --name <name>` with claude
// as the configured default tool.
func execOptions(name string) model.Options {
	var opts model.Options
	opts.Tool = "claude"
	opts.SessionName = name
	opts.Sources.SessionName = model.SourceCLI
	return opts
}

func TestExecSessionTargetWithoutNameLeavesSelectionToRuntime(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-1", "1", "aaaaaaaaaaaa", "/repo/a"),
	}}
	var opts model.Options
	opts.Tool = "claude"

	got, err := execSessionTarget(context.Background(), be, opts, model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a"})
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got != (runtime.ExecTarget{}) {
		t.Fatalf("execSessionTarget() = %+v, want no target so exec auto-selects", got)
	}
}

func TestExecSessionTargetRejectsBlankName(t *testing.T) {
	for _, name := range []string{"", "  "} {
		be := &stopTestBackend{sessions: []backend.Session{
			session("enclave-claude-aaaaaaaaaaaa-1", "1", "aaaaaaaaaaaa", "/repo/a"),
		}}
		if _, err := execSessionTarget(context.Background(), be, execOptions(name), model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a"}); err == nil {
			t.Fatalf("execSessionTarget(%q) succeeded, want an error instead of auto-selecting", name)
		}
	}
}

// The configured default tool is not a filter: a codex session is reachable
// by name while claude is the default and --tool is not given.
func TestExecSessionTargetDefaultToolIsNotAFilter(t *testing.T) {
	codex := session("enclave-codex-bbbbbbbbbbbb-review", "review", "bbbbbbbbbbbb", "/repo/b")
	codex.Tool = "codex"
	be := &stopTestBackend{sessions: []backend.Session{codex}}

	got, err := execSessionTarget(context.Background(), be, execOptions("review"), model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a"})
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.Name != codex.Ref.Name {
		t.Fatalf("execSessionTarget() = %q, want the codex session", got.Name)
	}
}

// Unlike a bare attach, exec may enter a foreground session; only running
// sessions are candidates.
func TestExecSessionTargetReachesForegroundRunningSessions(t *testing.T) {
	foreground := session("enclave-claude-aaaaaaaaaaaa-work", "work", "aaaaaaaaaaaa", "/repo/a")
	foreground.Background = false
	be := &stopTestBackend{sessions: []backend.Session{foreground}}

	got, err := execSessionTarget(context.Background(), be, execOptions("work"), model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a"})
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.Name != foreground.Ref.Name {
		t.Fatalf("execSessionTarget() = %q, want the foreground session", got.Name)
	}
	if !be.listFilter.RunningOnly || be.listFilter.All {
		t.Fatalf("filter = %+v, want running sessions only", be.listFilter)
	}
}

// The ambient tool is claude, so exec into a codex session paints the codex
// color, as attach does.
func TestExecSessionTargetFollowsSessionTint(t *testing.T) {
	writeGlobalToolTints(t)
	codex := session("enclave-codex-bbbbbbbbbbbb-review", "review", "bbbbbbbbbbbb", t.TempDir())
	codex.Tool = "codex"
	be := &stopTestBackend{sessions: []backend.Session{codex}}
	opts := execOptions("review")
	opts.SessionTint = "#111111"

	got, err := execSessionTarget(context.Background(), be, opts, model.Project{Hash: "cccccccccccc", Dir: "/home/user"})
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.SessionTint != "#222222" {
		t.Fatalf("session tint = %q, want the codex tint", got.SessionTint)
	}
}

// runMounts resolves the additional mounts of a run in home, returning what
// the guard printed.
func runMounts(t *testing.T, home string, project string, opts model.Options) (dirs, readonlyDirs []string, stderr string, err error) {
	t.Helper()
	_, stderr = captureOutput(t, func() {
		dirs, readonlyDirs, err = resolveAdditionalMounts(opts, guardProject(project), home, true)
	})
	return dirs, readonlyDirs, stderr, err
}

// Homeshell passes --add-sensitive-dir for its workstream directories, which
// may be the project itself: that exempts the project without mounting it a
// second time.
func TestResolveAdditionalMountsExemptsProjectThroughFlag(t *testing.T) {
	home := exceptionHome(t)
	workstream := filepath.Join(home, ".local", "share", "homeshell", "workstreams", "p-x")
	link := filepath.Join(home, "code", "p-x")
	if err := os.Symlink(workstream, link); err != nil {
		t.Fatal(err)
	}
	opts := model.Options{}
	opts.AddSensitiveDirs = []string{link}

	dirs, readonlyDirs, stderr, err := runMounts(t, home, workstream, opts)
	if err != nil {
		t.Fatalf("run refused: %v", err)
	}
	if len(dirs) != 0 || len(readonlyDirs) != 0 {
		t.Errorf("mounts = %q read-write, %q read-only; want the project mounted once, as the project", dirs, readonlyDirs)
	}
	if want := "Mounting sensitive host path " + workstream + " (project directory): is inside ~/.local; exempted by --add-sensitive-dir"; !strings.Contains(stderr, want) {
		t.Errorf("warnings %q do not mention %q", stderr, want)
	}
	if strings.Contains(stderr, "Skipping duplicate") {
		t.Errorf("warnings %q report the project as a duplicate", stderr)
	}
}

func TestResolveAdditionalMountsRefusesProjectAsReadonlySensitiveDir(t *testing.T) {
	home := exceptionHome(t)
	workstream := filepath.Join(home, ".local", "share", "homeshell", "workstreams", "p-x")
	opts := model.Options{}
	opts.AddReadonlySensitiveDirs = []string{workstream}

	_, _, _, err := runMounts(t, home, workstream, opts)
	want := "--add-readonly-sensitive-dir " + workstream + " is the project directory; use --add-sensitive-dir " + workstream +
		" to exempt it and --project-mount readonly to mount it read-only"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// The same directory read-write and read-only is mounted once, read-write,
// and the read-only flag still exempts it.
func TestResolveAdditionalMountsKeepsReadWriteOverReadonlySensitiveDir(t *testing.T) {
	home := exceptionHome(t)
	m2 := filepath.Join(home, ".m2")
	cli := model.Options{}
	cli.AddDirs = []string{m2}
	cli.AddReadonlySensitiveDirs = []string{m2}
	cliSources := model.OptionSources{}
	cliSources.AddDirs = model.SourceCLI
	cliSources.AddReadonlySensitiveDirs = model.SourceCLI
	opts, _, _ := config.ResolveOptionsForTool(cli, cliSources, config.Defaults{}, config.Defaults{}, "claude")

	dirs, readonlyDirs, stderr, err := runMounts(t, home, filepath.Join(home, "code", "project"), opts)
	if err != nil {
		t.Fatalf("run refused: %v", err)
	}
	if !slices.Equal(dirs, []string{m2}) || len(readonlyDirs) != 0 {
		t.Errorf("mounts = %q read-write, %q read-only; want %s read-write only", dirs, readonlyDirs, m2)
	}
	if want := "Mounting sensitive host path " + m2 + " (--add-dir): is ~/.m2; exempted by --add-readonly-sensitive-dir"; !strings.Contains(stderr, want) {
		t.Errorf("warnings %q do not mention %q", stderr, want)
	}
}

// Unlike --add-dir, the flags add their mounts to the configured ones.
func TestResolveAdditionalMountsKeepsConfiguredDirsBesideSensitiveDirs(t *testing.T) {
	home := exceptionHome(t)
	configured := filepath.Join(home, "code", "configured")
	flagged := filepath.Join(home, ".m2")
	readonly := filepath.Join(home, "code", "readonly")
	for _, dir := range []string{configured, readonly} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cli := model.Options{}
	cli.AddSensitiveDirs = []string{flagged}
	cliSources := model.OptionSources{}
	cliSources.AddSensitiveDirs = model.SourceCLI
	global := config.Defaults{AddDirs: []string{configured}, AddReadonlyDirs: []string{readonly}}
	opts, _, _ := config.ResolveOptionsForTool(cli, cliSources, global, config.Defaults{}, "claude")

	dirs, readonlyDirs, _, err := runMounts(t, home, filepath.Join(home, "code", "project"), opts)
	if err != nil {
		t.Fatalf("run refused: %v", err)
	}
	if !slices.Equal(dirs, []string{configured, flagged}) || !slices.Equal(readonlyDirs, []string{readonly}) {
		t.Errorf("mounts = %q read-write, %q read-only; want [%s %s] and [%s]", dirs, readonlyDirs, configured, flagged, readonly)
	}
}

// Project config lives outside the repository, so it can exempt paths like
// global config, and the exemption names it.
func TestResolveAdditionalMountsHonorsProjectSensitiveDirExceptions(t *testing.T) {
	home := exceptionHome(t)
	m2 := filepath.Join(home, ".m2")
	project := config.Defaults{AddReadonlyDirs: []string{m2}, SensitiveDirExceptions: []string{"~/.m2"}}
	opts, _, _ := config.ResolveOptionsForTool(model.Options{}, model.OptionSources{}, config.Defaults{}, project, "claude")

	_, readonlyDirs, stderr, err := runMounts(t, home, filepath.Join(home, "code", "project"), opts)
	if err != nil {
		t.Fatalf("run refused: %v", err)
	}
	if !slices.Equal(readonlyDirs, []string{m2}) {
		t.Errorf("read-only mounts = %q, want [%s]", readonlyDirs, m2)
	}
	if want := `is ~/.m2; exempted by sensitive_dir_exceptions entry "~/.m2" in project config`; !strings.Contains(stderr, want) {
		t.Errorf("warnings %q do not mention %q", stderr, want)
	}
}

func TestLoadProfileOrReportNamesUnknownTool(t *testing.T) {
	var err error
	_, errOut := captureOutput(t, func() { _, err = loadProfileOrReport(testPathsWithFeatures(t), "bogus") })
	if err == nil || !strings.Contains(errOut, "unknown tool: bogus") || strings.Contains(errOut, "file does not exist") {
		t.Fatalf("loadProfileOrReport(bogus) = %v, stderr %q; want the tool named", err, errOut)
	}
}
