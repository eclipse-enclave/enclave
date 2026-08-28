// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	stagingPrefix  = ".incoming-"
	replacedPrefix = ".replaced-"
	// staleAge bounds how long a leftover staging directory is kept. An
	// unlocked sweep has no race-free liveness signal, so mtime age decides;
	// touch keeps a live run's directory looking alive.
	staleAge = 24 * time.Hour
)

// replacedSwapSeq keeps the ".replaced-<name>-<pid>-<seq>" directory unique
// across repeated commits of the same name within one process, where the pid
// alone does not distinguish them.
var replacedSwapSeq atomic.Uint64

// staging is a per-run directory holding extensions after sanitization and
// before the atomic swap. It sits inside the destination directory so the final
// rename never crosses a filesystem boundary.
type staging struct {
	kindDir string
	root    string
	kind    model.ExtensionKind
}

// newStaging opens a staging directory for one run. A dry run stages outside
// the destination instead of inside it: it never commits, so it needs neither
// the same-filesystem rename nor the destination to exist, and creating that
// directory would be exactly the touch a dry run promises not to make.
func newStaging(env Env, kind model.ExtensionKind, dryRun bool) (*staging, error) {
	kindDir := env.kindDir(kind)
	if strings.TrimSpace(kindDir) == "" {
		return nil, fmt.Errorf("no user extension directory is configured")
	}
	parent := ""
	if !dryRun {
		if err := os.MkdirAll(kindDir, 0o750); err != nil {
			return nil, err
		}
		sweepStale(kindDir, env.now())
		parent = kindDir
	}

	root, err := os.MkdirTemp(parent, stagingPrefix)
	if err != nil {
		return nil, err
	}
	return &staging{kindDir: kindDir, root: root, kind: kind}, nil
}

// sweepStale removes staging and replaced directories left behind by an
// interrupted run. Dot-prefixed names are invisible to extension listing and
// validation, so a leftover is inert until swept.
func sweepStale(kindDir string, now time.Time) {
	entries, err := os.ReadDir(kindDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(name, stagingPrefix) && !strings.HasPrefix(name, replacedPrefix) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || now.Sub(info.ModTime()) < staleAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(kindDir, name))
	}
}

func (s *staging) extDir(name string) string { return filepath.Join(s.root, name) }

// finalPath is where name lands once committed.
func (s *staging) finalPath(name string) string { return filepath.Join(s.kindDir, name) }

// discard drops one extension's staged copy so an --all run does not keep every
// failed or declined candidate on disk until it ends. A committed copy has
// already been renamed away, so this is a no-op for it.
func (s *staging) discard(name string) {
	dir := s.extDir(name)
	if util.PathStrictlyWithin(s.root, dir) {
		_ = os.RemoveAll(dir)
	}
}

// touch refreshes the staging directory's own modification time, which is the
// only thing sweepStale stats, so a long-running install keeps looking live.
func (s *staging) touch(env Env) {
	t := env.now()
	_ = os.Chtimes(s.root, t, t)
}

func (s *staging) close() {
	if s.root != "" {
		_ = os.RemoveAll(s.root)
	}
}

// commit swaps the staged extension into its final location under a host lock:
// any existing directory is moved aside, the staged one renamed into place,
// stamp (which may be nil) runs while the lock is still held, and only then is
// the old directory deleted. A failure mid-swap, or a failing stamp, restores
// what was there. stamp writes by path, so it has to run inside the lock: a
// concurrent install of the same name could otherwise swap in its own content
// first and receive this run's metadata.
func (s *staging) commit(env Env, name string, stamp func(installedPath string) error) (string, error) {
	final := s.finalPath(name)
	if !util.PathStrictlyWithin(s.kindDir, final) {
		return "", fmt.Errorf("refusing to install outside %s", s.kindDir)
	}
	staged := s.extDir(name)
	replaced := filepath.Join(s.kindDir, fmt.Sprintf("%s%s-%d-%d", replacedPrefix, name, os.Getpid(), replacedSwapSeq.Add(1)))

	err := util.WithFileLock(env.lockPath(), func() error {
		hadExisting := false
		if _, statErr := os.Stat(final); statErr == nil {
			if renameErr := os.Rename(final, replaced); renameErr != nil {
				return renameErr
			}
			hadExisting = true
		}
		if renameErr := os.Rename(staged, final); renameErr != nil {
			if hadExisting {
				_ = os.Rename(replaced, final)
			}
			return renameErr
		}
		if stamp != nil {
			if stampErr := stamp(final); stampErr != nil {
				// Unwind the swap so a stamp failure leaves the same state a
				// rename failure would: nothing new visible under name.
				_ = os.Rename(final, staged)
				if hadExisting {
					_ = os.Rename(replaced, final)
				}
				return stampErr
			}
		}
		if hadExisting {
			_ = os.RemoveAll(replaced)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return final, nil
}

// removeInstalled deletes an installed extension directory under the same lock.
func removeInstalled(env Env, kind model.ExtensionKind, name string) error {
	kindDir := env.kindDir(kind)
	target := filepath.Join(kindDir, name)
	if !util.PathStrictlyWithin(kindDir, target) {
		return fmt.Errorf("refusing to remove %s: outside %s", target, kindDir)
	}
	return util.WithFileLock(env.lockPath(), func() error { return os.RemoveAll(target) })
}
