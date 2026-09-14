// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// scriptedBuild returns a BuildFunc that plays back the given attempts and
// counts how many times it was called.
func scriptedBuild(t *testing.T, attempts ...struct {
	output string
	err    error
}) (BuildFunc, *int) {
	t.Helper()
	calls := 0
	return func(_ context.Context, _ BuildRequest, out io.Writer) error {
		if calls >= len(attempts) {
			t.Fatalf("unexpected build attempt %d", calls+1)
		}
		attempt := attempts[calls]
		calls++
		_, _ = io.WriteString(out, attempt.output)
		return attempt.err
	}, &calls
}

type attempt = struct {
	output string
	err    error
}

func quietWarnings(t *testing.T) *[]string {
	t.Helper()
	var warnings []string
	orig := buildWarnf
	buildWarnf = func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { buildWarnf = orig })
	origDelay := BuildRetryDelay
	BuildRetryDelay = time.Millisecond
	t.Cleanup(func() { BuildRetryDelay = origDelay })
	return &warnings
}

func TestRunBuildRerunsAfterTransientNetworkFailure(t *testing.T) {
	t.Setenv(BuildRetriesEnv, "")
	warnings := quietWarnings(t)
	build, calls := scriptedBuild(t,
		attempt{output: "curl: (28) Operation timed out after 60001 milliseconds\n", err: errors.New("exit status 1")},
		attempt{output: "Successfully built\n"},
	)
	var shown strings.Builder
	log, err := RunBuild(context.Background(), "image", BuildRequest{}, &shown, build)
	if err != nil {
		t.Fatalf("expected the rerun to succeed, got %v", err)
	}
	if *calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", *calls)
	}
	if !strings.Contains(log.Tail(), "Successfully built") {
		t.Fatalf("returned log must belong to the final attempt, got %q", log.Tail())
	}
	if !strings.Contains(shown.String(), "timed out") || !strings.Contains(shown.String(), "Successfully built") {
		t.Fatalf("both attempts must stream to the caller, got %q", shown.String())
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0], "retrying from the last cached layer") || !strings.Contains((*warnings)[0], "attempt 2/2") {
		t.Fatalf("expected one rerun notice, got %v", *warnings)
	}
}

func TestRunBuildDoesNotRerunGenericFailures(t *testing.T) {
	t.Setenv(BuildRetriesEnv, "")
	quietWarnings(t)
	build, calls := scriptedBuild(t, attempt{output: "exit code: 1\n", err: errors.New("exit status 1")})
	if _, err := RunBuild(context.Background(), "image", BuildRequest{}, io.Discard, build); err == nil {
		t.Fatal("expected the failure to be returned")
	}
	if *calls != 1 {
		t.Fatalf("a non-network failure must not be rerun, got %d attempts", *calls)
	}
}

func TestRunBuildHonoursRetryCount(t *testing.T) {
	quietWarnings(t)
	transient := attempt{output: "Temporary failure resolving 'deb.debian.org'\n", err: errors.New("exit status 100")}

	t.Setenv(BuildRetriesEnv, "0")
	build, calls := scriptedBuild(t, transient)
	if _, err := RunBuild(context.Background(), "image", BuildRequest{}, io.Discard, build); err == nil || *calls != 1 {
		t.Fatalf("ENCLAVE_BUILD_RETRIES=0 must run once, got err=%v attempts=%d", err, *calls)
	}

	t.Setenv(BuildRetriesEnv, "2")
	build, calls = scriptedBuild(t, transient, transient, transient)
	if _, err := RunBuild(context.Background(), "image", BuildRequest{}, io.Discard, build); err == nil || *calls != 3 {
		t.Fatalf("ENCLAVE_BUILD_RETRIES=2 must run three times, got err=%v attempts=%d", err, *calls)
	}

	t.Setenv(BuildRetriesEnv, "lots")
	build, calls = scriptedBuild(t)
	if _, err := RunBuild(context.Background(), "image", BuildRequest{}, io.Discard, build); err == nil || !strings.Contains(err.Error(), BuildRetriesEnv) || *calls != 0 {
		t.Fatalf("an invalid retry count must be rejected before building, got err=%v attempts=%d", err, *calls)
	}
}

func TestRunBuildStopsRerunningWhenCancelled(t *testing.T) {
	t.Setenv(BuildRetriesEnv, "")
	quietWarnings(t)
	BuildRetryDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	build, calls := scriptedBuild(t, attempt{output: "connection reset by peer\n", err: errors.New("exit status 1")})
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := RunBuild(ctx, "image", BuildRequest{}, io.Discard, build)
	if err == nil || *calls != 1 {
		t.Fatalf("cancellation during the pause must return the original error without a rerun, got err=%v attempts=%d", err, *calls)
	}
}
