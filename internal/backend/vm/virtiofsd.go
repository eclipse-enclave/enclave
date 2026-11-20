// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const virtiofsdEnv = "ENCLAVE_VM_VIRTIOFSD"

// Distributions install virtiofsd outside PATH.
var virtiofsdPaths = []string{"/usr/libexec/virtiofsd", "/usr/lib/virtiofsd", "/usr/lib/qemu/virtiofsd"}

type virtiofsdCaps struct {
	// ReadOnly reports --readonly, which makes the daemon refuse writes.
	ReadOnly bool
	// InodeFileHandles reports --inode-file-handles.
	InodeFileHandles bool
}

func (b *Backend) findVirtiofsd() (string, error) {
	candidates := append([]string{"virtiofsd"}, virtiofsdPaths...)
	if override := strings.TrimSpace(os.Getenv(virtiofsdEnv)); override != "" {
		candidates = []string{override}
	}
	for _, candidate := range candidates {
		if path, err := b.host.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("vm backend: virtiofsd not found; install virtiofsd on the host or set %s", virtiofsdEnv)
}

func (b *Backend) probeVirtiofsd(ctx context.Context, binary string) (virtiofsdCaps, error) {
	help, err := b.host.Output(ctx, binary, "--help")
	if err != nil {
		return virtiofsdCaps{}, fmt.Errorf("vm backend: %s --help: %w", binary, err)
	}
	text := string(help)
	return virtiofsdCaps{
		ReadOnly:         strings.Contains(text, "--readonly"),
		InodeFileHandles: strings.Contains(text, "--inode-file-handles"),
	}, nil
}

// virtiofsdArgs runs the daemon as the invoking user. Without privileges it
// cannot enter a sandbox, and it serves every guest user with this user's
// rights, which is why the guest tool user has the host UID and GID.
func virtiofsdArgs(sh share, socket string, caps virtiofsdCaps) []string {
	args := []string{"--socket-path=" + socket, "--shared-dir=" + sh.Source, "--sandbox", "none"}
	if sh.ReadOnly && caps.ReadOnly {
		args = append(args, "--readonly")
	}
	if caps.InodeFileHandles {
		// "mandatory" needs CAP_DAC_READ_SEARCH; "prefer" falls back.
		args = append(args, "--inode-file-handles=prefer")
	}
	return args
}

// startExports starts one virtiofsd per virtiofs share and waits until each
// listens: QEMU connects at startup and fails on a missing socket.
func (b *Backend) startExports(ctx context.Context, binary string, caps virtiofsdCaps, rt runtimeFiles, shares []share) (func(), error) {
	var stops []func()
	stop := func() {
		for _, stopOne := range stops {
			stopOne()
		}
	}
	var sockets []string
	for i, sh := range shares {
		if sh.Transport != transportVirtiofs {
			continue
		}
		socket := rt.socket(i)
		pid, stopOne, err := b.host.Start(binary, virtiofsdArgs(sh, socket, caps), rt.exportLog(i))
		if err != nil {
			stop()
			return nil, fmt.Errorf("vm backend: start virtiofsd for %s: %w", sh.Source, err)
		}
		stops = append(stops, stopOne)
		// For the process that stops a detached session later.
		if err := os.WriteFile(rt.exportPID(i), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil { // #nosec G703 -- a fixed name inside the session runtime directory.
			stop()
			return nil, err
		}
		sockets = append(sockets, socket)
	}
	deadline := time.Now().Add(b.exportTimeout)
	for _, socket := range sockets {
		for !socketExists(socket) {
			if ctx.Err() != nil || time.Now().After(deadline) {
				stop()
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, fmt.Errorf("vm backend: virtiofsd did not open %s; see the virtiofsd logs in %s", socket, rt.dir)
			}
			time.Sleep(b.pollInterval)
		}
	}
	return stop, nil
}

func socketExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// reapExports stops the virtiofsd processes of a runtime directory that
// another enclave process started. They exit by themselves when QEMU closes
// their sockets; this catches the ones that did not. A PID is signalled only
// if its command line still names that directory's socket.
func reapExports(rt runtimeDir) {
	files, err := rt.open()
	if err != nil {
		return
	}
	pidFiles, _ := filepath.Glob(filepath.Join(files.dir, "fs*.sock.pid"))
	for _, pidFile := range pidFiles {
		socket := filepath.Join(files.socketDir(), strings.TrimSuffix(filepath.Base(pidFile), ".pid"))
		if pid, ok := ownedProcess(pidFile, "--socket-path="+socket); ok {
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Signal(syscall.SIGTERM)
			}
		}
	}
}

// ownedProcess reads a PID file and reports the PID only if that process
// still carries marker on its command line. PIDs are reused; a PID file
// alone does not say whose process it names.
func ownedProcess(pidFile string, marker string) (int, bool) {
	data, err := os.ReadFile(pidFile) // #nosec G304 -- a PID file inside the session runtime directory.
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 0, false
	}
	cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline")) // #nosec G304 -- a numeric PID under /proc.
	if err != nil || !strings.Contains(string(cmdline), marker) {
		return 0, false
	}
	return pid, true
}
