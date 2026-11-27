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

func TestTryAcquireFileLockSkipsHeldLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "test.lock")
	releaseFirst, ok, err := TryAcquireFileLock(lockPath)
	if err != nil || !ok {
		t.Fatalf("acquire free lock: ok=%v err=%v", ok, err)
	}
	if release, ok, err := TryAcquireFileLock(lockPath); err != nil || ok || release != nil {
		t.Fatalf("held lock: ok=%v err=%v release=%v, want not acquired", ok, err, release != nil)
	}
	releaseFirst()
	releaseAgain, ok, err := TryAcquireFileLock(lockPath)
	if err != nil || !ok {
		t.Fatalf("acquire released lock: ok=%v err=%v", ok, err)
	}
	releaseAgain()
}

func TestSharedFileLocksDoNotBlockEachOther(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "test.lock")
	releaseFirst, err := AcquireSharedFileLock(lockPath, func() {
		t.Error("first shared lock unexpectedly reported contention")
	})
	if err != nil {
		t.Fatalf("acquire first shared lock: %v", err)
	}
	defer releaseFirst()

	acquired := make(chan struct{})
	go func() {
		defer close(acquired)
		releaseSecond, lockErr := AcquireSharedFileLock(lockPath, func() {
			t.Error("second shared lock waited for the first")
		})
		if lockErr != nil {
			t.Errorf("acquire second shared lock: %v", lockErr)
			return
		}
		releaseSecond()
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second shared lock blocked on the first")
	}
}

func TestExclusiveFileLockWaitsForSharedHolder(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "test.lock")
	releaseShared, err := AcquireSharedFileLock(lockPath, nil)
	if err != nil {
		t.Fatalf("acquire shared lock: %v", err)
	}
	defer releaseShared()

	assertWaitsUntilReleased(t, releaseShared, func(onWait func()) (func(), error) {
		return AcquireFileLock(lockPath, onWait)
	})
}

func TestSharedFileLockWaitsForExclusiveHolder(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "test.lock")
	releaseExclusive, err := AcquireFileLock(lockPath, nil)
	if err != nil {
		t.Fatalf("acquire exclusive lock: %v", err)
	}
	defer releaseExclusive()

	assertWaitsUntilReleased(t, releaseExclusive, func(onWait func()) (func(), error) {
		return AcquireSharedFileLock(lockPath, onWait)
	})
}

// assertWaitsUntilReleased checks that acquire reports contention and only
// returns once the holder's lock is released.
func assertWaitsUntilReleased(t *testing.T, releaseHolder func(), acquire func(onWait func()) (func(), error)) {
	t.Helper()
	waiting := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		defer close(acquired)
		release, err := acquire(func() { close(waiting) })
		if err != nil {
			t.Errorf("acquire lock: %v", err)
			return
		}
		release()
	}()

	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("lock acquisition did not report contention")
	}
	select {
	case <-acquired:
		t.Fatal("lock acquired while the holder still held it")
	case <-time.After(20 * time.Millisecond):
	}
	releaseHolder()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("lock not acquired after the holder released it")
	}
}
