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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/extinstall"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/selfupdate"
	"enclave/internal/usercmd"
	"enclave/internal/util"
)

const fakeLatestCommit = "1234567890abcdef1234567890abcdef12345678"

type fakeSelfUpdater struct {
	channel     string
	unsupported error
	notWritable error
	upToDate    bool
	checkErr    error
	installErr  error
	// alreadyInstalled makes Install find the executable already replaced.
	alreadyInstalled bool
	// installHook runs inside Install with its context.
	installHook     func(ctx context.Context) error
	checks          int
	installs        int
	installedCommit string
}

func (f *fakeSelfUpdater) Supported() error { return f.unsupported }

func (f *fakeSelfUpdater) Writable(string) error { return f.notWritable }

func (f *fakeSelfUpdater) Check(context.Context) (selfupdate.Status, error) {
	f.checks++
	if f.checkErr != nil {
		return selfupdate.Status{}, f.checkErr
	}
	return selfupdate.Status{Channel: "rolling", Current: "abcdef0", Latest: fakeLatestCommit, UpdateAvailable: !f.upToDate}, nil
}

func (f *fakeSelfUpdater) Install(ctx context.Context, latest string) (string, bool, error) {
	f.installs++
	f.installedCommit = latest
	if f.installHook != nil {
		if err := f.installHook(ctx); err != nil {
			return "", false, err
		}
	}
	if f.installErr != nil {
		return "", false, f.installErr
	}
	return "/opt/enclave/enclave", !f.alreadyInstalled, nil
}

// useFakeSelfUpdater substitutes fake for the rolling release and returns a
// fresh HOME whose state root holds the check stamp and the update lock.
func useFakeSelfUpdater(t *testing.T, fake *fakeSelfUpdater) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	previous, previousPrompt := newSelfUpdater, promptUsable
	newSelfUpdater = func(channel string) selfUpdater {
		fake.channel = channel
		return fake
	}
	promptUsable = func() bool { return true }
	t.Cleanup(func() { newSelfUpdater, promptUsable = previous, previousPrompt })
	return home
}

func autoUpdateDefaults(enabled bool) config.Defaults {
	return config.Defaults{AutoUpdate: &enabled}
}

func stampExists(t *testing.T, home string) bool {
	t.Helper()
	_, err := os.Stat(config.HostSelfUpdateCheckPath(home))
	return err == nil
}

func TestAutomaticSelfUpdateAllowedSkipsMachineOutput(t *testing.T) {
	useFakeSelfUpdater(t, &fakeSelfUpdater{})
	for _, parsed := range []cli.Result{
		{Action: "config", ConfigView: model.ConfigView{JSON: true}},
		{Action: "ps", Options: model.Options{PSOptions: model.PSOptions{PSJSON: true}}},
		{Action: "network-log", NetworkLogView: model.NetworkLogView{JSON: true}},
		{Action: "tools", ExtRequest: &extinstall.Request{JSON: true}},
		{Action: "review-target"},
		{Action: "user-command", UserCommand: &usercmd.Command{Target: usercmd.TargetHost}},
		{Action: "shell", UserCommand: &usercmd.Command{Target: usercmd.TargetSession}},
	} {
		if automaticSelfUpdateAllowed(parsed) {
			t.Errorf("automatic update allowed for machine-oriented action %q", parsed.Action)
		}
	}
	if !automaticSelfUpdateAllowed(cli.Result{Action: "run"}) {
		t.Fatal("automatic update was not allowed for a normal run")
	}
}

func TestAutomaticSelfUpdateSkipsNonInteractiveUse(t *testing.T) {
	fake := &fakeSelfUpdater{}
	home := useFakeSelfUpdater(t, fake)
	promptUsable = func() bool { return false }

	maybeAutomaticSelfUpdate(cli.Result{Action: "ps"}, autoUpdateDefaults(true))
	if fake.checks != 0 || stampExists(t, home) {
		t.Fatalf("checked for updates without a terminal (checks = %d)", fake.checks)
	}
}

func TestSelfUpdateCommandHonorsVerbose(t *testing.T) {
	setRunningAsRoot(t, false)
	t.Cleanup(func() { logx.SetLevel("info") })
	for _, tc := range []struct {
		name   string
		args   []string
		config string
	}{
		{name: "flag", args: []string{"self-update", "--verbose"}},
		{name: "global config", args: []string{"self-update"}, config: `{"verbose":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logx.SetLevel("info")
			fake := &fakeSelfUpdater{upToDate: true}
			home := useFakeSelfUpdater(t, fake)
			if tc.config != "" {
				path, err := config.GlobalConfigPath()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// A directory where the stamp belongs makes recording the check
			// fail, which is reported only at debug level.
			if err := os.MkdirAll(config.HostSelfUpdateCheckPath(home), 0o700); err != nil {
				t.Fatal(err)
			}
			var code int
			_, stderr := captureOutput(t, func() { code = Run(tc.args) })
			if code != 0 || !strings.Contains(stderr, "Failed to record enclave update check") {
				t.Fatalf("code = %d, stderr = %q; want the debug diagnostic", code, stderr)
			}
		})
	}
}

func TestSelfUpdateCheckDue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "self-update-check")
	now := time.Now()
	if !selfUpdateCheckDue(path, now) {
		t.Fatal("missing stamp was not due")
	}
	if err := os.WriteFile(path, []byte("checked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if selfUpdateCheckDue(path, now) {
		t.Fatal("one-hour-old stamp was due")
	}
	if err := os.Chtimes(path, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !selfUpdateCheckDue(path, now) {
		t.Fatal("25-hour-old stamp was not due")
	}
	// A clock that is set back after a check, or a network home whose server
	// clock runs slightly ahead, dates the stamp in the future.
	if err := os.Chtimes(path, now.Add(time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if selfUpdateCheckDue(path, now) {
		t.Fatal("stamp one hour ahead was due")
	}
	if err := os.Chtimes(path, now.Add(30*24*time.Hour), now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !selfUpdateCheckDue(path, now) {
		t.Fatal("stamp 30 days ahead suppressed the check")
	}
}

func TestAutomaticSelfUpdateNotifiesWithoutOptIn(t *testing.T) {
	fake := &fakeSelfUpdater{}
	home := useFakeSelfUpdater(t, fake)

	stdout, stderr := captureOutput(t, func() {
		maybeAutomaticSelfUpdate(cli.Result{Action: "network-print"}, config.Defaults{})
	})
	if fake.installs != 0 {
		t.Fatal("installed an update without auto_update")
	}
	if stdout != "" {
		t.Fatalf("update notice written to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "enclave self-update") {
		t.Fatalf("stderr lacks the update notice: %q", stderr)
	}
	if !stampExists(t, home) {
		t.Fatal("check was not recorded")
	}
}

func TestAutomaticSelfUpdateInstallsWhenOptedIn(t *testing.T) {
	fake := &fakeSelfUpdater{}
	useFakeSelfUpdater(t, fake)

	stdout, stderr := captureOutput(t, func() {
		maybeAutomaticSelfUpdate(cli.Result{Action: "exec"}, autoUpdateDefaults(true))
	})
	if fake.installs != 1 || fake.installedCommit != fakeLatestCommit {
		t.Fatalf("installs = %d of %q, want one of the checked commit", fake.installs, fake.installedCommit)
	}
	if stdout != "" {
		t.Fatalf("update progress written to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "Updated enclave to rolling build 1234567 at") {
		t.Fatalf("stderr lacks the update result: %q", stderr)
	}
}

func TestAutomaticSelfUpdateOffersReinstallForUnwritableInstall(t *testing.T) {
	for _, autoUpdate := range []bool{false, true} {
		t.Run(fmt.Sprintf("auto_update=%v", autoUpdate), func(t *testing.T) {
			fake := &fakeSelfUpdater{notWritable: fmt.Errorf("enclave cannot replace /usr/local/bin/enclave because its directory is %w; reinstall it with: sudo install", selfupdate.ErrNotWritable)}
			useFakeSelfUpdater(t, fake)

			stdout, stderr := captureOutput(t, func() {
				maybeAutomaticSelfUpdate(cli.Result{Action: "run"}, autoUpdateDefaults(autoUpdate))
			})
			if fake.installs != 0 {
				t.Fatal("attempted to replace an executable in an unwritable directory")
			}
			if stdout != "" || !strings.Contains(stderr, "(1234567; current abcdef0), but enclave cannot replace /usr/local/bin/enclave") {
				t.Fatalf("stdout = %q, stderr = %q; want the reinstall notice on stderr", stdout, stderr)
			}
			if strings.Contains(stderr, "enclave self-update") {
				t.Fatalf("suggested a self-update that cannot work: %q", stderr)
			}
		})
	}
}

func TestAutomaticSelfUpdateStaysQuietWhenAnotherUpdateInstalledTheBuild(t *testing.T) {
	fake := &fakeSelfUpdater{alreadyInstalled: true}
	useFakeSelfUpdater(t, fake)

	_, stderr := captureOutput(t, func() {
		maybeAutomaticSelfUpdate(cli.Result{Action: "run"}, autoUpdateDefaults(true))
	})
	if strings.Contains(stderr, "Updated enclave") {
		t.Fatalf("claimed an update that another process made: %q", stderr)
	}
}

func TestAutomaticSelfUpdateFailureStaysOffStdout(t *testing.T) {
	fake := &fakeSelfUpdater{installErr: errors.New("checksum mismatch")}
	useFakeSelfUpdater(t, fake)

	stdout, stderr := captureOutput(t, func() {
		maybeAutomaticSelfUpdate(cli.Result{Action: "network-print"}, autoUpdateDefaults(true))
	})
	if stdout != "" {
		t.Fatalf("update failure written to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "Automatic enclave update failed: checksum mismatch. Run 'enclave self-update' to retry.") {
		t.Fatalf("stderr lacks the failure: %q", stderr)
	}
	if strings.Contains(stderr, "privileges") {
		t.Fatalf("a failure unrelated to permissions blamed them: %q", stderr)
	}
}

func TestAutomaticSelfUpdateRateLimitsFailedChecks(t *testing.T) {
	fake := &fakeSelfUpdater{checkErr: errors.New("offline")}
	home := useFakeSelfUpdater(t, fake)

	for range 2 {
		stdout, stderr := captureOutput(t, func() {
			maybeAutomaticSelfUpdate(cli.Result{Action: "run"}, config.Defaults{})
		})
		if stdout != "" || stderr != "" {
			t.Fatalf("failed check was not silent: stdout=%q stderr=%q", stdout, stderr)
		}
	}
	if fake.checks != 1 {
		t.Fatalf("checks = %d, want 1: a failed check must wait for the next interval", fake.checks)
	}
	if !stampExists(t, home) {
		t.Fatal("failed check was not recorded")
	}
}

func TestAutomaticSelfUpdateSkipsUnsupportedBuilds(t *testing.T) {
	fake := &fakeSelfUpdater{unsupported: errors.New("package build")}
	home := useFakeSelfUpdater(t, fake)

	_, stderr := captureOutput(t, func() {
		maybeAutomaticSelfUpdate(cli.Result{Action: "run"}, autoUpdateDefaults(true))
	})
	if fake.checks != 0 || stampExists(t, home) {
		t.Fatalf("checked an unsupported build (checks = %d)", fake.checks)
	}
	if stderr != "" {
		t.Fatalf("unsupported build was told about updates: %q", stderr)
	}
}

func TestAutomaticSelfUpdateSkipsWhileAnotherProcessHoldsTheLock(t *testing.T) {
	fake := &fakeSelfUpdater{}
	home := useFakeSelfUpdater(t, fake)
	release, locked, err := util.TryAcquireFileLock(config.HostSelfUpdateLockPath(home))
	if err != nil || !locked {
		t.Fatalf("take update lock: locked=%v err=%v", locked, err)
	}
	defer release()

	maybeAutomaticSelfUpdate(cli.Result{Action: "run"}, autoUpdateDefaults(true))
	if fake.checks != 0 || stampExists(t, home) {
		t.Fatalf("checked while another process held the lock (checks = %d)", fake.checks)
	}
}

func TestRunSelfUpdate(t *testing.T) {
	t.Run("unsupported build", func(t *testing.T) {
		fake := &fakeSelfUpdater{unsupported: errors.New("package build")}
		useFakeSelfUpdater(t, fake)
		var code int
		_, stderr := captureOutput(t, func() { code = runSelfUpdate(false) })
		if code != 1 || fake.checks != 0 || !strings.Contains(stderr, "package build") {
			t.Fatalf("code = %d, checks = %d, stderr = %q; want a refusal before checking", code, fake.checks, stderr)
		}
	})
	t.Run("up to date", func(t *testing.T) {
		fake := &fakeSelfUpdater{upToDate: true}
		home := useFakeSelfUpdater(t, fake)
		var code int
		captureOutput(t, func() { code = runSelfUpdate(false) })
		if code != 0 || fake.installs != 0 || !stampExists(t, home) {
			t.Fatalf("code = %d, installs = %d; want a recorded no-op", code, fake.installs)
		}
	})
	t.Run("update", func(t *testing.T) {
		fake := &fakeSelfUpdater{}
		useFakeSelfUpdater(t, fake)
		var code int
		stdout, _ := captureOutput(t, func() { code = runSelfUpdate(false) })
		if code != 0 || fake.installedCommit != fakeLatestCommit {
			t.Fatalf("code = %d, installed %q; want the checked commit", code, fake.installedCommit)
		}
		if !strings.Contains(stdout, "Updated enclave from abcdef0 to rolling build 1234567") {
			t.Fatalf("stdout lacks the update result: %q", stdout)
		}
	})
	t.Run("installed by another update meanwhile", func(t *testing.T) {
		fake := &fakeSelfUpdater{alreadyInstalled: true}
		home := useFakeSelfUpdater(t, fake)
		var code int
		stdout, _ := captureOutput(t, func() { code = runSelfUpdate(false) })
		if code != 0 || !stampExists(t, home) {
			t.Fatalf("code = %d; want a recorded success", code)
		}
		if strings.Contains(stdout, "Updated enclave") || !strings.Contains(stdout, "Enclave is already rolling build 1234567.") {
			t.Fatalf("stdout = %q, want the build already installed", stdout)
		}
	})
	t.Run("unwritable install", func(t *testing.T) {
		fake := &fakeSelfUpdater{notWritable: fmt.Errorf("enclave cannot replace /usr/local/bin/enclave because its directory is %w", selfupdate.ErrNotWritable)}
		useFakeSelfUpdater(t, fake)
		var code int
		_, stderr := captureOutput(t, func() { code = runSelfUpdate(false) })
		if code != 1 || fake.installs != 0 {
			t.Fatalf("code = %d, installs = %d; want a refusal without installing", code, fake.installs)
		}
		if !strings.Contains(stderr, "build is available (1234567), but enclave cannot replace /usr/local/bin/enclave") {
			t.Fatalf("stderr lacks the reinstall notice: %q", stderr)
		}
	})
	t.Run("failure", func(t *testing.T) {
		fake := &fakeSelfUpdater{installErr: errors.New("checksum mismatch")}
		useFakeSelfUpdater(t, fake)
		var code int
		_, stderr := captureOutput(t, func() { code = runSelfUpdate(false) })
		if code != 1 || !strings.Contains(stderr, "Update enclave: checksum mismatch") {
			t.Fatalf("code = %d, stderr = %q; want the failure", code, stderr)
		}
		if strings.Contains(stderr, "privileges") {
			t.Fatalf("a failure unrelated to permissions blamed them: %q", stderr)
		}
	})
}

func TestAutomaticSelfUpdateDebugOutputStaysOffStdout(t *testing.T) {
	logx.SetLevel("debug")
	t.Cleanup(func() { logx.SetLevel("info") })

	for _, tc := range []struct {
		name    string
		fake    *fakeSelfUpdater
		setup   func(t *testing.T, home string)
		wantErr string
	}{
		{name: "unsupported build", fake: &fakeSelfUpdater{unsupported: errors.New("package build")}, wantErr: "package build"},
		{name: "failed check", fake: &fakeSelfUpdater{checkErr: errors.New("offline")}, wantErr: "offline"},
		{
			name: "unwritable state",
			fake: &fakeSelfUpdater{upToDate: true},
			setup: func(t *testing.T, home string) {
				// A directory where the stamp file belongs makes recording fail.
				if err := os.MkdirAll(config.HostSelfUpdateCheckPath(home), 0o700); err != nil {
					t.Fatal(err)
				}
				old := time.Now().Add(-2 * selfUpdateCheckInterval)
				if err := os.Chtimes(config.HostSelfUpdateCheckPath(home), old, old); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "Failed to record enclave update check",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := useFakeSelfUpdater(t, tc.fake)
			if tc.setup != nil {
				tc.setup(t, home)
			}
			stdout, stderr := captureOutput(t, func() {
				maybeAutomaticSelfUpdate(cli.Result{Action: "network-print"}, config.Defaults{})
			})
			if stdout != "" {
				t.Fatalf("debug output written to stdout: %q", stdout)
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, tc.wantErr)
			}
		})
	}
}

func TestAutomaticSelfUpdateUsesConfiguredChannel(t *testing.T) {
	fake := &fakeSelfUpdater{upToDate: true}
	useFakeSelfUpdater(t, fake)

	maybeAutomaticSelfUpdate(cli.Result{Action: "run"}, config.Defaults{UpdateChannel: "stable"})
	if fake.channel != "stable" {
		t.Fatalf("updater channel = %q, want the configured stable channel", fake.channel)
	}
}

func TestRunSelfUpdateReadsChannelFromGlobalConfig(t *testing.T) {
	writeGlobalConfig := func(t *testing.T, content string) {
		t.Helper()
		path, err := config.GlobalConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("configured", func(t *testing.T) {
		fake := &fakeSelfUpdater{upToDate: true}
		useFakeSelfUpdater(t, fake)
		writeGlobalConfig(t, `{"update_channel":"stable"}`)
		captureOutput(t, func() { runSelfUpdate(false) })
		if fake.channel != "stable" {
			t.Fatalf("updater channel = %q, want stable", fake.channel)
		}
	})
	t.Run("broken config falls back to the build channel", func(t *testing.T) {
		fake := &fakeSelfUpdater{upToDate: true}
		useFakeSelfUpdater(t, fake)
		writeGlobalConfig(t, `{"update_channel":`)
		var code int
		_, stderr := captureOutput(t, func() { code = runSelfUpdate(false) })
		if code != 0 || fake.channel != "" {
			t.Fatalf("code = %d, channel = %q; want an update from the build channel", code, fake.channel)
		}
		if !strings.Contains(stderr, "config.json") {
			t.Fatalf("stderr does not name the broken config: %q", stderr)
		}
	})
}
