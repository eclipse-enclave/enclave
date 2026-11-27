// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestImageInboxMountIsProjectScopedAndReadOnly(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := newCacheMountRuntime(home)
	r.run.ImageInbox = true
	r.backend = &engineBackend{name: backend.NamePodman}

	acc := newMountAccumulator(nil, nil)
	if err := r.addImageInboxMount(acc); err != nil {
		t.Fatalf("addImageInboxMount: %v", err)
	}

	want := config.HostImageInboxDir(home, backend.NamePodman, "projhash")
	var found bool
	for _, m := range acc.Mounts() {
		if m.ContainerPath != model.ContainerImageInboxDir {
			continue
		}
		found = true
		if m.Source != want {
			t.Errorf("inbox source = %s, want %s", m.Source, want)
		}
		if !m.ReadOnly {
			t.Error("inbox mount must be read-only")
		}
	}
	if !found {
		t.Fatalf("no mount at %s", model.ContainerImageInboxDir)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("inbox dir not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("inbox dir mode = %o, want 700", perm)
	}
	if v, ok := lookupEnv(acc.Env(), model.EnvImageInbox); !ok || v != model.ContainerImageInboxDir {
		t.Errorf("%s = %q (set %v), want %s", model.EnvImageInbox, v, ok, model.ContainerImageInboxDir)
	}
}

func TestImageInboxMountIsOptIn(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := newCacheMountRuntime(home)

	acc := newMountAccumulator(nil, nil)
	if err := r.addImageInboxMount(acc); err != nil {
		t.Fatalf("addImageInboxMount: %v", err)
	}
	if len(acc.Mounts()) != 0 || len(acc.Env()) != 0 {
		t.Fatalf("mounts/env added without --image-inbox: %v %v", acc.Mounts(), acc.Env())
	}
	if _, err := os.Stat(config.HostImageInboxDir(home, backend.NameDocker, "projhash")); !os.IsNotExist(err) {
		t.Fatalf("inbox dir created without --image-inbox: %v", err)
	}
}

// A sweep in another enclave process must not remove the inbox between its
// creation and the container that mounts it appearing, so the inbox lock is
// held until the session start lock is released.
func TestImageInboxLockIsHeldUntilTheSessionStarts(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := newCacheMountRuntime(home)
	r.run.ImageInbox = true
	r.backend = &engineBackend{name: backend.NameDocker}
	releaseStart, err := r.acquireSessionStartLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.addImageInboxMount(newMountAccumulator(nil, nil)); err != nil {
		t.Fatalf("addImageInboxMount: %v", err)
	}

	lockPath := config.HostImageInboxLockPath(home)
	if _, ok, err := util.TryAcquireFileLock(lockPath); ok || err != nil {
		t.Fatalf("inbox lock free (%v, %v) while the session is starting", ok, err)
	}
	releaseStart()
	release, ok, err := util.TryAcquireFileLock(lockPath)
	if !ok || err != nil {
		t.Fatalf("inbox lock still held (%v, %v) after the session started", ok, err)
	}
	release()
}

// engineBackend is a fakeBackend that runs on a container engine, which
// decides where its sessions' image inboxes live.
type engineBackend struct {
	fakeBackend
	name string
}

func (b *engineBackend) Name() string { return b.name }

// Inbox-enabled starts only exclude sweeps, so starts in unrelated projects
// do not wait for each other.
func TestImageInboxStartsShareTheInboxLock(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	first := newCacheMountRuntime(home)
	first.run.ImageInbox = true
	first.backend = &engineBackend{name: backend.NameDocker}
	if err := first.addImageInboxMount(newMountAccumulator(nil, nil)); err != nil {
		t.Fatalf("first addImageInboxMount: %v", err)
	}
	defer first.releaseImageInboxLock()

	second := newCacheMountRuntime(home)
	second.project.Hash = "otherhash"
	second.run.ImageInbox = true
	second.backend = &engineBackend{name: backend.NameDocker}
	done := make(chan error, 1)
	go func() { done <- second.addImageInboxMount(newMountAccumulator(nil, nil)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second addImageInboxMount: %v", err)
		}
		second.releaseImageInboxLock()
	case <-time.After(time.Second):
		t.Fatal("an inbox-enabled start waited for another start")
	}
}
