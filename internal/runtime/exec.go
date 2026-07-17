// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"fmt"
	"strings"

	"enclave/internal/backend"
)

func (r *Runtime) Exec() error {
	containerName, err := r.resolveExecTarget()
	if err != nil {
		return err
	}
	if err := r.ensureRunning(containerName); err != nil {
		return err
	}

	cmdArgs := r.run.CmdArgs
	if len(cmdArgs) == 0 {
		cmdArgs = []string{"/bin/bash"}
	}

	user := ""
	if r.run.Admin {
		user = "root"
	}

	be := r.backend
	if be == nil {
		return fmt.Errorf("runtime backend is not configured")
	}
	execer, ok := be.(backend.Execer)
	if !ok {
		return fmt.Errorf("backend %s does not support exec", be.Name())
	}
	// The backend syncs session credentials after the exec completes, deriving
	// the involved stores from the running container.
	return execer.Exec(context.Background(), backend.SessionRef{Name: containerName}, backend.ExecRequest{Argv: cmdArgs, User: user, TTY: true}, backend.AttachIO{})
}

// resolveExecTarget determines which container to attach to.
// If --name is set, construct the container name directly.
// Otherwise, find running containers for this tool+project and auto-select
// if exactly one exists.
func (r *Runtime) resolveExecTarget() (string, error) {
	if r.run.SessionName != "" {
		return r.containerName(), nil
	}
	if r.backend == nil {
		return "", fmt.Errorf("runtime backend is not configured")
	}

	base := r.baseContainerName()
	prefix := base + "-"
	sessions, err := r.backend.List(context.Background(), backend.SessionFilter{RunningOnly: true, NamePrefix: base})
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}

	type execMatches struct {
		exactMatch   string
		matches      []string
		sessionNames []string
	}
	addMatch := func(result *execMatches, session backend.Session, name string) {
		if name == base {
			result.exactMatch = name
			return
		}
		if !strings.HasPrefix(name, prefix) {
			return
		}
		if isGatewaySidecar(name) {
			return
		}
		sessionLabel := session.Name
		if sessionLabel == "" {
			sessionLabel = name[len(prefix):]
		}
		result.matches = append(result.matches, name)
		result.sessionNames = append(result.sessionNames, sessionLabel)
	}
	hasCandidates := func(result execMatches) bool {
		return result.exactMatch != "" || len(result.matches) > 0
	}

	currentWorktree := strings.TrimSpace(r.project.Dir)
	var preferred execMatches
	var fallback execMatches
	for _, session := range sessions {
		name := strings.TrimPrefix(session.Ref.Name, "/")
		if name == "" {
			continue
		}
		addMatch(&fallback, session, name)
		if strings.TrimSpace(session.Worktree) == currentWorktree {
			addMatch(&preferred, session, name)
		}
	}

	selected := fallback
	if hasCandidates(preferred) {
		selected = preferred
	}
	if selected.exactMatch != "" {
		return selected.exactMatch, nil
	}

	switch len(selected.matches) {
	case 0:
		return "", fmt.Errorf("no running session found for %s (%s)", r.project.Name, r.profile.Name)
	case 1:
		return selected.matches[0], nil
	default:
		return "", fmt.Errorf("multiple sessions running for %s (%s); use --name to specify one: %s",
			r.project.Name, r.profile.Name, strings.Join(selected.sessionNames, ", "))
	}
}

func (r *Runtime) ensureRunning(containerName string) error {
	if r.backend == nil {
		return fmt.Errorf("runtime backend is not configured")
	}
	sessions, err := r.backend.List(context.Background(), backend.SessionFilter{RunningOnly: true, ExactName: containerName})
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return fmt.Errorf("no running container found for %s (%s)", r.project.Name, containerName)
	}
	return nil
}
