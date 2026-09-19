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
	"time"

	"enclave/internal/buildinfo"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/selfupdate"
	"enclave/internal/util"
)

const selfUpdateCheckInterval = 24 * time.Hour

func runSelfUpdate() int {
	service := selfupdate.New()
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), 10*time.Second)
	status, err := service.Check(checkCtx, buildinfo.Read())
	cancelCheck()
	if err != nil {
		logx.Errorf("Check for enclave update: %v", err)
		return 1
	}
	if !status.UpdateAvailable {
		logx.Infof("Enclave is up to date at commit %s.", shortCommit(status.LatestCommit))
		markSelfUpdateChecked()
		return 0
	}

	updateCtx, cancelUpdate := context.WithTimeout(context.Background(), 2*time.Minute)
	path, err := service.Install(updateCtx)
	cancelUpdate()
	if err != nil {
		logx.Errorf("Update enclave: %v", err)
		logx.Infof("The updater does not elevate privileges; reinstall the rolling binary manually if %s is not writable.", executablePathForMessage())
		return 1
	}
	markSelfUpdateChecked()
	logx.Successf("Updated enclave from commit %s to %s at %s. The new binary will be used on the next invocation.", shortCommit(status.CurrentCommit), shortCommit(status.LatestCommit), path)
	return 0
}

func maybeAutomaticSelfUpdate(parsed cli.Result, global config.Defaults) {
	if !automaticSelfUpdateAllowed(parsed) {
		return
	}
	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Debugf("Skipping enclave update check: %v", err)
		return
	}
	stamp := config.HostSelfUpdateCheckPath(home)
	if !selfUpdateCheckDue(stamp, time.Now()) {
		return
	}
	// Rate-limit failures too: an offline host should not pause on every command.
	if err := writeSelfUpdateStamp(stamp); err != nil {
		logx.Debugf("Failed to record enclave update check: %v", err)
	}

	service := selfupdate.New()
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), 5*time.Second)
	status, err := service.Check(checkCtx, buildinfo.Read())
	cancelCheck()
	if err != nil {
		logx.Debugf("Enclave update check failed: %v", err)
		return
	}
	if !status.UpdateAvailable {
		return
	}
	if global.AutoUpdate == nil || !*global.AutoUpdate {
		logx.Warnf("A different enclave rolling binary is available (%s; current %s). Run 'enclave self-update' or set \"auto_update\": true in the global config.", shortCommit(status.LatestCommit), shortCommit(status.CurrentCommit))
		return
	}

	updateCtx, cancelUpdate := context.WithTimeout(context.Background(), 2*time.Minute)
	path, err := service.Install(updateCtx)
	cancelUpdate()
	if err != nil {
		logx.Warnf("Automatic enclave update failed: %v", err)
		logx.Infof("Run 'enclave self-update' after making %s writable, or reinstall the rolling binary manually.", executablePathForMessage())
		return
	}
	logx.Successf("Updated enclave to rolling commit %s at %s. The new binary will be used on the next invocation.", shortCommit(status.LatestCommit), path)
}

func automaticSelfUpdateAllowed(parsed cli.Result) bool {
	if !promptAllowed(parsed) || parsed.NetworkLogView.JSON {
		return false
	}
	switch parsed.Action {
	case "review-target", "self-update", "user-command", "validate-extensions":
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

func markSelfUpdateChecked() {
	home, err := config.ResolveHostHome()
	if err != nil {
		return
	}
	if err := writeSelfUpdateStamp(config.HostSelfUpdateCheckPath(home)); err != nil {
		logx.Debugf("Failed to record enclave update check: %v", err)
	}
}

func writeSelfUpdateStamp(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return util.WriteFileAtomic(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	if commit == "" {
		return "unknown"
	}
	return commit
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
