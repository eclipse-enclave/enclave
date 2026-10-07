// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func qemuProcessStub(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestQEMULeaseSurvivesParentDescriptorClose(t *testing.T) {
	home, _ := protectedIdentityTestDir(t)
	qemuProcessStub(t, "exit 0\n")
	lease, err := AcquireQEMULease(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	// The child owns the inherited descriptor after the CLI's copy closes.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, "/bin/sh", "-c", "printf ready; read done")
	child.ExtraFiles = []*os.File{lease}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	ready := make([]byte, len("ready"))
	if _, err := io.ReadFull(output, ready); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RequireNoQEMUSessions(ctx, home); err == nil || !strings.Contains(err.Error(), "QEMU") {
		t.Fatalf("lost inherited QEMU lease: %v", err)
	}
	// Other ordinary sessions have independent leases, not a global mutex.
	other, err := AcquireQEMULease(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(input, "done\n"); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := RequireNoQEMUSessions(ctx, home); err != nil {
		t.Fatalf("ended child remained active: %v", err)
	}
}

func TestQEMULeaseSerializesWithProtectedAdmission(t *testing.T) {
	home, _ := protectedIdentityTestDir(t)
	release, err := AcquireAuthMountAdmission(home)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	started := make(chan struct{})
	done := make(chan *os.File, 1)
	failed := make(chan error, 1)
	go func() {
		close(started)
		lease, err := AcquireQEMULease(home)
		if err != nil {
			failed <- err
			return
		}
		done <- lease
	}()
	<-started
	select {
	case lease := <-done:
		_ = lease.Close()
		t.Fatal("QEMU preparation bypassed enrollment admission")
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case lease := <-done:
		_ = lease.Close()
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("QEMU preparation did not resume")
	}
}

func TestQEMUOlderProcessDetectionAndInspectionFailure(t *testing.T) {
	for name, body := range map[string]string{
		"linux":       "printf '%s\n' qemu-system-x86\n",
		"macos":       "printf '%s\n' /opt/homebrew/bin/qemu-system-x86_64\n",
		"unavailable": "exit 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			home, _ := protectedIdentityTestDir(t)
			qemuProcessStub(t, body)
			if err := RequireNoQEMUSessions(context.Background(), home); err == nil {
				t.Fatal("unverified older QEMU process was ignored")
			}
		})
	}
}
