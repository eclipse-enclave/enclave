// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireFileLockReportsContention(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "test.lock")
	releaseFirst, err := AcquireFileLock(lockPath, func() {
		t.Error("first lock acquisition unexpectedly reported contention")
	})
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer releaseFirst()

	waiting := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		defer close(acquired)
		releaseSecond, lockErr := AcquireFileLock(lockPath, func() { close(waiting) })
		if lockErr != nil {
			t.Errorf("second lock acquisition failed: %v", lockErr)
			return
		}
		releaseSecond()
	}()

	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("second lock acquisition did not report contention")
	}
	select {
	case <-acquired:
		t.Fatal("second lock acquisition completed before first lock was released")
	case <-time.After(20 * time.Millisecond):
	}
	releaseFirst()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second lock acquisition did not complete after release")
	}
}
