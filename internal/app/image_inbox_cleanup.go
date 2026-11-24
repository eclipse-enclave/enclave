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
	"os"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/util"
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

// planImageInboxSweep returns the image inboxes of the backend's engine that no
// inbox-enabled session holds any more. It never judges another engine's
// inboxes, whose sessions it cannot list. Only the Docker and Podman backends
// run inbox-enabled sessions, so another backend plans nothing. When the
// sessions cannot be listed, nothing is removable and the error is returned.
func planImageInboxSweep(ctx context.Context, be backend.Backend, home string, keep string) ([]cleanupDir, error) {
	if !backendRunsImageInboxes(be) {
		return nil, nil
	}
	inboxes := listSubdirs(config.HostImageInboxEngineDir(home, be.Name()))
	if len(inboxes) == 0 {
		return nil, nil
	}
	sessions, err := be.List(ctx, backend.SessionFilter{All: true})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var dirs []cleanupDir
	for _, hash := range orphanedImageInboxes(inboxes, sessions, keep) {
		dirs = append(dirs, cleanupDir{Kind: imageInboxKind, Path: config.HostImageInboxDir(home, be.Name(), hash)})
	}
	return dirs, nil
}

func backendRunsImageInboxes(be backend.Backend) bool {
	return isImageInboxEngine(be.Name())
}

func isImageInboxEngine(name string) bool {
	switch name {
	case backend.NameDocker, backend.NamePodman:
		return true
	default:
		return false
	}
}

// strayImageInboxEntries returns the entries under the image inbox root that
// are not an engine's inbox directory, such as inboxes from before inboxes
// were kept per engine. No session mounts them.
func strayImageInboxEntries(home string) []cleanupDir {
	root := config.HostImageInboxRootDir(home)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []cleanupDir
	for _, entry := range entries {
		if entry.IsDir() && isImageInboxEngine(entry.Name()) {
			continue
		}
		dirs = append(dirs, cleanupDir{Kind: imageInboxKind, Path: filepath.Join(root, entry.Name())})
	}
	return dirs
}

// sweepImageInboxes removes the image inboxes no inbox-enabled session holds.
// It holds the image inbox lock, which a starting session holds from creating
// its inbox until its container exists, so that a starting session counts.
func sweepImageInboxes(ctx context.Context, be backend.Backend, home string, keep string) error {
	release, err := util.AcquireFileLock(config.HostImageInboxLockPath(home), nil)
	if err != nil {
		return fmt.Errorf("acquire image inbox lock: %w", err)
	}
	defer release()
	dirs, err := planImageInboxSweep(ctx, be, home, keep)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		logx.Debugf("Removing image inbox without a running session: %s", dir.Path)
	}
	cleanupDirs(dirs)
	return nil
}

// sweepImageInboxesQuietly sweeps alongside another command, which must not
// fail because of it; a later sweep retries.
func sweepImageInboxesQuietly(ctx context.Context, be backend.Backend, home string, keep string) {
	if err := sweepImageInboxes(ctx, be, home, keep); err != nil {
		logx.Debugf("image inbox sweep skipped: %v", err)
	}
}

// runHoldingImageInbox runs an inbox-enabled session. Starting one sweeps the
// inboxes of other projects; a foreground session that ends sweeps again, which
// removes its own project's inbox if it was the last session holding it.
func runHoldingImageInbox(ctx context.Context, be backend.Backend, home string, projectHash string, foreground bool, run func() int) int {
	sweepImageInboxesQuietly(ctx, be, home, projectHash)
	code := run()
	if foreground {
		sweepImageInboxesQuietly(ctx, be, home, "")
	}
	return code
}
