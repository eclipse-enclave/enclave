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

// containerIDMinPrefix is the shortest container-ID prefix accepted in place of
// a name. Anything shorter is treated as a session name only, so that short
// hex-looking session names keep working.
const containerIDMinPrefix = 8

// containerIDFullLen is the length of a full container ID. Recorded IDs are
// truncated, so only a value of exactly this length may extend one; anything
// between is a prefix or nothing at all.
const containerIDFullLen = 64

// sessionTargetQuery describes how a positional container/session argument is
// resolved to exactly one managed session.
type sessionTargetQuery struct {
	// Args are the command's positional arguments. The first one selects the
	// session: a container name, a container ID, or a session name as passed to
	// `--name`. No argument at all means "auto-select".
	Args []string
	// Tool restricts session-name matches and auto-selection to one tool. Only
	// set it when the tool was chosen explicitly on the command line; the
	// resolved default would otherwise hide sessions of other tools.
	Tool string
	// Project is the project resolved from the working directory. Its sessions
	// win over sessions of other projects that carry the same session name, and
	// auto-selection never leaves it. A zero value disables both.
	Project model.Project
	// IncludeStopped also considers stopped sessions (used by `stop`, which
	// removes leftovers).
	IncludeStopped bool
	// ProjectScopedNames rejects a session name that only matches outside the
	// current project. `stop` sets it: removal is destructive and the
	// auto-assigned names `1`, `2`, … collide across projects by construction,
	// so another project's session needs its container name or ID. It requires
	// Project to be set; without it no name resolves.
	ProjectScopedNames bool
	// BackgroundOnly restricts auto-selection to detached sessions, so that a
	// bare `attach` cannot grab the TTY of a foreground session that a second
	// terminal is already driving.
	BackgroundOnly bool
}

// resolveSessionTarget resolves a positional container/session argument to one
// running (or, with IncludeStopped, existing) session. A container name or ID
// matches verbatim; anything else is matched against the session names of the
// candidates in sanitized form, preferring (and with ProjectScopedNames
// requiring) the current project. Ambiguity is reported rather than guessed.
func resolveSessionTarget(ctx context.Context, be backend.Backend, q sessionTargetQuery) (backend.Session, error) {
	requested, err := requestedSession(q.Args)
	if err != nil {
		return backend.Session{}, err
	}

	filter := backend.SessionFilter{}
	if q.IncludeStopped {
		filter.All = true
	} else {
		filter.RunningOnly = true
	}
	// The tool is applied to the candidates below rather than to the listing:
	// a container named or identified verbatim is resolved whatever its tool.
	sessions, err := be.List(ctx, filter)
	if err != nil {
		return backend.Session{}, fmt.Errorf("list containers: %w", err)
	}

	if requested == "" {
		return autoSelectSession(sessions, q)
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
	for _, session := range sessionsForTool(sessions, q.Tool) {
		if model.SanitizeSessionName(session.Name) == wanted {
			matches = append(matches, session)
		}
	}
	if len(matches) == 0 {
		if byID := sessionsByContainerID(sessions, requested); len(byID) > 0 {
			return selectSingleSession(byID, "container ID", requested)
		}
		return backend.Session{}, fmt.Errorf("no enclave session named %q (use `%s ps` to list sessions)", requested, model.AppName)
	}
	scoped, inProject := projectSessions(matches, q.Project)
	if q.ProjectScopedNames && !inProject {
		return backend.Session{}, fmt.Errorf("no enclave session named %q in this project; pass a container name or ID from `%s ps` to reach a session of another project",
			requested, model.AppName)
	}
	return selectSingleSession(scoped, "session name", requested)
}

// requestedSession picks the session argument out of a command's positional
// arguments. An argument that is present but blank is rejected rather than
// treated as "auto-select", so an unset variable in `enclave stop "$SESSION"`
// cannot remove a session nobody named.
func requestedSession(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	requested := strings.TrimSpace(args[0])
	if requested == "" {
		return "", fmt.Errorf("empty session name; pass a session or container name from `%s ps`", model.AppName)
	}
	return requested, nil
}

// autoSelectSession resolves the no-argument form. Unlike an explicit name it
// never leaves the current project: picking up another project's agent because
// this one has no session would be a surprising place to land.
func autoSelectSession(sessions []backend.Session, q sessionTargetQuery) (backend.Session, error) {
	candidates := sessionsForTool(sessions, q.Tool)
	if q.BackgroundOnly {
		var background []backend.Session
		for _, session := range candidates {
			if session.Background {
				background = append(background, session)
			}
		}
		candidates = background
	}
	if strings.TrimSpace(q.Project.Hash) != "" {
		scoped, ok := projectSessions(candidates, q.Project)
		if !ok {
			scoped = nil
		}
		candidates = scoped
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		if q.BackgroundOnly {
			return backend.Session{}, fmt.Errorf("no running background session for this project; name one explicitly (`%s ps`) or enter a foreground session with `%s exec`",
				model.AppName, model.AppName)
		}
		return backend.Session{}, fmt.Errorf("no running enclave containers for this project (start one first)")
	default:
		return backend.Session{}, fmt.Errorf("multiple running enclave containers; pass one of these container names explicitly:\n  %s",
			strings.Join(describeSessions(candidates), "\n  "))
	}
}

// projectSessions narrows candidates to the current project: sessions started
// from the very same worktree first, then any session of the project. ok
// reports whether the project matched anything, so that callers can decide
// between falling back to all candidates and reporting nothing.
func projectSessions(sessions []backend.Session, project model.Project) (scoped []backend.Session, ok bool) {
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
			return sameWorktree, true
		}
	}
	if sameProject := projectHashSessions(sessions, project.Hash); len(sameProject) > 0 {
		return sameProject, true
	}
	return sessions, false
}

// projectHashSessions narrows sessions to one project. An unresolvable project
// (empty hash) matches nothing, never everything, so that a name-filtered
// command cannot silently widen to the whole host.
func projectHashSessions(sessions []backend.Session, hash string) []backend.Session {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return nil
	}
	var matches []backend.Session
	for _, session := range sessions {
		if strings.TrimSpace(session.ProjectHash) == hash {
			matches = append(matches, session)
		}
	}
	return matches
}

func sessionsForTool(sessions []backend.Session, tool string) []backend.Session {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return sessions
	}
	var matches []backend.Session
	for _, session := range sessions {
		if strings.EqualFold(strings.TrimSpace(session.Tool), tool) {
			matches = append(matches, session)
		}
	}
	return matches
}

// sessionsByContainerID collects the sessions matching a container ID (or a
// prefix of one, as printed by `docker ps`), which `attach` and `stop` accepted
// before names were resolved. Session names win, so this only runs when none
// matched. All matches are returned so that an ambiguous prefix is reported
// instead of resolved by listing order.
func sessionsByContainerID(sessions []backend.Session, requested string) []backend.Session {
	if len(requested) < containerIDMinPrefix || len(requested) > containerIDFullLen || !isHexString(requested) {
		return nil
	}
	full := len(requested) == containerIDFullLen
	var matches []backend.Session
	for _, session := range sessions {
		id := strings.TrimSpace(session.Ref.ID)
		if id == "" {
			continue
		}
		// Session IDs are truncated, so a full ID pasted from docker carries the
		// one recorded here as its prefix.
		if strings.HasPrefix(id, requested) || (full && strings.HasPrefix(requested, id)) {
			matches = append(matches, session)
		}
	}
	return matches
}

func isHexString(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return value != ""
}

func selectSingleSession(matches []backend.Session, kind string, requested string) (backend.Session, error) {
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return backend.Session{}, fmt.Errorf("no enclave session named %q (use `%s ps` to list sessions)", requested, model.AppName)
	default:
		return backend.Session{}, fmt.Errorf("%s %q matches multiple containers; pass one of these container names explicitly:\n  %s",
			kind, requested, strings.Join(describeSessions(matches), "\n  "))
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

// sessionsMatchingName filters sessions by a user-provided `--name` value.
// Both sides are sanitized, so `--name "My Task"` and `--name my-task` select
// the same sessions regardless of the form recorded in the session label. A
// name that sanitizes to nothing matches nothing, never everything.
func sessionsMatchingName(sessions []backend.Session, requested string) []backend.Session {
	wanted := model.SanitizeSessionName(requested)
	if wanted == "" {
		return nil
	}
	matches := make([]backend.Session, 0, len(sessions))
	for _, session := range sessions {
		if model.SanitizeSessionName(session.Name) == wanted {
			matches = append(matches, session)
		}
	}
	return matches
}

// sessionNameFilter returns the `--name` value of a listing or stop command.
// given separates an omitted flag from an explicitly blank one: the latter has
// to match nothing rather than everything. The value is passed through raw,
// since matching sanitizes it together with the recorded session names.
func sessionNameFilter(opts model.Options) (name string, given bool) {
	if opts.Sources.SessionName != model.SourceCLI {
		return "", false
	}
	return strings.TrimSpace(opts.SessionName), true
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
