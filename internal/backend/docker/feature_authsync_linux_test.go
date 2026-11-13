// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A source held under a write lease fails the copy at once instead of
// blocking the sync until the lease holder lets go. The next sync copies it.
func TestSyncFeatureAuthStoreDoesNotWaitOnLeasedSource(t *testing.T) {
	stores := newFeatureAuthStores(t, "", "config.yml")
	source, err := os.Open(filepath.Join(stores.configDir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	fd := int(source.Fd()) // #nosec G115 -- an open file descriptor fits in int.
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETLEASE, unix.F_WRLCK); err != nil {
		t.Skipf("file leases unavailable here: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		result <- stores.b.syncFeatureAuthStore(context.Background(), stores.configDir, stores.sync)
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected the leased source to fail the copy")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sync waited on the leased source")
	}
	dst := filepath.Join(stores.authDir, "config.yml")
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination written while the source was leased: %v", err)
	}

	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETLEASE, unix.F_UNLCK); err != nil {
		t.Fatal(err)
	}
	if err := stores.b.syncFeatureAuthStore(context.Background(), stores.configDir, stores.sync); err != nil {
		t.Fatalf("sync after the lease ended: %v", err)
	}
	assertFileContent(t, dst, "oauth_token: inside\n")
}
