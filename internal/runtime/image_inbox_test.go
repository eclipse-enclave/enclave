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

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestImageInboxMountIsProjectScopedAndReadOnly(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := newCacheMountRuntime(home)
	r.run.ImageInbox = true

	acc := newMountAccumulator(nil, nil)
	if err := r.addImageInboxMount(acc); err != nil {
		t.Fatalf("addImageInboxMount: %v", err)
	}

	want := config.HostImageInboxDir(home, "projhash")
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
	if _, err := os.Stat(config.HostImageInboxDir(home, "projhash")); !os.IsNotExist(err) {
		t.Fatalf("inbox dir created without --image-inbox: %v", err)
	}
}
