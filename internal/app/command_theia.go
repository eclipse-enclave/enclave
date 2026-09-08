// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/theia"
)

// runTheia launches the host-installed Theia (or Theia-Next) IDE attached to
// a running enclave container. The single positional CLI argument may be a
// container name or a session name; if omitted, the command auto-selects when
// exactly one managed container is running.
func runTheia(variant theia.Variant, project model.Project, opts model.Options) int {
	if !variant.Valid() {
		logx.Errorf("unsupported theia variant: %q", variant)
		return 1
	}
	if err := checkDocker(); err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	ctx := context.Background()

	be, err := newListingBackend(opts)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	session, err := resolveSessionTarget(ctx, be, sessionTargetQuery{
		Args:    opts.CmdArgs,
		Tool:    sessionTargetTool(opts),
		Project: sessionTargetProject(projectDir),
	})
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	containerName := session.Ref.Name

	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Errorf("resolve home: %v", err)
		return 1
	}

	prefs, err := theia.LoadPreferencesForProject(home, project.Hash, theia.ContainerYoloEnabled(ctx, be, containerName))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	logPath := theia.LogPath(home, containerName)
	if err := theia.Launch(variant, containerName, prefs, logPath); err != nil {
		logx.Errorf("launch %s: %v", variant, err)
		return 1
	}
	logx.Infof("launched %s attached to %s (logs: %s)", variant, containerName, logPath)
	return 0
}
