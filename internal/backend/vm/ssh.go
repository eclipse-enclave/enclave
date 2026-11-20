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
	"net"
	"os"
	"strconv"
	"strings"

	"enclave/internal/util"
)

const (
	sshBinary       = "ssh"
	sshKeygenBinary = "ssh-keygen"
	// guestSessionCommand is the remote command: what to run and with which
	// environment comes from the control export, not from argv.
	guestSessionCommand = "/bin/bash " + guestInPath + "/session"
	// Modes of the session script besides running the tool on the terminal.
	sessionModeDetached = "detached"
	sessionModeAttach   = "attach"
)

// generateKey creates an ed25519 key pair at path and path.pub.
func (b *Backend) generateKey(ctx context.Context, path string) error {
	out, err := b.host.Output(ctx, sshKeygenBinary, "-q", "-t", "ed25519", "-N", "", "-C", "enclave-vm", "-f", path)
	if err != nil {
		return fmt.Errorf("vm backend: ssh-keygen: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// writeKnownHosts pins the guest's host key, which the host generated, for
// the forwarded port. The client accepts nothing else.
func writeKnownHosts(rt runtimeFiles, port int) error {
	public, err := os.ReadFile(rt.hostKey() + ".pub")
	if err != nil {
		return fmt.Errorf("vm backend: read guest host key: %w", err)
	}
	fields := strings.Fields(string(public))
	if len(fields) < 2 {
		return fmt.Errorf("vm backend: guest host key %s.pub is malformed", rt.hostKey())
	}
	line := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", port, fields[0], fields[1])
	return os.WriteFile(rt.knownHosts(), []byte(line), 0o600) // #nosec G703 -- a fixed name inside the session runtime directory.
}

// sshArgs connects to the guest as root with the session's identity only:
// no user configuration, agent, forwarding or host keys from elsewhere. The
// remote command is the guest's session script in one of its modes.
func sshArgs(rt runtimeFiles, port int, tty bool, mode ...string) []string {
	args := []string{
		"-F", "/dev/null",
		"-i", rt.clientKey(),
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "UserKnownHostsFile=" + rt.knownHosts(),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "BatchMode=yes",
		"-o", "ForwardAgent=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR",
		"-p", strconv.Itoa(port),
	}
	if tty {
		args = append(args, "-tt")
	} else {
		args = append(args, "-T")
	}
	command := guestSessionCommand
	for _, word := range mode {
		command += " " + util.ShellQuote(word)
	}
	return append(args, "root@127.0.0.1", command)
}

// dtachDetachKey maps a detach key in Docker's notation to dtach's. dtach
// detaches on one control character, so key sequences have no equivalent.
func dtachDetachKey(keys string) (string, error) {
	key, isCtrl := strings.CutPrefix(strings.ToLower(strings.TrimSpace(keys)), "ctrl-")
	if !isCtrl || len(key) != 1 || !strings.Contains("abcdefghijklmnopqrstuvwxyz[\\]^_", key) {
		return "", fmt.Errorf("vm backend: detach keys %q are not supported; use one ctrl-<key>", keys)
	}
	return "^" + strings.ToUpper(key), nil
}

// freeLoopbackPort asks the kernel for an unused loopback port. Another
// process can take it before QEMU binds it; QEMU then fails to start and
// the session reports that.
func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("vm backend: find a free loopback port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
