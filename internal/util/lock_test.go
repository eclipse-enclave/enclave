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
	releaseFirst, waited, err := AcquireFileLock(lockPath, nil)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer releaseFirst()
	if waited {
		t.Fatal("first lock acquisition unexpectedly waited")
	}

	waiting := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		releaseSecond, didWait, lockErr := AcquireFileLock(lockPath, func() { close(waiting) })
		if lockErr == nil {
			defer releaseSecond()
		}
		if !didWait || lockErr != nil {
			t.Errorf("second lock acquisition = (waited %v, error %v), want (true, nil)", didWait, lockErr)
		}
		close(acquired)
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
