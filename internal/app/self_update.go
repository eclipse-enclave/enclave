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
	"os/signal"
	"path/filepath"
	"syscall"
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
	Writable(latest string) error
	Check(ctx context.Context) (selfupdate.Status, error)
	Install(ctx context.Context, latest string) (path string, replaced bool, err error)
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
	// Registered only now: while waiting for the lock, Ctrl-C must still end
	// the process at once.
	ctx, stop := interruptibleUpdate()
	defer stop()

	checkCtx, cancelCheck := context.WithTimeout(ctx, selfUpdateCheckTimeout)
	status, err := updater.Check(checkCtx)
	cancelCheck()
	if ctx.Err() != nil {
		logx.Warnf("Enclave update interrupted.")
		return exitCodeInterrupted
	}
	if err != nil {
		logx.Errorf("Check for enclave update: %v", err)
		return 1
	}
	if !status.UpdateAvailable {
		logx.Infof("Enclave is up to date with the %s channel (%s).", status.Channel, selfupdate.ShortBuild(status.Latest))
		markSelfUpdateChecked(stamp)
		return 0
	}
	if err := updater.Writable(status.Latest); err != nil {
		logx.Errorf("A different enclave %s build is available (%s), but %v", status.Channel, selfupdate.ShortBuild(status.Latest), err)
		return 1
	}

	logx.Infof("Updating enclave to %s build %s.", status.Channel, selfupdate.ShortBuild(status.Latest))
	updateCtx, cancelUpdate := context.WithTimeout(ctx, selfUpdateInstallTimeout)
	path, replaced, err := updater.Install(updateCtx, status.Latest)
	cancelUpdate()
	if ctx.Err() != nil {
		logx.Warnf("Enclave update interrupted.")
		return exitCodeInterrupted
	}
	if err != nil {
		logx.Errorf("Update enclave: %v", err)
		return 1
	}
	markSelfUpdateChecked(stamp)
	if !replaced {
		logx.Infof("Enclave is already %s build %s.", status.Channel, selfupdate.ShortBuild(status.Latest))
		return 0
	}
	logx.Successf("Updated enclave from %s to %s build %s at %s. The new binary will be used on the next invocation.", selfupdate.ShortBuild(status.Current), status.Channel, selfupdate.ShortBuild(status.Latest), path)
	return 0
}

// interruptibleUpdate returns a context that SIGINT or SIGTERM cancels. While
// it is active those signals no longer kill the process, so an interrupted
// download returns through Install, which removes its temporary file.
func interruptibleUpdate() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
// redirected or parsed. It reports whether SIGINT or SIGTERM interrupted the
// check or download, in which case the command must not run.
func maybeAutomaticSelfUpdate(parsed cli.Result, global config.Defaults) (interrupted bool) {
	if !automaticSelfUpdateAllowed(parsed) {
		return false
	}
	updater := newSelfUpdater(global.UpdateChannel)
	if err := updater.Supported(); err != nil {
		logx.DebugNoticef("Skipping enclave update check: %v", err)
		return false
	}
	home, err := config.ResolveHostHome()
	if err != nil {
		logx.DebugNoticef("Skipping enclave update check: %v", err)
		return false
	}
	// Skip rather than wait: another process is already checking or updating.
	release, locked, err := util.TryAcquireFileLock(config.HostSelfUpdateLockPath(home))
	if err != nil {
		logx.DebugNoticef("Skipping enclave update check: %v", err)
		return false
	}
	if !locked {
		return false
	}
	defer release()
	stamp := config.HostSelfUpdateCheckPath(home)
	if !selfUpdateCheckDue(stamp, time.Now()) {
		return false
	}
	// Rate-limit failures too: an offline host should not pause on every command.
	if err := writeSelfUpdateStamp(stamp); err != nil {
		logx.DebugNoticef("Failed to record enclave update check: %v", err)
	}

	ctx, stop := interruptibleUpdate()
	defer stop()
	checkCtx, cancelCheck := context.WithTimeout(ctx, automaticSelfUpdateCheckTimeout)
	status, err := updater.Check(checkCtx)
	cancelCheck()
	if ctx.Err() != nil {
		logx.Warnf("Enclave update interrupted.")
		return true
	}
	if err != nil {
		logx.DebugNoticef("Enclave update check failed: %v", err)
		return false
	}
	if !status.UpdateAvailable {
		return false
	}
	if err := updater.Writable(status.Latest); err != nil {
		logx.Warnf("A different enclave %s build is available (%s; current %s), but %v", status.Channel, selfupdate.ShortBuild(status.Latest), selfupdate.ShortBuild(status.Current), err)
		return false
	}
	if global.AutoUpdate == nil || !*global.AutoUpdate {
		logx.Warnf("A different enclave %s build is available (%s; current %s). Run 'enclave self-update' or set \"auto_update\": true in the global config.", status.Channel, selfupdate.ShortBuild(status.Latest), selfupdate.ShortBuild(status.Current))
		return false
	}

	logx.Noticef("Updating enclave to %s build %s.", status.Channel, selfupdate.ShortBuild(status.Latest))
	updateCtx, cancelUpdate := context.WithTimeout(ctx, selfUpdateInstallTimeout)
	path, replaced, err := updater.Install(updateCtx, status.Latest)
	cancelUpdate()
	if ctx.Err() != nil {
		logx.Warnf("Enclave update interrupted.")
		return true
	}
	if errors.Is(err, selfupdate.ErrNotWritable) {
		logx.Warnf("Automatic enclave update failed: %v", err)
		return false
	}
	if err != nil {
		logx.Warnf("Automatic enclave update failed: %v. Run 'enclave self-update' to retry.", err)
		return false
	}
	if replaced {
		logx.Noticef("Updated enclave to %s build %s at %s. The new binary will be used on the next invocation.", status.Channel, selfupdate.ShortBuild(status.Latest), path)
	}
	return false
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

// selfUpdateCheckDue also counts a stamp dated an interval or more in the
// future as due: a clock that was once far ahead (a restored VM, RTC drift)
// must not suppress checks until it catches up, while the skew of a network
// home's server clock stays below that.
func selfUpdateCheckDue(path string, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	age := now.Sub(info.ModTime())
	return age >= selfUpdateCheckInterval || age <= -selfUpdateCheckInterval
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
