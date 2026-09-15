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

// installFoo puts a managed "foo" feature in place by running Add, so update
// tests start from exactly what a real install produces.
func installFoo(t *testing.T, env Env) {
	t.Helper()
	if _, err := Add(context.Background(), env, addRequest()); err != nil {
		t.Fatalf("seed install: %v", err)
	}
}

func updateRequest(names ...string) Request {
	return Request{Kind: model.KindFeature, Op: OpUpdate, Names: names, Yes: true}
}

func TestUpdateSkipsWhenCommitUnchanged(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	opensBefore := fetcher.opens
	results, err := Update(context.Background(), env, updateRequest())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUnchanged {
		t.Fatalf("results = %s", fmtResults(results))
	}
	if fetcher.opens != opensBefore {
		t.Fatalf("Update fetched the repository despite an unchanged commit (opens %d -> %d)", opensBefore, fetcher.opens)
	}
}

func TestUpdateAppliesNewCommit(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	// Advance the fake remote: new commit, new content.
	updated := fooRepoFiles()
	updated["extensions/features/foo/spec.yaml"] = fooFeatureSpec + "aptPackages:\n  - jq\n"
	next := newFakeFetcher(t, "b2c3d4e5", updated)
	env.Fetcher = next

	results, err := Update(context.Background(), env, updateRequest())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("results = %s", fmtResults(results))
	}
	installed := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	origin, err := readOrigin(installed)
	if err != nil || origin == nil {
		t.Fatalf("readOrigin: %v, %v", origin, err)
	}
	if origin.Commit != "b2c3d4e5" {
		t.Fatalf("origin commit = %q, want the new commit", origin.Commit)
	}
	// An update stamps the tree hash it computed while diffing the staged tree
	// rather than reading the installed directory a second time, so that hash
	// must still describe what the swap put on disk.
	modified, err := isLocallyModified(installed, *origin)
	if err != nil {
		t.Fatalf("isLocallyModified: %v", err)
	}
	if modified {
		t.Fatal("a freshly updated extension reports local modifications")
	}
	content, err := os.ReadFile(filepath.Join(installed, "spec.yaml"))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(content), "jq") {
		t.Fatal("updated content was not installed")
	}
}

// TestUpdateUnchangedContentReportsNoCapabilityDiff: the installed ("before")
// directory carries a provenance sidecar that the staged ("after") tree never
// has, so file/byte counts must exclude it or every update reports a diff of
// exactly the sidecar's size. A new commit whose content is byte-identical
// must report an empty diff.
func TestUpdateUnchangedContentReportsNoCapabilityDiff(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	// Advance the fake remote to a new commit with byte-identical content, so
	// the update proceeds (new commit) but nothing about the extension itself
	// changed.
	next := newFakeFetcher(t, "b2c3d4e5", fooRepoFiles())
	env.Fetcher = next
	out := &bytesBuffer{}
	env.Narration = out

	results, err := Update(context.Background(), env, updateRequest())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("results = %s", fmtResults(results))
	}
	if !strings.Contains(out.String(), "no capability changes") {
		t.Fatalf("update with byte-identical content did not report an empty capability diff:\n%s", out.String())
	}
}

// TestUpdateRebuildHintTracksContent: the image identity hash is content-based
// and excludes the provenance sidecar, so re-pinning to a new commit whose
// content is byte-identical cannot move it and no rebuild follows. Promising
// one either way would be a claim the build never honours.
func TestUpdateRebuildHintTracksContent(t *testing.T) {
	const rebuildHint = "rebuilds the image"
	for _, tc := range []struct {
		name        string
		spec        string
		wantRebuild bool
	}{
		{"identical content", fooFeatureSpec, false},
		{"changed content", fooFeatureSpec + "aptPackages:\n  - jq\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", fooRepoFiles()), "")
			installFoo(t, env)

			next := fooRepoFiles()
			next["extensions/features/foo/spec.yaml"] = tc.spec
			env.Fetcher = newFakeFetcher(t, "b2c3d4e5", next)
			out := &bytesBuffer{}
			env.Narration = out

			results, err := Update(context.Background(), env, updateRequest())
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if len(results) != 1 || results[0].Action != ActionUpdated {
				t.Fatalf("results = %s", fmtResults(results))
			}
			if got := strings.Contains(out.String(), rebuildHint); got != tc.wantRebuild {
				t.Fatalf("promised a rebuild = %v, want %v:\n%s", got, tc.wantRebuild, out.String())
			}
		})
	}
}

// TestUpdateShowsChangedFileList: an update must show the changed-file list
// and the capability diff, not the capability diff alone. A new commit that
// adds a file, removes a file, and modifies a third must list all three with
// their status.
func TestUpdateShowsChangedFileList(t *testing.T) {
	original := fooRepoFiles()
	original["extensions/features/foo/old.txt"] = "will be removed\n"
	fetcher := newFakeFetcher(t, "a1b2c3d4", original)
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	updated := map[string]string{
		"extensions/features/foo/spec.yaml":  fooFeatureSpec,              // unchanged
		"extensions/features/foo/install.sh": "#!/bin/sh\necho updated\n", // modified
		"extensions/features/foo/new.txt":    "added\n",                   // added
		// old.txt dropped: removed
	}
	out := &bytesBuffer{}
	env.Fetcher = newFakeFetcher(t, "b2c3d4e5", updated)
	env.Narration = out

	results, err := Update(context.Background(), env, updateRequest())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("results = %s", fmtResults(results))
	}
	// The status is padded into a column, so the assertions match on the
	// status and its path rather than on the run of spaces between them.
	rendered := out.String()
	for _, want := range []string{"file(s) changed", "modified install.sh", "added    new.txt", "removed  old.txt"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("changed-file list missing %q:\n%s", want, rendered)
		}
	}
}

func TestUpdateRefusesLocalModifications(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	installed := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	writeFixture(t, filepath.Join(installed, "local.sh"), "echo mine\n", 0o644)

	results, err := Update(context.Background(), env, updateRequest())
	if err == nil && !HasFailure(results) {
		t.Fatalf("Update clobbered local edits: %s", fmtResults(results))
	}
	if _, statErr := os.Stat(filepath.Join(installed, "local.sh")); statErr != nil {
		t.Fatalf("local edit was destroyed: %v", statErr)
	}

	forced := updateRequest()
	forced.Force = true
	results, err = Update(context.Background(), env, forced)
	if err != nil {
		t.Fatalf("Update --force: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("results = %s", fmtResults(results))
	}
	if _, statErr := os.Stat(filepath.Join(installed, "local.sh")); !os.IsNotExist(statErr) {
		t.Fatal("--force did not discard the local edit")
	}
}

func TestUpdatePinnedCommitNeedsNoNetwork(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0", fooRepoFiles())
	fetcher.repo.refType = RefTypeCommit
	fetcher.repo.ref = fetcher.repo.commit
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	resolvesBefore, opensBefore := fetcher.resolves, fetcher.opens
	results, err := Update(context.Background(), env, updateRequest())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUnchanged {
		t.Fatalf("results = %s", fmtResults(results))
	}
	if fetcher.resolves != resolvesBefore || fetcher.opens != opensBefore {
		t.Fatalf("pinned update touched the network (resolves %d->%d, opens %d->%d)",
			resolvesBefore, fetcher.resolves, opensBefore, fetcher.opens)
	}
}

// TestUpdateForcedIgnoresUnreadableInstalledContent pins that --force replaces
// content it cannot hash: it discards local edits whatever they are, so the
// unreadable dangling symlink below must not turn into a failed update.
func TestUpdateForcedIgnoresUnreadableInstalledContent(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)

	installed := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	if err := os.Symlink(filepath.Join(installed, "gone"), filepath.Join(installed, "dangling")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	req := updateRequest()
	req.Force = true
	results, err := Update(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Update --force: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("results = %s, want a forced update", fmtResults(results))
	}
	if _, statErr := os.Lstat(filepath.Join(installed, "dangling")); !os.IsNotExist(statErr) {
		t.Fatal("--force did not replace the installed tree")
	}
}

// TestUpdateFetchesEachRemoteRefOnce covers the common "one kit repository,
// several extensions" layout: every target of a bare update comes from the
// same remote at the same ref, so the whole run costs one reference resolution
// and one fetch, not one of each per extension.
func TestUpdateFetchesEachRemoteRefOnce(t *testing.T) {
	kit := func(marker string) map[string]string {
		files := map[string]string{}
		for _, name := range []string{"alpha", "beta", "gamma"} {
			dir := "extensions/features/" + name
			files[dir+"/spec.yaml"] = "schemaVersion: \"1\"\nkind: mixin\nname: " + name + "\n"
			files[dir+"/install.sh"] = "#!/bin/sh\necho " + marker + "\n"
		}
		return files
	}
	env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", kit("first")), "")
	seed := addRequest()
	seed.All = true
	if _, err := Add(context.Background(), env, seed); err != nil {
		t.Fatalf("seed install: %v", err)
	}

	next := newFakeFetcher(t, "b2c3d4e5", kit("second"))
	env.Fetcher = next

	results, err := Update(context.Background(), env, updateRequest())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %s, want all three extensions updated", fmtResults(results))
	}
	for _, result := range results {
		if result.Action != ActionUpdated {
			t.Fatalf("results = %s, want all three extensions updated", fmtResults(results))
		}
	}
	if next.resolves != 1 {
		t.Errorf("resolves = %d, want the shared remote ref resolved once", next.resolves)
	}
	if next.opens != 1 {
		t.Errorf("opens = %d, want the shared remote ref fetched once", next.opens)
	}
}

func TestUpdateSkipsUnmanagedExtensions(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	writeFixture(t, filepath.Join(env.Paths.UserFeaturesDir, "handmade", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: mixin\nname: handmade\n", 0o644)

	results, err := Update(context.Background(), env, updateRequest())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Name != "handmade" || results[0].Action != ActionSkipped {
		t.Fatalf("results = %s, want handmade reported as skipped", fmtResults(results))
	}
	assertResultsDoNotNameExtensions(t, results)
}

func TestUpdateSkipsCorruptSidecar(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	broken := filepath.Join(env.Paths.UserFeaturesDir, "broken")
	writeFixture(t, filepath.Join(broken, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: broken\n", 0o644)
	writeFixture(t, filepath.Join(broken, model.ExtensionSourceFilename), "{not valid json", 0o644)

	results, err := Update(context.Background(), env, updateRequest())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Name != "broken" || results[0].Action != ActionSkipped {
		t.Fatalf("results = %s, want broken reported as skipped", fmtResults(results))
	}
	if !strings.Contains(results[0].Error, "provenance file could not be read") {
		t.Fatalf("Error = %q, want it to distinguish an unreadable sidecar from a genuinely unmanaged extension", results[0].Error)
	}
	if strings.Contains(results[0].Error, "not installed by enclave") {
		t.Fatalf("Error = %q, a corrupt sidecar is not the same as never having been installed", results[0].Error)
	}
	assertResultsDoNotNameExtensions(t, results)
}

func TestUpdateRefRejectedForMultipleTargets(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	installFoo(t, env)
	// Seed a second managed extension so the --ref check, not the
	// unknown-name check, is what this test exercises.
	barDir := filepath.Join(env.Paths.UserFeaturesDir, "bar")
	writeFixture(t, filepath.Join(barDir, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: bar\n", 0o644)
	if err := WriteOrigin(barDir, Origin{
		SchemaVersion: OriginSchemaVersion,
		Kind:          string(model.KindFeature),
		Name:          "bar",
		Remote:        "https://github.com/acme/bar",
		Source:        "acme/bar",
		Ref:           "main",
		RefType:       RefTypeBranch,
		Commit:        "c3d4e5f6",
		InstalledAt:   "2026-08-01T00:00:00Z",
		InstalledBy:   "enclave test",
	}); err != nil {
		t.Fatalf("seed bar origin: %v", err)
	}

	req := updateRequest("foo", "bar")
	req.Ref = "v2"
	if _, err := Update(context.Background(), env, req); err == nil {
		t.Fatal("--ref with several targets was accepted")
	}
}

// TestUpdateFailsExplicitlyWhenSourceNoLongerProvidesTheName: when none of
// the fetched candidates matches the installed name, updateOne must fail
// rather than update a different extension from the same source.
//
// "foo" is installed from a repository-root spec (Subpath ""), so update's
// candidateDirs call re-scans the whole repository rather than one fixed
// path; the remote is then advanced to a tree that still has one matching
// mixin, just declared under a different name and directory ("renamed", not
// "foo").
func TestUpdateFailsExplicitlyWhenSourceNoLongerProvidesTheName(t *testing.T) {
	rootFoo := map[string]string{
		"spec.yaml":  "schemaVersion: \"1\"\nkind: mixin\nname: foo\n",
		"install.sh": "#!/bin/sh\necho foo\n",
	}
	fetcher := newFakeFetcher(t, "a1b2c3d4", rootFoo)
	env, _ := testEnv(t, fetcher, "")
	if _, err := Add(context.Background(), env, addRequest()); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if origin, err := readOrigin(filepath.Join(env.Paths.UserFeaturesDir, "foo")); err != nil || origin == nil || origin.Subpath != "" {
		t.Fatalf("precondition: expected a root-installed foo with empty Subpath, got %+v, %v", origin, err)
	}

	renamed := map[string]string{
		"extensions/features/renamed/spec.yaml":  "schemaVersion: \"1\"\nkind: mixin\nname: renamed\n",
		"extensions/features/renamed/install.sh": "#!/bin/sh\necho renamed\n",
	}
	env.Fetcher = newFakeFetcher(t, "b2c3d4e5", renamed)

	results, err := Update(context.Background(), env, updateRequest())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionFailed {
		t.Fatalf("results = %s, want an explicit failure", fmtResults(results))
	}
	if results[0].Name != "foo" {
		t.Fatalf("results = %s, want the failure attributed to foo", fmtResults(results))
	}
	assertResultsDoNotNameExtensions(t, results)
	// The installed "foo" must be untouched, not silently replaced by "renamed".
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo", "spec.yaml")); statErr != nil {
		t.Fatalf("installed extension was touched by the failed update: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "renamed")); !os.IsNotExist(statErr) {
		t.Fatal("update silently installed the wrong extension under the old name")
	}
}

func TestUpdateUnknownName(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	if _, err := Update(context.Background(), env, updateRequest("missing")); err == nil {
		t.Fatal("Update accepted an unknown name")
	}
}
