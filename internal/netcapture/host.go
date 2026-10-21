// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PrepareSessionDir creates the capture root and the session directory the
// gateway bind-mounts, and returns the latter.
//
// The root is private to the host user. The session directory is world
// writable because the gateway's unprivileged proxy user is another UID than
// the host user under rootful Docker and rootless podman alike, and it must be
// able to create files there while the host user can still delete them. The
// private root keeps both the directory and the files the proxy creates out of
// reach of other host users.
func PrepareSessionDir(root string, session string) (string, error) {
	if !validSessionName(session) {
		return "", fmt.Errorf("invalid capture session name %q", session)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil { // #nosec G302 -- a directory needs its execute bit; owner only.
		return "", err
	}
	dir := SessionDir(root, session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o777); err != nil { // #nosec G302 -- the parent root is 0700; see above.
		return "", err
	}
	return dir, nil
}

// AbandonedAfter is how long a body file without a record must have gone
// unwritten before the host treats it as left behind by a gateway that stopped
// mid-capture. A capture in flight writes its body as data arrives, so only a
// stream silent for that long could be mistaken for one.
const AbandonedAfter = 24 * time.Hour

// PruneStore deletes the oldest captures across every session directory under
// root until the store fits maxBytes. Only whole captures are deleted, and
// directories are left in place: a running gateway keeps writing into the
// directory it bind-mounted, which must therefore survive even when emptied.
// Body files without a record are removed once unwritten for AbandonedAfter,
// and are not counted until then, because they may belong to a capture still
// in flight. The second return is the number of captures removed.
func PruneStore(root string, maxBytes int64) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	type located struct {
		sessionCapture
		dir string
	}
	var captures []located
	var total int64
	cutoff := time.Now().Add(-AbandonedAfter)
	abandoned := func(modified time.Time) bool { return modified.Before(cutoff) }
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		found, err := scanSessionDir(dir, abandoned)
		if err != nil {
			continue
		}
		for _, capture := range found {
			captures = append(captures, located{sessionCapture: capture, dir: dir})
			total += capture.bytes
		}
	}
	sort.SliceStable(captures, func(i, j int) bool {
		return captures[i].modified.Before(captures[j].modified)
	})
	removed := 0
	for _, capture := range captures {
		if maxBytes <= 0 || total <= maxBytes {
			break
		}
		removeCapture(capture.dir, capture.id)
		total -= capture.bytes
		removed++
	}
	return removed, nil
}

// ErrNotFound reports a capture ID that no session directory in scope holds.
var ErrNotFound = errors.New("capture not found")

// Find returns the record path of a capture. Without a session every session
// directory under the roots is searched, which is unambiguous in practice
// because IDs are random.
func Find(roots []string, session string, id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid capture ID %q: expected 16 hex digits", id)
	}
	if session != "" && !validSessionName(session) {
		return "", fmt.Errorf("invalid session name %q", session)
	}
	var matches []string
	for _, root := range roots {
		sessions := []string{session}
		if session == "" {
			sessions = sessionDirs(root)
		}
		for _, name := range sessions {
			path := RecordPath(SessionDir(root, name), id)
			if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
				matches = append(matches, path)
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("capture %s exists in several sessions; pass --session", id)
	}
}

// sessionDirs lists the session directories under root.
func sessionDirs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && validSessionName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names
}

// validSessionName accepts a single, non-hidden path element: sessions are
// named by their container.
func validSessionName(session string) bool {
	return strings.TrimSpace(session) != "" && filepath.Base(session) == session && !strings.HasPrefix(session, ".")
}
