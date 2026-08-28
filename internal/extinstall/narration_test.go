// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNarrateWithoutStreamDiscards pins that this package never picks a process
// stream of its own: with no narration configured there is nothing to render
// to, and nothing is rendered.
func TestNarrateWithoutStreamDiscards(t *testing.T) {
	if got := (Env{}).narrate(); got != io.Discard {
		t.Fatalf("narrate() = %v, want io.Discard", got)
	}
	if got := (Env{Narration: os.Stderr}).narrate(); got != os.Stderr {
		t.Fatalf("narrate() = %v, want the configured stream", got)
	}
}

// TestAddWithoutNarrationInstallsSilently covers the --json path: the installer
// still does the work and still reports it through the returned results, but
// renders no human-facing text anywhere.
func TestAddWithoutNarrationInstallsSilently(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, out := testEnv(t, fetcher, "")
	env.Narration = nil

	results, err := Add(context.Background(), env, addRequest())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s", fmtResults(results))
	}
	if out.Len() != 0 {
		t.Fatalf("narration was rendered despite a nil stream:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); statErr != nil {
		t.Fatalf("extension was not installed: %v", statErr)
	}
}

// TestConfirmWithoutNarrationFails pins that a run which may prompt but has
// nowhere to put the question fails instead of assuming an answer.
func TestConfirmWithoutNarrationFails(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "y\n")
	env.Narration = nil

	req := addRequest()
	req.Yes = false
	req.Interactive = true

	results, err := Add(context.Background(), env, req)
	if err == nil && !HasFailure(results) {
		t.Fatalf("a prompt with nowhere to render succeeded: %s", fmtResults(results))
	}
	message := results[0].Error
	if err != nil {
		message = err.Error()
	}
	if !strings.Contains(message, "--yes") {
		t.Fatalf("error = %q, want it to point at --yes", message)
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); !os.IsNotExist(statErr) {
		t.Fatal("an unconfirmed install wrote files")
	}
}
