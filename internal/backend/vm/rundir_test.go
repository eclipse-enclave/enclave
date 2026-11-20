// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
)

// disown turns a runtime directory into one that the backend must not take
// for the session's: its mode is what another user's directory under the
// same path would fail on as well. It returns the tree as it is then.
func disown(t *testing.T, dir string) map[string]string {
	t.Helper()
	if err := os.Chmod(dir, 0o755); err != nil { // #nosec G302 -- the test plays a directory that is not private.
		t.Fatal(err)
	}
	return treeOf(t, dir)
}

// treeOf describes every entry below root by its type, mode and, for a
// regular file, content. Sockets are left out: the processes that listen on
// them remove them when they stop, and the fake ones do the same.
func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSocket != 0 {
			return nil
		}
		described := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path) // #nosec G304 -- a file in the test's own directory.
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			described += " " + hex.EncodeToString(sum[:])
		}
		rel, _ := filepath.Rel(root, path)
		tree[rel] = described
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func sameTree(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := treeOf(t, root)
	for path, was := range before {
		if is, ok := after[path]; !ok {
			t.Errorf("%s was removed from a directory that is not the session's", path)
		} else if is != was {
			t.Errorf("%s was changed in a directory that is not the session's: %s, was %s", path, is, was)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s was created in a directory that is not the session's", path)
		}
	}
}

// Every way a command comes to a runtime directory, tried on one that is no
// longer the session's. Each file in it would change what the command does if
// it were read: the PID file names a process that runs and can be killed,
// the control socket answers to the session's name, the exit code says 0,
// the file-mount result differs from the host file, and the started mark
// would have the launch payload removed. None of that may happen, and the
// directory has to be as it was afterwards.
func TestNothingIsReadOrRemovedInARuntimeDirectoryThatIsNotTheSessions(t *testing.T) {
	silent := func(t *testing.T) {
		previous := qmpIOTimeout
		qmpIOTimeout = 20 * time.Millisecond
		t.Cleanup(func() { qmpIOTimeout = previous })
	}

	t.Run("stop from another process", func(t *testing.T) {
		b, host, paths, record := startDetached(t)
		before := readTestFile(t, paths["allow"])
		writeTestFile(t, record.Files[0].ResultPath, "planted\n")
		tree := disown(t, record.RuntimeDir)
		other := otherProcess(b, host)
		other.shutdownTimeout = 40 * time.Millisecond
		ref := backend.SessionRef{Name: record.Name}

		if session, err := other.Inspect(context.Background(), ref); err != nil || session.Status == statusRunning {
			t.Errorf("Inspect = %+v, %v; the PID file was read", session, err)
		}
		if err := other.Attach(context.Background(), ref, backend.AttachIO{}); err == nil {
			t.Error("Attach used the identity files")
		}
		err := other.EndSession(context.Background(), ref, backend.StopOptions{Finalize: true})
		if err == nil || !strings.Contains(err.Error(), "its VM cannot be found") {
			t.Errorf("EndSession = %v", err)
		}
		if len(host.killed) != 0 || host.guest.isOff() {
			t.Errorf("killed = %v, off = %v; the PID file was read", host.killed, host.guest.isOff())
		}
		if got := host.guest.received(); slices.Contains(got, "query-name") || slices.Contains(got, "system_powerdown") {
			t.Errorf("monitor commands = %v; the control socket was used", got)
		}
		if len(host.commandsOf(sshBinary)) != 1 {
			t.Errorf("ssh ran with the identity files: %v", host.commandsOf(sshBinary))
		}
		if got := readTestFile(t, paths["allow"]); got != before {
			t.Errorf("host file = %q; the file-mount result was read", got)
		}
		if _, ok, _ := other.readRecord(record.Name); !ok {
			t.Error("the session was forgotten")
		}
		sameTree(t, record.RuntimeDir, tree)
	})

	t.Run("forced shutdown by the session's own process", func(t *testing.T) {
		b, host, baseDir := newTestBackend(t)
		req, paths := sessionRequest(t, baseDir)
		before := readTestFile(t, paths["allow"])
		host.staysUp, host.mute = true, true
		silent(t)
		b.shutdownTimeout = 50 * time.Millisecond
		var dir string
		var tree map[string]string
		host.duringSession = func(rt runtimeFiles) {
			dir = rt.dir
			writeTestFile(t, filepath.Join(rt.out(), "files", "01", "result"), "planted\n")
			// What the guest writes when the tool ends, so that the tree
			// taken here is the tree at the end.
			writeTestFile(t, filepath.Join(rt.out(), guestExitCodeFile), "0\n")
			writeTestFile(t, rt.console(), "planted console line\n")
			tree = disown(t, rt.dir)
		}
		status, err := b.Run(context.Background(), req, backend.AttachIO{})
		if err == nil || status.Code == 0 || !strings.Contains(err.Error(), "could not be stopped") {
			t.Errorf("Run = %+v, %v", status, err)
		}
		if err != nil && strings.Contains(err.Error(), "planted console line") {
			t.Error("the console log was read")
		}
		if len(host.killed) != 0 || host.guest.isOff() {
			t.Errorf("killed = %v, off = %v; the PID file was read", host.killed, host.guest.isOff())
		}
		if got := readTestFile(t, paths["allow"]); got != before {
			t.Errorf("host file = %q; the file-mount result was read", got)
		}
		if record, ok, _ := b.readRecord(req.Session.Name); !ok || record.Finalized {
			t.Errorf("record = %+v, ok = %v; want the session kept", record, ok)
		}
		sameTree(t, dir, tree)
	})

	t.Run("exit code and finalization of a session that ended", func(t *testing.T) {
		b, host, baseDir := newTestBackend(t)
		req, paths := sessionRequest(t, baseDir)
		before := readTestFile(t, paths["allow"])
		var dir string
		var tree map[string]string
		host.duringSession = func(rt runtimeFiles) {
			dir = rt.dir
			writeTestFile(t, filepath.Join(rt.out(), "files", "01", "result"), "planted\n")
			writeTestFile(t, filepath.Join(rt.out(), guestExitCodeFile), "0\n")
			tree = disown(t, rt.dir)
		}
		status, err := b.Run(context.Background(), req, backend.AttachIO{})
		if err == nil || status.Code != 1 || !strings.Contains(err.Error(), "no longer this session's") {
			t.Errorf("Run = %+v, %v; the exit code was read", status, err)
		}
		if got := readTestFile(t, paths["allow"]); got != before {
			t.Errorf("host file = %q; the file-mount result was read", got)
		}
		if _, ok, _ := b.readRecord(req.Session.Name); ok {
			t.Error("the session is still recorded")
		}
		sameTree(t, dir, tree)
	})

	t.Run("finalization by a later stop", func(t *testing.T) {
		b, host, paths, record := startDetached(t)
		host.guest.powerOff()
		before := readTestFile(t, paths["allow"])
		writeTestFile(t, record.Files[0].ResultPath, "planted\n")
		tree := disown(t, record.RuntimeDir)
		other := otherProcess(b, host)
		err := other.EndSession(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true})
		if err == nil || !strings.Contains(err.Error(), "no longer this session's") {
			t.Errorf("EndSession = %v", err)
		}
		if got := readTestFile(t, paths["allow"]); got != before {
			t.Errorf("host file = %q; the file-mount result was read", got)
		}
		if _, ok, _ := other.readRecord(record.Name); ok {
			t.Error("the session is still recorded")
		}
		sameTree(t, record.RuntimeDir, tree)
	})

	t.Run("launch payload of a detached start", func(t *testing.T) {
		b, host, baseDir := newTestBackend(t)
		b.bootTimeout = 50 * time.Millisecond
		req, _ := sessionRequest(t, baseDir)
		req.Detached = true
		req.Session.Background = true
		var dir string
		var tree map[string]string
		host.beforeLaunch = func(rt runtimeFiles) {
			dir = rt.dir
			// The mark the launcher is about to write.
			writeTestFile(t, filepath.Join(rt.out(), guestStartedFile), "")
			tree = disown(t, rt.dir)
		}
		if _, err := b.Start(context.Background(), req); err == nil {
			t.Error("Start succeeded; the started mark was read")
		}
		if !host.guest.isOff() {
			t.Error("the VM of the failed start still runs")
		}
		if _, ok := tree[filepath.Join("in", "payload", "env")]; !ok {
			t.Fatalf("the test did not catch the payload: %v", tree)
		}
		sameTree(t, dir, tree)
	})

	// The sockets of a session under a deep home have a directory of their
	// own, which can be lost while the runtime directory is not.
	t.Run("first connection to a VM whose socket directory is lost", func(t *testing.T) {
		b, host, baseDir := newTestBackend(t)
		b.opts.Host.Home = filepath.Join(b.opts.Host.Home, strings.Repeat("d", 90))
		useSocketTempDirs(t, shortTempDir(t))
		req, _ := sessionRequest(t, baseDir)
		req.Detached = true
		req.Session.Background = true
		var tree map[string]string
		var sockets string
		host.afterDaemonize = func(rt runtimeFiles) {
			sockets = rt.sockets
			tree = disown(t, rt.sockets)
		}
		if _, err := b.Start(context.Background(), req); err == nil || !strings.Contains(err.Error(), "socket directory") {
			t.Errorf("Start = %v; want the lost socket directory reported", err)
		}
		if got := host.guest.received(); len(got) != 0 {
			t.Errorf("monitor commands = %v; a socket in the directory was dialled", got)
		}
		if !host.guest.isOff() || len(host.killed) != 1 {
			t.Errorf("off = %v, killed = %v; want the VM of the failed start stopped through its PID", host.guest.isOff(), host.killed)
		}
		if sockets == "" {
			t.Fatal("the session had no socket directory of its own")
		}
		sameTree(t, sockets, tree)
	})

	// ssh reads the identity files every time it runs, so each attempt
	// starts from a new look at the directory.
	t.Run("another ssh attempt", func(t *testing.T) {
		b, host, baseDir := newTestBackend(t)
		req, _ := sessionRequest(t, baseDir)
		// The launcher never starts and ssh fails as it does while sshd
		// is not up, which is what makes the backend try again.
		host.toolExit, host.sshExit = -1, 255
		host.staysUp = true
		var dir string
		var tree map[string]string
		host.duringSSH = func(rt runtimeFiles) {
			if dir == "" {
				dir = rt.dir
				tree = disown(t, rt.dir)
			}
		}
		if _, err := b.Run(context.Background(), req, backend.AttachIO{}); err == nil || !strings.Contains(err.Error(), "no longer this session's") {
			t.Errorf("Run = %v; want the lost runtime directory reported", err)
		}
		if got := host.commandsOf(sshBinary); len(got) != 1 {
			t.Errorf("ssh ran %d times; want no attempt after the directory was lost", len(got))
		}
		sameTree(t, dir, tree)
	})
}
