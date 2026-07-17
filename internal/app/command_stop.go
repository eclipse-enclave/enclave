// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func runStop(run model.RunOptions) int {
	if code := requireDocker(); code != 0 {
		return code
	}

	host, hostErr := resolveHost()
	if hostErr != nil {
		logx.Warnf("Failed to resolve host for auth finalization: %v", hostErr)
	}
	paths, pathsErr := config.ResolvePaths()
	if pathsErr != nil {
		logx.Warnf("Failed to resolve auth finalization assets: %v", pathsErr)
	}

	be, err := selectBackend(model.Options{RunOptions: run}, dockerBackendOptions(host, paths, model.BuildOptions{}, run))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	if len(run.CmdArgs) > 0 && run.CmdArgs[0] != "" {
		stopContainer(be, run.CmdArgs[0])
		return 0
	}

	background := true
	sessions, err := be.List(context.Background(), backend.SessionFilter{All: true, Background: &background, Tool: run.Tool, SessionName: run.SessionName})
	if err != nil {
		logx.Errorf("Failed to list background sessions: %v", err)
		return 1
	}

	if len(sessions) == 0 {
		logx.Infof("No background containers found")
		return 0
	}

	for _, session := range sessions {
		stopContainer(be, session.Ref.Name)
	}
	return 0
}

func stopContainer(be backend.Backend, name string) {
	timeout := 10 * time.Second
	logx.Infof("Stopping container: %s", name)
	ref := backend.SessionRef{Name: name}
	if err := be.Stop(context.Background(), ref, backend.StopOptions{Finalize: true, Timeout: timeout}); err != nil {
		logx.Warnf("Failed to stop/finalize container %s: %v", name, err)
	}
	if remover, ok := be.(backend.UnfinalizedRemover); ok {
		if err := remover.RemoveWithoutFinalize(context.Background(), ref); err != nil {
			logx.Warnf("Failed to remove container %s: %v", name, err)
		}
		return
	}
	if err := be.Remove(context.Background(), ref); err != nil {
		logx.Warnf("Failed to remove container %s: %v", name, err)
	}
}
