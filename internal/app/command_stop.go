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
	"strings"
	"time"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func runStop(opts model.Options, projectDir string) int {
	if code := requireDocker(); code != 0 {
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

	if group, ok := stopPeerGroupRequested(opts); ok {
		if err := validateStopPeerGroup(opts); err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		code := runStopPeerGroup(opts, group, func(ctx context.Context, container string) error {
			return stopAndRemoveContainer(ctx, be, container)
		})
		if newA2ALeaver(context.Background(), opts).reconcile(context.Background()) && code == 0 {
			code = a2aExitDegraded
		}
		return code
	}

	return stopSessions(context.Background(), be, opts, projectDir)
}

// stopPeerGroupRequested reads --peer-group from the command line only: a
// configured peer_group must not turn every stop into a group stop.
func stopPeerGroupRequested(opts model.Options) (string, bool) {
	group := strings.TrimSpace(opts.PeerGroup)
	return group, group != "" && opts.Sources.PeerGroup == model.SourceCLI
}

// validateStopPeerGroup refuses selectors that would otherwise narrow the stop to sessions.
func validateStopPeerGroup(opts model.Options) error {
	if len(opts.CmdArgs) > 0 {
		return fmt.Errorf("stop --peer-group stops every member of the group; drop the session argument %q", opts.CmdArgs[0])
	}
	if _, given := sessionNameFilter(opts); given {
		return fmt.Errorf("stop --peer-group stops every member of the group; drop --name")
	}
	return nil
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
		return stopContainers(ctx, be, opts, []string{session.Ref.Name})
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

	names := make([]string, 0, len(sessions))
	for _, session := range sessions {
		names = append(names, session.Ref.Name)
	}
	return stopContainers(ctx, be, opts, names)
}

// stopContainers stops and removes containers; peer group members leave
// their group first, and only their groups are reconciled afterwards.
func stopContainers(ctx context.Context, be backend.Backend, opts model.Options, names []string) int {
	leaver := a2aStopLeaver(ctx, be, opts, names)
	if leaver != nil {
		names = orderOrchestratorsFirst(names, leaver.members)
	}
	for _, name := range names {
		leaver.leave(ctx, name)
		stopContainer(be, name)
	}
	if leaver.reconcileLeftGroups(ctx) {
		return a2aExitDegraded
	}
	return 0
}

// a2aStopLeaver is replaced in tests; peer groups exist only on the engine backends.
var a2aStopLeaver = func(ctx context.Context, be backend.Backend, opts model.Options, containers []string) *a2aLeaver {
	if _, ok := be.(*backenddocker.Backend); !ok {
		return nil
	}
	return newA2AStopLeaver(ctx, opts, containers)
}

func stopContainer(be backend.Backend, name string) {
	if err := stopAndRemoveContainer(context.Background(), be, name); err != nil {
		logx.Warnf("%v", err)
	}
}

// stopAndRemoveContainer stops with auth finalization, then removes; a
// failed stop is only logged because the removal still ends the session.
func stopAndRemoveContainer(ctx context.Context, be backend.Backend, name string) error {
	timeout := 10 * time.Second
	logx.Infof("Stopping container: %s", name)
	ref := backend.SessionRef{Name: name}
	if err := be.Stop(ctx, ref, backend.StopOptions{Finalize: true, Timeout: timeout}); err != nil {
		logx.Warnf("Failed to stop/finalize container %s: %v", name, err)
	}
	if remover, ok := be.(backend.UnfinalizedRemover); ok {
		if err := remover.RemoveWithoutFinalize(ctx, ref); err != nil {
			return fmt.Errorf("failed to remove container %s: %w", name, err)
		}
		return nil
	}
	if err := be.Remove(ctx, ref); err != nil {
		return fmt.Errorf("failed to remove container %s: %w", name, err)
	}
	return nil
}
