// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func runAttach(opts model.Options, projectDir string) int {
	if code := requireDocker(); code != 0 {
		return code
	}

	run := opts.RunOptions
	// The CLI puts the detach keys first, so that the optional session argument
	// keeps its "not given" state.
	detachKeys := model.DetachKeysDefault
	args := run.CmdArgs
	if len(args) > 0 {
		if keys := strings.TrimSpace(args[0]); keys != "" {
			detachKeys = keys
		}
		args = args[1:]
	}

	be, err := selectBackend(opts, dockerBackendOptions(model.Host{}, model.Paths{}, model.BuildOptions{}, run))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	ctx := context.Background()
	session, err := resolveSessionTarget(ctx, be, sessionTargetQuery{
		Args:    args,
		Tool:    sessionTargetTool(opts),
		Project: sessionTargetProject(projectDir),
		// Without a name, only detached sessions are picked: sharing the TTY of
		// a foreground session means two terminals fighting over its stdin.
		BackgroundOnly: len(args) == 0,
	})
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	if err := be.Attach(ctx, session.Ref, backend.AttachIO{DetachKeys: detachKeys}); err != nil {
		logx.Errorf("attach: %v", err)
		return 1
	}
	return 0
}
