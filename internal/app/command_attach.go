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
	if err := be.Attach(context.Background(), backend.SessionRef{Name: containerName}, backend.AttachIO{DetachKeys: detachKeys}); err != nil {
		logx.Errorf("attach: %v", err)
		return 1
	}
	return 0
}
