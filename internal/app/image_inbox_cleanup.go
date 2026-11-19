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
	"enclave/internal/logx"
)

const imageInboxKind = "image inbox"

// orphanedImageInboxes returns the project hashes, sorted, of the inboxes that
// no inbox-enabled session holds. keep names a project whose session is about
// to start, which holds its inbox before its container exists; it may be empty.
func orphanedImageInboxes(inboxes []string, sessions []backend.Session, keep string) []string {
	held := map[string]bool{}
	if keep = strings.TrimSpace(keep); keep != "" {
		held[keep] = true
	}
	for _, session := range sessions {
		if sessionHasImageInbox(session) && !sessionHasEnded(session) {
			held[strings.TrimSpace(session.ProjectHash)] = true
		}
	}
	var orphaned []string
	for _, hash := range inboxes {
		if !held[hash] {
			orphaned = append(orphaned, hash)
		}
	}
	sort.Strings(orphaned)
	return orphaned
}

// sessionHasEnded reports whether a session's container stopped. A stopped but
// unremoved container no longer reads its inbox. Every other state holds it,
// including "created": a starting session's container exists before it runs,
// and its inbox must survive a concurrent sweep. An unknown state holds it too,
// since removing an inbox in use breaks its session's mount.
func sessionHasEnded(session backend.Session) bool {
	switch strings.ToLower(strings.TrimSpace(session.Status)) {
	case "exited", "dead", "stopped", "removing":
		return true
	default:
		return false
	}
}

// planImageInboxSweep returns the image inboxes no inbox-enabled session holds
// any more. Only the Docker and Podman backends run inbox-enabled sessions, so
// another backend's listing cannot decide anything and plans nothing. When the
// sessions cannot be listed, nothing is removable and the error is returned.
func planImageInboxSweep(ctx context.Context, be backend.Backend, home string, keep string) ([]cleanupDir, error) {
	if !backendRunsImageInboxes(be) {
		return nil, nil
	}
	inboxes := listSubdirs(config.HostImageInboxRootDir(home))
	if len(inboxes) == 0 {
		return nil, nil
	}
	sessions, err := be.List(ctx, backend.SessionFilter{All: true})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var dirs []cleanupDir
	for _, hash := range orphanedImageInboxes(inboxes, sessions, keep) {
		dirs = append(dirs, cleanupDir{Kind: imageInboxKind, Path: config.HostImageInboxDir(home, hash)})
	}
	return dirs, nil
}

func backendRunsImageInboxes(be backend.Backend) bool {
	switch be.Name() {
	case backend.NameDocker, backend.NamePodman:
		return true
	default:
		return false
	}
}

// sweepImageInboxes removes the image inboxes no inbox-enabled session holds.
// It is best-effort: the sweep runs alongside other commands, which must not
// fail because of it, and a later sweep retries.
func sweepImageInboxes(ctx context.Context, be backend.Backend, home string, keep string) {
	dirs, err := planImageInboxSweep(ctx, be, home, keep)
	if err != nil {
		logx.Debugf("image inbox sweep skipped: %v", err)
		return
	}
	for _, dir := range dirs {
		logx.Debugf("Removing image inbox without a running session: %s", dir.Path)
	}
	cleanupDirs(dirs)
}

// runHoldingImageInbox runs an inbox-enabled session. Starting one sweeps the
// inboxes of other projects; a foreground session that ends sweeps again, which
// removes its own project's inbox if it was the last session holding it.
func runHoldingImageInbox(ctx context.Context, be backend.Backend, home string, projectHash string, foreground bool, run func() int) int {
	sweepImageInboxes(ctx, be, home, projectHash)
	code := run()
	if foreground {
		sweepImageInboxes(ctx, be, home, "")
	}
	return code
}
