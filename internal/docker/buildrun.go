// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// BuildRetriesEnv sets how many times an image build is rerun after it fails
// on a transient network error. Each rerun starts from the layer cache, so it
// only repeats the step that failed, and the build-time package caches keep
// what that step had already downloaded.
const BuildRetriesEnv = "ENCLAVE_BUILD_RETRIES"

const defaultBuildRetries = 1

// BuildStallWarnAfter is how long the engine may stay silent before the user
// is told the build may be stuck. A stalled network transfer looks exactly
// like a slow one from outside, and the engine prints nothing while waiting.
const BuildStallWarnAfter = 3 * time.Minute

// BuildRetryDelay is the pause before a rerun; tests shorten it.
var BuildRetryDelay = 10 * time.Second

// BuildRetriesFromEnv returns the rerun count requested through
// BuildRetriesEnv, defaulting to one.
func BuildRetriesFromEnv() (int, error) {
	value := strings.TrimSpace(os.Getenv(BuildRetriesEnv))
	if value == "" {
		return defaultBuildRetries, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %q", BuildRetriesEnv, value)
	}
	return n, nil
}

// BuildFunc is the engine build; callers pass Build or a test double.
type BuildFunc func(context.Context, BuildRequest, io.Writer) error

// RunBuild runs an engine build with the shared robustness policy: output is
// teed through a BuildLog for failure classification, the user is warned
// while the engine stays silent (unless the progress style is quiet, where
// silence is normal), and a build that fails on a transient network error is
// rerun up to BuildRetriesFromEnv times. what names the build in messages,
// for example "image" or "gateway image". The returned log belongs to the
// final attempt.
func RunBuild(ctx context.Context, what string, req BuildRequest, out io.Writer, build BuildFunc) (*BuildLog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	retries, err := BuildRetriesFromEnv()
	if err != nil {
		return NewBuildLog(nil), err
	}
	for attempt := 1; ; attempt++ {
		log := NewBuildLog(out)
		err := runWatchedBuild(ctx, what, req, log, build)
		if err == nil {
			return log, nil
		}
		if attempt > retries || !IsTransientNetworkFailure(log.Tail()) {
			return log, err
		}
		buildWarnf("The %s build failed on a network transfer; retrying from the last cached layer in %s (attempt %d/%d)",
			what, BuildRetryDelay, attempt+1, retries+1)
		select {
		case <-ctx.Done():
			return log, err
		case <-time.After(BuildRetryDelay):
		}
	}
}

func runWatchedBuild(ctx context.Context, what string, req BuildRequest, log *BuildLog, build BuildFunc) error {
	if !BuildProgressIsQuiet(req.Progress) {
		stop := log.WarnWhenStalled(BuildStallWarnAfter, func(idle time.Duration, lastLine string) {
			if lastLine == "" {
				buildWarnf("No %s build output for %s; the build may be stalled on a network transfer.", what, idle.Round(time.Second))
				return
			}
			buildWarnf("No %s build output for %s; the build may be stalled on a network transfer. Last output: %s", what, idle.Round(time.Second), lastLine)
		})
		defer stop()
	}
	return build(ctx, req, log)
}
