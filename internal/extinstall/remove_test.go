// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

func removeRequest(names ...string) Request {
	return Request{Kind: model.KindFeature, Op: OpRemove, Names: names, Yes: true}
}

func TestRemoveDeletesManagedExtension(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	results, err := Remove(context.Background(), env, removeRequest("foo"))
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionRemoved {
		t.Fatalf("results = %s", fmtResults(results))
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); !os.IsNotExist(statErr) {
		t.Fatal("extension directory survived removal")
	}
}

// TestRemoveDeletesExtensionWithUnloadableSpec keeps remove usable as the
// recovery path it is meant to be: an upstream schemaVersion bump is enough to
// stop a spec loading, and an extension that can no longer be loaded still
// occupies a directory that has to be removable without a hand-written rm -rf.
func TestRemoveDeletesExtensionWithUnloadableSpec(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	installed := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	writeFixture(t, filepath.Join(installed, "spec.yaml"), "schemaVersion: \"2\"\nkind: mixin\nname: foo\n", 0o644)

	results, err := Remove(context.Background(), env, removeRequest("foo"))
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionRemoved {
		t.Fatalf("results = %s", fmtResults(results))
	}
	if _, statErr := os.Stat(installed); !os.IsNotExist(statErr) {
		t.Fatal("extension directory survived removal")
	}
}

func TestRemoveRefusesUnmanagedWithoutForce(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	handmade := filepath.Join(env.Paths.UserFeaturesDir, "handmade")
	writeFixture(t, filepath.Join(handmade, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: handmade\n", 0o644)

	results, err := Remove(context.Background(), env, removeRequest("handmade"))
	if err == nil && !HasFailure(results) {
		t.Fatalf("Remove deleted an unmanaged extension: %s", fmtResults(results))
	}
	message := ""
	if err != nil {
		message = err.Error()
	} else {
		message = results[0].Error
	}
	if !strings.Contains(message, "not installed by enclave") {
		t.Fatalf("error = %q, want it to say the extension was not installed by enclave", message)
	}
	assertResultsDoNotNameExtensions(t, results)
	if _, statErr := os.Stat(handmade); statErr != nil {
		t.Fatalf("unmanaged extension was deleted: %v", statErr)
	}

	forced := removeRequest("handmade")
	forced.Force = true
	results, err = Remove(context.Background(), env, forced)
	if err != nil {
		t.Fatalf("Remove --force: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionRemoved {
		t.Fatalf("results = %s", fmtResults(results))
	}
}

func TestRemoveRefusesCorruptSidecarWithoutForce(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	broken := filepath.Join(env.Paths.UserFeaturesDir, "broken")
	writeFixture(t, filepath.Join(broken, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: broken\n", 0o644)
	writeFixture(t, filepath.Join(broken, model.ExtensionSourceFilename), "{not valid json", 0o644)

	results, err := Remove(context.Background(), env, removeRequest("broken"))
	if err == nil && !HasFailure(results) {
		t.Fatalf("Remove deleted an extension with an unreadable sidecar: %s", fmtResults(results))
	}
	message := ""
	if err != nil {
		message = err.Error()
	} else {
		message = results[0].Error
	}
	if !strings.Contains(message, "provenance file could not be read") {
		t.Fatalf("error = %q, want it to distinguish an unreadable sidecar from a genuinely unmanaged extension", message)
	}
	if strings.Contains(message, "not installed by enclave") {
		t.Fatalf("error = %q, a corrupt sidecar is not the same as never having been installed", message)
	}
	assertResultsDoNotNameExtensions(t, results)
	if _, statErr := os.Stat(broken); statErr != nil {
		t.Fatalf("extension with unreadable sidecar was deleted: %v", statErr)
	}

	forced := removeRequest("broken")
	forced.Force = true
	results, err = Remove(context.Background(), env, forced)
	if err != nil {
		t.Fatalf("Remove --force: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionRemoved {
		t.Fatalf("results = %s", fmtResults(results))
	}
}

// TestRemoveNeverReadsInstalledContent pins that removal consults an
// extension's provenance and nothing else. Hashing the installed tree would
// let the unreadable dangling symlink below downgrade a perfectly managed
// extension to "provenance could not be read; pass --force".
func TestRemoveNeverReadsInstalledContent(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	installed := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	if err := os.Symlink(filepath.Join(installed, "gone"), filepath.Join(installed, "dangling")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	results, err := Remove(context.Background(), env, removeRequest("foo"))
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionRemoved {
		t.Fatalf("results = %s, want the extension removed without reading its content", fmtResults(results))
	}
}

func TestRemoveRefusesBuiltinOnly(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	writeFixture(t, filepath.Join(env.Paths.FeaturesDir, "builtin", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: mixin\nname: builtin\n", 0o644)

	results, err := Remove(context.Background(), env, removeRequest("builtin"))
	if err == nil && !HasFailure(results) {
		t.Fatalf("Remove accepted a built-in: %s", fmtResults(results))
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.FeaturesDir, "builtin", "spec.yaml")); statErr != nil {
		t.Fatalf("built-in was touched: %v", statErr)
	}
}

func TestRemoveOverlayReportsBuiltinRestored(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, out := testEnv(t, fetcher, "")
	writeFixture(t, filepath.Join(env.Paths.FeaturesDir, "foo", "spec.yaml"), fooFeatureSpec, 0o644)
	// Overlaying a built-in requires --force: installing "foo" here would
	// otherwise shadow the built-in fixture just written above.
	req := addRequest()
	req.Force = true
	if _, err := Add(context.Background(), env, req); err != nil {
		t.Fatalf("seed install: %v", err)
	}

	if _, err := Remove(context.Background(), env, removeRequest("foo")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !strings.Contains(out.String(), "built-in") {
		t.Fatalf("removal did not mention the restored built-in:\n%s", out.String())
	}
}

func TestRemoveUnknownName(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	if _, err := Remove(context.Background(), env, removeRequest("nope")); err == nil {
		t.Fatal("Remove accepted a name that is not installed")
	}
}

func TestRemoveRequiresNames(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	if _, err := Remove(context.Background(), env, removeRequest()); err == nil {
		t.Fatal("Remove with no names was accepted")
	}
}

func TestRemoveDeclinedKeepsFiles(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "n\n")
	installFoo(t, env)

	req := removeRequest("foo")
	req.Yes = false
	req.Interactive = true
	results, err := Remove(context.Background(), env, req)
	if err == nil && !HasFailure(results) {
		t.Fatalf("declining still removed: %s", fmtResults(results))
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); statErr != nil {
		t.Fatalf("declined removal deleted files: %v", statErr)
	}
}
