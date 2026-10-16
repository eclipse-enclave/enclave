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
	"enclave/internal/model"
)

// ExecTarget is the session that `exec --name` resolved to.
// The zero value selects the running session of
// this tool and project.
type ExecTarget struct {
	Name string
}

func (r *Runtime) Exec(target ExecTarget) error {
	containerName := target.Name
	if containerName == "" {
		var err error
		containerName, err = r.resolveExecTarget()
		if err != nil {
			return err
		}
	}
	session, err := r.runningSession(containerName)
	if err != nil {
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
	// An exec occupies the terminal with an interactive TTY into the session, so
	// it is marked like a session that owns the terminal from the start.
	sessionTarget := model.TerminalTarget{Name: containerName, ID: session.Ref.ID, Tool: session.Tool, ProjectDir: session.ProjectDir, ProjectHash: session.ProjectHash}
	finish := r.beginTerminal(context.Background(), sessionTarget)
	var execErr error
	defer func() { finish(execErr) }()
	// The backend syncs session credentials after the exec completes, deriving
	// the involved stores from the running container.
	execErr = execer.Exec(context.Background(), backend.SessionRef{Name: containerName}, backend.ExecRequest{Argv: cmdArgs, User: user, TTY: true}, backend.AttachIO{})
	return execErr
}

// resolveExecTarget finds the running containers for this tool and project
// and auto-selects one if it is unambiguous.
func (r *Runtime) resolveExecTarget() (string, error) {
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

func (r *Runtime) runningSession(containerName string) (backend.Session, error) {
	if r.backend == nil {
		return backend.Session{}, fmt.Errorf("runtime backend is not configured")
	}
	sessions, err := r.backend.List(context.Background(), backend.SessionFilter{RunningOnly: true, ExactName: containerName})
	if err != nil {
		return backend.Session{}, err
	}
	if len(sessions) == 0 {
		return backend.Session{}, fmt.Errorf("no running container found for %s (%s)", r.project.Name, containerName)
	}
	return sessions[0], nil
}
