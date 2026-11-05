// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"context"
	"fmt"
	"time"

	"enclave/internal/util"
)

// defaultLockTimeout bounds a group lock wait when ctx has no deadline, so a
// wedged holder fails the command instead of hanging it.
const defaultLockTimeout = 2 * time.Minute

const (
	lockPollMin = 10 * time.Millisecond
	lockPollMax = 200 * time.Millisecond
)

// acquireLock polls a non-blocking flock (LockFileEx on Windows) because a
// blocking flock cannot be interrupted by ctx. Locks are per open file, so
// two holders in one process exclude each other as well.
func acquireLock(ctx context.Context, path string) (func(), error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultLockTimeout)
		defer cancel()
	}
	wait := lockPollMin
	for {
		release, ok, err := util.TryAcquireFileLock(path)
		if err != nil {
			return nil, fmt.Errorf("peer group lock %s: %w", path, err)
		}
		if ok {
			return release, nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("waiting for peer group lock %s: %w", path, ctx.Err())
		case <-timer.C:
		}
		wait = min(wait*2, lockPollMax)
	}
}
