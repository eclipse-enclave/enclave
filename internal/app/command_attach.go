// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/termtint"
)

func runAttach(run model.RunOptions) int {
	if code := requireDocker(); code != 0 {
		return code
	}

	containerName := run.CmdArgs[0]
	detachKeys := model.DetachKeysDefault
	if len(run.CmdArgs) > 1 && run.CmdArgs[1] != "" {
		detachKeys = run.CmdArgs[1]
	}

	be, err := selectBackend(model.Options{RunOptions: run}, dockerBackendOptions(model.Host{}, model.Paths{}, model.BuildOptions{}, run))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	restoreTint := termtint.Begin(attachSessionTint(be, containerName, run.SessionTint))
	defer restoreTint()
	if err := be.Attach(context.Background(), backend.SessionRef{Name: containerName}, backend.AttachIO{DetachKeys: detachKeys}); err != nil {
		logx.Errorf("attach: %v", err)
		return 1
	}
	return 0
}

// attachSessionTint resolves session_tint for the session being attached rather
// than for the ambient tool and cwd project, so attaching a codex session from
// a shell whose default tool is claude paints the codex color. Warnings from
// re-reading the config layers stay at debug level because the run path already
// reports them. The fallback covers a session that cannot be inspected and
// containers labeled by an older enclave that recorded no project dir.
func attachSessionTint(be backend.Backend, containerName string, fallback string) string {
	session, err := be.Inspect(context.Background(), backend.SessionRef{Name: containerName})
	if err != nil || session == nil {
		return fallback
	}
	if session.Tool == "" || session.ProjectDir == "" {
		return fallback
	}
	global, project, warnings, err := config.LoadDefaults(session.ProjectDir)
	if err != nil {
		logx.Debugf("resolve session_tint for %s: %v", containerName, err)
		return fallback
	}
	for _, warning := range warnings {
		logx.Debugf("%s", warning)
	}
	opts, _, _ := config.ResolveOptionsForTool(model.Options{}, model.OptionSources{}, global, project, session.Tool)
	return opts.SessionTint
}
