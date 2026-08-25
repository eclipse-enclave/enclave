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
	"sort"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

// sessionTargetQuery describes how a positional container/session argument is
// resolved to exactly one managed session.
type sessionTargetQuery struct {
	// Requested is the user-provided name: either a full container name or a
	// session name as passed to `--name`. Empty means "auto-select".
	Requested string
	// Tool restricts candidates to one tool. Only set it when the tool was
	// chosen explicitly on the command line; the resolved default would
	// otherwise hide sessions of other tools.
	Tool string
	// Project is the project resolved from the working directory. Its sessions
	// win over sessions of other projects that carry the same session name. A
	// zero value disables the preference.
	Project model.Project
	// IncludeStopped also considers stopped sessions (used by `stop`, which
	// removes leftovers).
	IncludeStopped bool
}

// resolveSessionTarget resolves a positional container/session argument to one
// running (or, with IncludeStopped, existing) session. A full container name
// matches verbatim; anything else is treated as a session name and matched
// against the sanitized session names of the candidates, preferring the
// current project. Ambiguity is reported rather than guessed.
func resolveSessionTarget(ctx context.Context, be backend.Backend, q sessionTargetQuery) (backend.Session, error) {
	filter := backend.SessionFilter{Tool: strings.TrimSpace(q.Tool)}
	if q.IncludeStopped {
		filter.All = true
	} else {
		filter.RunningOnly = true
	}
	sessions, err := be.List(ctx, filter)
	if err != nil {
		return backend.Session{}, fmt.Errorf("list containers: %w", err)
	}

	requested := strings.TrimSpace(q.Requested)
	if requested == "" {
		return selectSingleSession(scopeSessions(sessions, q.Project), sessions, "")
	}

	for _, session := range sessions {
		if session.Ref.Name == requested {
			return session, nil
		}
	}

	wanted := model.SanitizeSessionName(requested)
	if wanted == "" {
		return backend.Session{}, fmt.Errorf("invalid session name %q", requested)
	}
	var matches []backend.Session
	for _, session := range sessions {
		if model.SanitizeSessionName(session.Name) == wanted {
			matches = append(matches, session)
		}
	}
	return selectSingleSession(scopeSessions(matches, q.Project), matches, requested)
}

// scopeSessions narrows candidates to the current project: sessions started
// from the very same worktree first, then any session of the project. It
// returns the input unchanged when nothing matches, so that a name pointing at
// another project still resolves when it is unambiguous.
func scopeSessions(sessions []backend.Session, project model.Project) []backend.Session {
	worktree := strings.TrimSpace(project.Dir)
	realDir := strings.TrimSpace(project.RealDir)
	if worktree != "" || realDir != "" {
		var sameWorktree []backend.Session
		for _, session := range sessions {
			if worktree != "" && strings.TrimSpace(session.Worktree) == worktree {
				sameWorktree = append(sameWorktree, session)
				continue
			}
			if realDir != "" && strings.TrimSpace(session.ProjectDir) == realDir {
				sameWorktree = append(sameWorktree, session)
			}
		}
		if len(sameWorktree) > 0 {
			return sameWorktree
		}
	}
	if hash := strings.TrimSpace(project.Hash); hash != "" {
		var sameProject []backend.Session
		for _, session := range sessions {
			if strings.TrimSpace(session.ProjectHash) == hash {
				sameProject = append(sameProject, session)
			}
		}
		if len(sameProject) > 0 {
			return sameProject
		}
	}
	return sessions
}

// selectSingleSession picks the only candidate, or explains what to pass
// instead. all is the unscoped candidate set, used to point at the sessions of
// other projects that the scoping ruled out.
func selectSingleSession(scoped []backend.Session, all []backend.Session, requested string) (backend.Session, error) {
	switch len(scoped) {
	case 1:
		return scoped[0], nil
	case 0:
		if requested == "" {
			return backend.Session{}, fmt.Errorf("no running enclave containers (start one first)")
		}
		return backend.Session{}, fmt.Errorf("no enclave session named %q (use `%s ps` to list sessions)", requested, model.AppName)
	default:
		subject := "multiple running enclave containers"
		if requested != "" {
			subject = fmt.Sprintf("session name %q matches multiple containers", requested)
		}
		return backend.Session{}, fmt.Errorf("%s; pass one of these container names explicitly:\n  %s",
			subject, strings.Join(describeSessions(scoped), "\n  "))
	}
}

func describeSessions(sessions []backend.Session) []string {
	described := make([]string, 0, len(sessions))
	for _, session := range sessions {
		described = append(described, fmt.Sprintf("%s (%s)", session.Ref.Name,
			directoryDisplayName(session.Worktree, session.ProjectHash)))
	}
	sort.Strings(described)
	return described
}

// sessionTargetTool returns the tool filter for session resolution: the tool
// only counts when it was requested on the command line, since the resolved
// default would otherwise exclude sessions of every other tool.
func sessionTargetTool(opts model.Options) string {
	if opts.Sources.Tool == model.SourceCLI {
		return strings.TrimSpace(opts.Tool)
	}
	return ""
}

// sessionTargetProject resolves the project for scoping. Resolution failures
// are not fatal: the caller then resolves names across all projects.
func sessionTargetProject(projectDir string) model.Project {
	if strings.TrimSpace(projectDir) == "" {
		return model.Project{}
	}
	project, err := config.ResolveProjectFromDir(projectDir)
	if err != nil {
		return model.Project{}
	}
	return project
}
