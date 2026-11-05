// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/util"
)

// QEMUControlTag is the 9p tag of the control share every Enclave guest has;
// admission recognizes Enclave's QEMU processes by it.
const QEMUControlTag = "enclave-control"

// AcquireQEMULease registers preparation, guest lifetime and finalization before
// releasing cross-engine admission. The QEMU child must inherit the returned
// descriptor; close it only after the child exits and host copy-back finishes.
// Each run has its own lease, so ordinary QEMU/Docker sessions remain concurrent.
func AcquireQEMULease(home string) (*os.File, error) {
	if home == "" {
		return nil, errors.New("QEMU auth admission requires a host home")
	}
	release, err := AcquireAuthMountAdmission(home)
	if err != nil {
		return nil, err
	}
	defer release()
	root := filepath.Dir(config.HostLockPath(home, "auth-mount-admission.lock"))
	file, err := os.CreateTemp(root, "qemu-*.lease")
	if err != nil {
		return nil, err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		return nil, err
	}
	lease, err := util.AcquireFileLease(name)
	if err != nil {
		_ = os.Remove(name) // #nosec G703 -- name comes from os.CreateTemp inside the trusted host lock directory.
	}
	return lease, err
}

// RequireNoQEMUSessions conservatively excludes protected identity transitions
// while any QEMU guest is preparing, running or finalizing. Unlike containers,
// QEMU has no reliable inspect API for its current host-share set.
func RequireNoQEMUSessions(ctx context.Context, home string) error {
	root := filepath.Dir(config.HostLockPath(home, "auth-mount-admission.lock"))
	if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), root, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "qemu-") || !strings.HasSuffix(entry.Name(), ".lease") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("invalid QEMU auth admission lease")
		}
		path := filepath.Join(root, entry.Name())
		release, _, err := util.TryAcquireFileLock(path)
		if err != nil {
			return err
		}
		if release == nil {
			return errors.New("stop QEMU sessions before changing protected authentication")
		}
		// An unlocked lease outlived its guest, such as after a crash.
		_ = os.Remove(path) // #nosec G703 -- path is a lease name listed in the trusted host lock directory.
		release()
	}
	// Also cover guests launched before lease tracking was introduced. Only
	// Enclave guests share the enclave-control tag; other QEMU users, such as
	// Docker Desktop, podman machine or Colima, do not block admission. Both
	// Linux and macOS ps list the invoking user's full command lines with -x
	// and -ww.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-x", "-ww", "-o", "args=").Output()
	if err != nil {
		return fmt.Errorf("cannot verify existing QEMU sessions: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 0 && strings.HasPrefix(filepath.Base(fields[0]), "qemu-system-") && strings.Contains(line, "mount_tag="+QEMUControlTag) {
			return errors.New("stop existing QEMU processes before changing protected authentication")
		}
	}
	return nil
}
