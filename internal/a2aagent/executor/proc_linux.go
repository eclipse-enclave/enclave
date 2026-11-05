// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package executor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const prSetChildSubreaper = 36

// enableSubreaper makes orphaned descendants, including those that called
// setsid, reparent to this process so escapes stay visible.
func enableSubreaper() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_CHILD_SUBREAPER): %w", errno)
	}
	return nil
}

func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
	cmd.SysProcAttr.Setsid = false
}

func signalGroup(pgid int, sig signal) error {
	if pgid <= 1 {
		return fmt.Errorf("refusing to signal process group %d", pgid)
	}
	err := syscall.Kill(-pgid, syscall.Signal(sig))
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func signalProcess(pid int, sig signal) error {
	err := syscall.Kill(pid, syscall.Signal(sig))
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// reap collects pid if it is an exited child of this process; it never blocks.
func reap(pid int) {
	var status syscall.WaitStatus
	_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
}

// scanProcesses reads every visible process from /proc.
func scanProcesses() ([]procInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]procInfo, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat")) // #nosec G304 -- fixed /proc path.
		if err != nil {
			continue
		}
		info, ok := parseStat(pid, string(data))
		if ok {
			out = append(out, info)
		}
	}
	return out, nil
}

// parseStat reads state, ppid and pgrp; comm may contain spaces and parentheses.
func parseStat(pid int, stat string) (procInfo, bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return procInfo{}, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 3 {
		return procInfo{}, false
	}
	ppid, err1 := strconv.Atoi(fields[1])
	pgid, err2 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil {
		return procInfo{}, false
	}
	zombie := fields[0] == "Z" || fields[0] == "X"
	// A leader that called pthread_exit shows Z while its other threads still run.
	if zombie && len(fields) > 17 {
		if threads, err := strconv.Atoi(fields[17]); err == nil && threads > 1 {
			zombie = false
		}
	}
	return procInfo{PID: pid, PPID: ppid, PGID: pgid, Zombie: zombie}, true
}

func platformProcessOps() (processOps, error) {
	if err := enableSubreaper(); err != nil {
		return processOps{}, err
	}
	return processOps{
		signalGroup:   signalGroup,
		signalProcess: signalProcess,
		scan:          scanProcesses,
		reap:          reap,
		self:          os.Getpid(),
	}, nil
}
