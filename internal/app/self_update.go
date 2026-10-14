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
	"time"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/selfupdate"
	"enclave/internal/util"
)

const (
	selfUpdateCheckInterval         = 24 * time.Hour
	selfUpdateCheckTimeout          = 10 * time.Second
	automaticSelfUpdateCheckTimeout = 5 * time.Second
	selfUpdateInstallTimeout        = 10 * time.Minute
)

type selfUpdater interface {
	Supported() error
	Check(ctx context.Context) (selfupdate.Status, error)
	Install(ctx context.Context, latest string) (string, error)
}

// newSelfUpdater returns an updater for the configured update_channel; empty
// selects the channel the binary was published on. It is a variable so tests
// can substitute a fake release.
var newSelfUpdater = func(channel string) selfUpdater { return selfupdate.New(channel) }

// runSelfUpdate dispatches before option resolution, so it applies --verbose
// (or the global verbose setting) itself.
func runSelfUpdate(verbose bool) int {
	global := loadSelfUpdateGlobals()
	if verbose || (global.Verbose != nil && *global.Verbose) {
		logx.SetLevel("debug")
	}
	updater := newSelfUpdater(global.UpdateChannel)
	if err := updater.Supported(); err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	// Shared with automatic updates, so two processes never replace the
	// executable at once.
	release, err := util.AcquireFileLock(config.HostSelfUpdateLockPath(home), func() {
		logx.Infof("Waiting for another enclave update to finish.")
	})
	if err != nil {
		logx.Errorf("Lock enclave update: %v", err)
		return 1
	}
	defer release()
	stamp := config.HostSelfUpdateCheckPath(home)

	checkCtx, cancelCheck := context.WithTimeout(context.Background(), selfUpdateCheckTimeout)
	status, err := updater.Check(checkCtx)
	cancelCheck()
	if err != nil {
		logx.Errorf("Check for enclave update: %v", err)
		return 1
	}
	if !status.UpdateAvailable {
		logx.Infof("Enclave is up to date with the %s channel (%s).", status.Channel, shortBuild(status.Latest))
		markSelfUpdateChecked(stamp)
		return 0
	}

	logx.Infof("Downloading enclave %s build %s.", status.Channel, shortBuild(status.Latest))
	updateCtx, cancelUpdate := context.WithTimeout(context.Background(), selfUpdateInstallTimeout)
	path, err := updater.Install(updateCtx, status.Latest)
	cancelUpdate()
	if err != nil {
		logx.Errorf("Update enclave: %v", err)
		logx.Infof("The updater does not elevate privileges; reinstall enclave manually if %s is not writable.", executablePathForMessage())
		return 1
	}
	markSelfUpdateChecked(stamp)
	logx.Successf("Updated enclave from %s to %s build %s at %s. The new binary will be used on the next invocation.", shortBuild(status.Current), status.Channel, shortBuild(status.Latest), path)
	return 0
}

// loadSelfUpdateGlobals reads the global config alone: self-update runs
// before the project is resolved, and a broken config must not keep a fixed
// binary from being installed.
func loadSelfUpdateGlobals() config.Defaults {
	global, warnings, err := config.LoadGlobalDefaults()
	if err != nil {
		logx.Warnf("Ignoring the global config for this update: %v", err)
		return config.Defaults{}
	}
	for _, warning := range warnings {
		logx.Warnf(warning.Message)
	}
	return global
}

// maybeAutomaticSelfUpdate runs the daily update check before an ordinary
// command. Everything it prints goes to stderr: the command's stdout may be
// redirected or parsed.
func maybeAutomaticSelfUpdate(parsed cli.Result, global config.Defaults) {
	if !automaticSelfUpdateAllowed(parsed) {
		return
	}
	updater := newSelfUpdater(global.UpdateChannel)
	if err := updater.Supported(); err != nil {
		logx.DebugNoticef("Skipping enclave update check: %v", err)
		return
	}
	home, err := config.ResolveHostHome()
	if err != nil {
		logx.DebugNoticef("Skipping enclave update check: %v", err)
		return
	}
	// Skip rather than wait: another process is already checking or updating.
	release, locked, err := util.TryAcquireFileLock(config.HostSelfUpdateLockPath(home))
	if err != nil {
		logx.DebugNoticef("Skipping enclave update check: %v", err)
		return
	}
	if !locked {
		return
	}
	defer release()
	stamp := config.HostSelfUpdateCheckPath(home)
	if !selfUpdateCheckDue(stamp, time.Now()) {
		return
	}
	// Rate-limit failures too: an offline host should not pause on every command.
	if err := writeSelfUpdateStamp(stamp); err != nil {
		logx.DebugNoticef("Failed to record enclave update check: %v", err)
	}

	checkCtx, cancelCheck := context.WithTimeout(context.Background(), automaticSelfUpdateCheckTimeout)
	status, err := updater.Check(checkCtx)
	cancelCheck()
	if errors.Is(err, selfupdate.ErrChannelUnavailable) {
		// A configuration problem, unlike a network failure: say so, once per
		// check interval, instead of finding no updates in silence.
		logx.Warnf("Enclave update check skipped: %v", err)
		return
	}
	if err != nil {
		logx.DebugNoticef("Enclave update check failed: %v", err)
		return
	}
	if !status.UpdateAvailable {
		return
	}
	if global.AutoUpdate == nil || !*global.AutoUpdate {
		logx.Warnf("A different enclave %s build is available (%s; current %s). Run 'enclave self-update' or set \"auto_update\": true in the global config.", status.Channel, shortBuild(status.Latest), shortBuild(status.Current))
		return
	}

	logx.Noticef("Updating enclave to %s build %s.", status.Channel, shortBuild(status.Latest))
	updateCtx, cancelUpdate := context.WithTimeout(context.Background(), selfUpdateInstallTimeout)
	path, err := updater.Install(updateCtx, status.Latest)
	cancelUpdate()
	if err != nil {
		logx.Warnf("Automatic enclave update failed: %v. Run 'enclave self-update' to retry; the updater does not elevate privileges, so %s must be writable.", err, executablePathForMessage())
		return
	}
	logx.Noticef("Updated enclave to %s build %s at %s. The new binary will be used on the next invocation.", status.Channel, shortBuild(status.Latest), path)
}

// automaticSelfUpdateAllowed limits the periodic check to interactive use:
// scripts and integrations neither wait for the network nor get the binary
// swapped between two calls.
func automaticSelfUpdateAllowed(parsed cli.Result) bool {
	if !promptAllowed(parsed) || parsed.NetworkLogView.JSON || parsed.UserCommand != nil || !promptUsable() {
		return false
	}
	switch parsed.Action {
	case "review-target", "validate-extensions":
		return false
	default:
		return true
	}
}

func selfUpdateCheckDue(path string, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return now.Sub(info.ModTime()) >= selfUpdateCheckInterval
}

func markSelfUpdateChecked(stamp string) {
	if err := writeSelfUpdateStamp(stamp); err != nil {
		logx.DebugNoticef("Failed to record enclave update check: %v", err)
	}
}

func writeSelfUpdateStamp(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return util.WriteFileAtomic(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// shortBuild abbreviates a commit identifying a build; versions are kept.
func shortBuild(build string) string {
	if build == "" {
		return "unknown"
	}
	base, dirty := strings.CutSuffix(build, "-dirty")
	if len(base) > 8 && isHex(base) {
		base = base[:8]
	}
	if dirty {
		return base + "-dirty"
	}
	return base
}

func isHex(value string) bool {
	return strings.Trim(strings.ToLower(value), "0123456789abcdef") == ""
}

func executablePathForMessage() string {
	path, err := os.Executable()
	if err != nil {
		return "the enclave executable"
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	return fmt.Sprintf("%q", path)
}
