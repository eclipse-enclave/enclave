// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	diskFile     = "disk.qcow2"
	diskMetaFile = "disk.json"
	diskLockFile = "disk.lock"
)

// diskMeta records which base a project disk was created on. The qcow2
// header holds the backing file itself; this is for telling the user when the
// disk no longer matches the base a new one would get.
type diskMeta struct {
	BaseKey   string `json:"baseKey"`
	Base      string `json:"base"`
	CreatedAt string `json:"createdAt"`
}

// errDiskInUse reports a project disk that a VM, or the enclave process of
// its session, holds.
var errDiskInUse = errors.New("in use by another session")

// AdmitSession claims the persistent disk of the session's project and tool
// for the VM this process is about to start. Two VMs writing one qcow2 would
// corrupt it, so the claim is on the disk, whatever the sessions are called,
// and it is made before the runtime touches the stores the running session
// uses.
//
// The claim is a lock file. QEMU inherits it, so the disk stays reserved for
// as long as the VM runs, also when the enclave process that started it is
// gone.
//
// A start is refused while an earlier session of the project still owes a
// finalization that could not be settled: the new session would work on host
// files whose pending changes are still waiting.
func (b *Backend) AdmitSession(_ context.Context, session backend.SessionMeta) error {
	dir, err := b.diskDir(session)
	if err != nil {
		return err
	}
	if b.reservation != nil && b.reservedDir == dir {
		return nil
	}
	b.releaseReservation()
	return b.locked(session.ProjectHash, session.Tool, func() error {
		lock, err := b.claimDisk(dir, session.Tool)
		if err != nil {
			return err
		}
		// With the disk in hand no earlier VM of this project runs. What such
		// a session still owes is settled before this one prepares the same
		// stores.
		problems := b.finalizeLeftovers(session.ProjectHash, session.Tool)
		unsettled, err := b.unsettled(session.ProjectHash, session.Tool)
		if err != nil {
			_ = lock.Close()
			return fmt.Errorf("vm backend: cannot tell whether an earlier session of this project still owes a finalization: %w", err)
		}
		if len(unsettled) > 0 {
			_ = lock.Close()
			name := unsettled[0].Name
			return errors.Join(
				fmt.Errorf("vm backend: the earlier session %s of this project is not finalized; settle it with `%s stop --backend %s %s`, then start again", name, model.AppName, backend.NameVM, name),
				problems)
		}
		if problems != nil {
			logx.Warnf("%v", problems)
		}
		b.reservation, b.reservedDir = lock, dir
		return nil
	})
}

// locked runs fn under the lock of a project and tool's sessions. Admitting a
// start and every change to a session record (publishing, finalizing,
// removing) happen under it, so no two processes interleave them.
func (b *Backend) locked(projectHash string, tool string, fn func() error) error {
	name := "vm-sessions-" + util.HashString(projectHash+"\x00"+tool) + ".lock"
	return util.WithFileLock(config.HostLockPath(b.home(), name), fn)
}

func (b *Backend) diskDir(session backend.SessionMeta) (string, error) {
	if strings.TrimSpace(session.ProjectHash) == "" || strings.TrimSpace(session.Tool) == "" {
		return "", fmt.Errorf("vm backend: session has no project or tool to keep a disk for")
	}
	return config.HostProjectVMDir(b.home(), session.ProjectHash, session.Tool), nil
}

// claimDisk takes the lock of a project disk, or fails with errDiskInUse.
// It runs under the guard, so that a cleanup that found the disk idle cannot
// delete it while the claim is being made.
func (b *Backend) claimDisk(dir string, tool string) (*os.File, error) {
	var lock *os.File
	err := util.WithFileLock(diskGuardPath(b.home()), func() error {
		claimed, ok, err := util.TryLockFile(filepath.Join(dir, diskLockFile))
		if err != nil {
			return fmt.Errorf("vm backend: reserve disk: %w", err)
		}
		if !ok {
			return fmt.Errorf("vm backend: the %s disk of this project is %w; stop that session first", tool, errDiskInUse)
		}
		lock = claimed
		return nil
	})
	return lock, err
}

// releaseReservation gives up this process's hold on the disk. A VM that
// inherited the lock keeps the disk reserved.
func (b *Backend) releaseReservation() {
	if b.reservation != nil {
		_ = b.reservation.Close()
		b.reservation, b.reservedDir = nil, ""
	}
}

func (b *Backend) ensureDisk(ctx context.Context, dir string, bs base) (string, error) {
	disk := filepath.Join(dir, diskFile)
	metaPath := filepath.Join(dir, diskMetaFile)
	if _, err := os.Stat(disk); err == nil {
		meta, err := readDiskMeta(dir)
		if err != nil {
			return "", fmt.Errorf("vm backend: disk %s has no readable %s; remove %s to start with a new disk", disk, diskMetaFile, dir)
		}
		if _, err := os.Stat(meta.Base); err != nil {
			return "", fmt.Errorf("vm backend: disk %s needs its base image %s, which is gone; remove %s to start with a new disk", disk, meta.Base, dir)
		}
		if meta.BaseKey != bs.Key {
			// A disk cannot move to another base: its blocks are differences
			// to the one it was created on.
			logx.Noticef("vm disk %s was created on an earlier base image and keeps it; remove %s to start with a new disk on the current base.", disk, dir)
		}
		return disk, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("vm backend: inspect disk: %w", err)
	}
	// A leftover of an interrupted creation; the reservation is ours.
	staging := disk + ".tmp"
	_ = os.Remove(staging)
	if out, err := b.host.Output(ctx, qemuImgBinary, "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", bs.Disk, staging); err != nil {
		_ = os.Remove(staging)
		return "", fmt.Errorf("vm backend: create disk: %s: %w", strings.TrimSpace(string(out)), err)
	}
	meta, err := json.MarshalIndent(diskMeta{BaseKey: bs.Key, Base: bs.Disk, CreatedAt: time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := util.WriteFileAtomic(metaPath, append(meta, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("vm backend: write disk metadata: %w", err)
	}
	if err := os.Rename(staging, disk); err != nil {
		return "", fmt.Errorf("vm backend: publish disk: %w", err)
	}
	logx.Infof("Created vm disk %s", disk)
	return disk, nil
}

// DiskBase returns the base image directory and key of the disk a project
// and tool already have. ok is false without a usable disk, including one
// whose base is gone; starting a session reports that.
func DiskBase(home string, projectHash string, tool string) (baseDir string, baseKey string, ok bool) {
	dir := config.HostProjectVMDir(home, projectHash, tool)
	meta, err := readDiskMeta(dir)
	if err != nil {
		return "", "", false
	}
	if _, err := os.Stat(filepath.Join(dir, diskFile)); err != nil {
		return "", "", false
	}
	if _, err := os.Stat(meta.Base); err != nil {
		return "", "", false
	}
	return filepath.Dir(meta.Base), meta.BaseKey, true
}

func readDiskMeta(dir string) (diskMeta, error) {
	var meta diskMeta
	data, err := os.ReadFile(filepath.Join(dir, diskMetaFile)) // #nosec G304 -- path is inside the enclave-managed project state directory.
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

// diskGuardPath is the lock that orders claiming a disk against removing
// disks. It lives outside every directory a cleanup removes.
func diskGuardPath(home string) string {
	return config.HostLockPath(home, "vm-disks.lock")
}

// RemoveUnlessDiskInUse runs remove, which deletes the given directories,
// unless a running VM holds a disk inside one of them: the VM would keep
// writing to a deleted file and lose the guest's state. No VM can claim a
// disk while remove runs.
func RemoveUnlessDiskInUse(home string, dirs []string, remove func()) error {
	return util.WithFileLock(diskGuardPath(home), func() error {
		inUse, err := disksInUse(home)
		if err != nil {
			return fmt.Errorf("check for running vm sessions: %w", err)
		}
		for _, disk := range inUse {
			for _, dir := range dirs {
				if util.PathWithin(dir, disk) {
					return fmt.Errorf("a running vm session uses the disk in %s; stop it before cleaning up", disk)
				}
			}
		}
		remove()
		return nil
	})
}

// disksInUse returns the project disk directories a running VM holds. The
// answer only stays true under the disk guard.
func disksInUse(home string) ([]string, error) {
	locks, err := filepath.Glob(filepath.Join(config.HostProjectsDir(home), "*", "*", "vm", diskLockFile))
	if err != nil {
		return nil, err
	}
	var inUse []string
	for _, lock := range locks {
		release, ok, err := util.TryAcquireFileLock(lock)
		if err != nil {
			return nil, err
		}
		if !ok {
			inUse = append(inUse, filepath.Dir(lock))
			continue
		}
		release()
	}
	return inUse, nil
}
