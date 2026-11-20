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

func runStop(opts model.Options, projectDir string) int {
	if code := requireSessionEngine(opts); code != 0 {
		return code
	}

	run := opts.RunOptions

	host, hostErr := resolveHost()
	if hostErr != nil {
		logx.Warnf("Failed to resolve host for auth finalization: %v", hostErr)
	}
	paths, pathsErr := config.ResolvePaths()
	if pathsErr != nil {
		logx.Warnf("Failed to resolve auth finalization assets: %v", pathsErr)
	}

	be, err := selectBackend(opts, dockerBackendOptions(host, paths, model.BuildOptions{}, run))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	return stopSessions(context.Background(), be, opts, projectDir)
}

// stopSessions removes the containers a `stop` invocation selects: the single
// session named by the positional argument, or every background session of the
// tool, narrowed to the current project's matching sessions when `--name` was
// given.
func stopSessions(ctx context.Context, be backend.Backend, opts model.Options, projectDir string) int {
	run := opts.RunOptions

	if len(run.CmdArgs) > 0 {
		session, err := resolveSessionTarget(ctx, be, sessionTargetQuery{
			Args:               run.CmdArgs,
			Tool:               sessionTargetTool(opts),
			Project:            sessionTargetProject(projectDir),
			IncludeStopped:     true,
			ProjectScopedNames: true,
		})
		if err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		if !stopContainer(be, session.Ref) {
			return 1
		}
		return 0
	}

	background := true
	sessions, err := be.List(ctx, backend.SessionFilter{All: true, Background: &background, Tool: run.Tool})
	if err != nil {
		logx.Errorf("Failed to list background sessions: %v", err)
		return 1
	}
	// Sanitized matching happens here rather than as a label filter, so that a
	// name which sanitizes to nothing stops nothing instead of everything. Like
	// the positional form, `--name` stays inside the current project: session
	// names are project-relative and collide across projects.
	if name, given := sessionNameFilter(opts); given {
		project := sessionTargetProject(projectDir)
		sessions = sessionsMatchingName(projectHashSessions(sessions, project.Hash), name)
	}

	if len(sessions) == 0 {
		logx.Infof("No background containers found")
		return 0
	}

	code := 0
	for _, session := range sessions {
		if !stopContainer(be, session.Ref) {
			code = 1
		}
	}
	return code
}

// stopContainer stops, finalizes and removes one session. It reports failure
// only for a backend that ends a session in one operation: there an error
// means the session still runs or still owes its finalization, and `stop`
// must not exit as if it were done. For the container engines a failure stays
// the warning it has always been.
func stopContainer(be backend.Backend, selected backend.SessionRef) bool {
	timeout := 10 * time.Second
	name := selected.Name
	logx.Infof("Stopping container: %s", name)
	if ender, ok := be.(backend.SessionEnder); ok {
		// The whole reference, ID included: the session that was selected,
		// not whichever one has its name by the time this runs.
		if err := ender.EndSession(context.Background(), selected, backend.StopOptions{Finalize: true, Timeout: timeout}); err != nil {
			logx.Errorf("Failed to stop/finalize container %s: %v", name, err)
			return false
		}
		return true
	}
	ref := backend.SessionRef{Name: name}
	if err := be.Stop(context.Background(), ref, backend.StopOptions{Finalize: true, Timeout: timeout}); err != nil {
		logx.Warnf("Failed to stop/finalize container %s: %v", name, err)
	}
	if remover, ok := be.(backend.UnfinalizedRemover); ok {
		if err := remover.RemoveWithoutFinalize(context.Background(), ref); err != nil {
			logx.Warnf("Failed to remove container %s: %v", name, err)
		}
		return true
	}
	if err := be.Remove(context.Background(), ref); err != nil {
		logx.Warnf("Failed to remove container %s: %v", name, err)
	}
	return true
}
