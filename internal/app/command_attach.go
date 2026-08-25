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
	"enclave/internal/logx"
	"enclave/internal/model"
)

func runAttach(opts model.Options, projectDir string) int {
	if code := requireDocker(); code != 0 {
		return code
	}

	run := opts.RunOptions
	requested := ""
	if len(run.CmdArgs) > 0 {
		requested = run.CmdArgs[0]
	}
	detachKeys := model.DetachKeysDefault
	if len(run.CmdArgs) > 1 && run.CmdArgs[1] != "" {
		detachKeys = run.CmdArgs[1]
	}

	be, err := selectBackend(opts, dockerBackendOptions(model.Host{}, model.Paths{}, model.BuildOptions{}, run))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	ctx := context.Background()
	session, err := resolveSessionTarget(ctx, be, sessionTargetQuery{
		Requested: requested,
		Tool:      sessionTargetTool(opts),
		Project:   sessionTargetProject(projectDir),
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
