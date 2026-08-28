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

// TestUpdateSharesOneCheckoutAgainstRealGit covers what the fake fetcher
// cannot: several extensions from one repository at one ref are updated from a
// single fetched checkout, so each of them materializes its own directories
// out of a working tree another extension already materialized from.
func TestUpdateSharesOneCheckoutAgainstRealGit(t *testing.T) {
	specFor := func(name string, description string) string {
		return "schemaVersion: \"1\"\nkind: mixin\nname: " + name + "\ndescription: " + description + "\n"
	}
	repo := fixtureRepo(t, true, map[string]string{
		"extensions/features/alpha/spec.yaml":  specFor("alpha", "v1"),
		"extensions/features/alpha/install.sh": "#!/bin/sh\necho alpha\n",
		"extensions/features/beta/spec.yaml":   specFor("beta", "v1"),
		"extensions/features/beta/install.sh":  "#!/bin/sh\necho beta\n",
		"website/large.txt":                    "not an extension\n",
	})
	fetcher, err := NewGitFetcher()
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	env, _ := testEnv(t, fetcher, "")

	if _, err := Add(context.Background(), env, Request{Kind: model.KindFeature, Op: OpAdd, Source: repo, All: true, Yes: true}); err != nil {
		t.Fatalf("Add --all: %v", err)
	}

	for _, name := range []string{"alpha", "beta"} {
		writeFixture(t, filepath.Join(repo, "extensions", "features", name, "spec.yaml"), specFor(name, "v2"), 0o644)
	}
	runGitFixture(t, repo, "add", "-A")
	runGitFixture(t, repo, "commit", "-qm", "second")

	results, err := Update(context.Background(), env, Request{Kind: model.KindFeature, Op: OpUpdate, Yes: true})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %s, want both extensions updated", fmtResults(results))
	}
	for _, result := range results {
		if result.Action != ActionUpdated {
			t.Fatalf("results = %s, want both extensions updated", fmtResults(results))
		}
	}
	for _, name := range []string{"alpha", "beta"} {
		installed := filepath.Join(env.Paths.UserFeaturesDir, name)
		content, readErr := os.ReadFile(filepath.Join(installed, "spec.yaml"))
		if readErr != nil {
			t.Fatalf("read %s spec: %v", name, readErr)
		}
		if !strings.Contains(string(content), "v2") {
			t.Fatalf("%s was not updated:\n%s", name, content)
		}
		if _, statErr := os.Stat(filepath.Join(installed, "install.sh")); statErr != nil {
			t.Fatalf("%s install.sh missing after the shared update: %v", name, statErr)
		}
		if _, statErr := os.Stat(filepath.Join(installed, "website")); !os.IsNotExist(statErr) {
			t.Fatalf("%s picked up unrelated repository content", name)
		}
	}
}

// TestAddRootExtensionKeepsItsSubdirectoriesAgainstRealGit covers a repository
// that is itself an extension and also holds nested ones. The root extension
// owns the whole tree, so no set of subdirectories describes it and the
// checkout must not be narrowed: a sparse cone naming only the nested
// directories brings in the root's files but none of its subdirectories.
func TestAddRootExtensionKeepsItsSubdirectoriesAgainstRealGit(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{
		"spec.yaml":              "schemaVersion: \"1\"\nkind: mixin\nname: rootfeat\ndescription: Root\n",
		"install.sh":             "#!/bin/sh\necho root\n",
		"skills/demo/SKILL.md":   "# demo\n",
		"nested/other/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: other\ndescription: Other\n",
	})
	fetcher, err := NewGitFetcher()
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	env, _ := testEnv(t, fetcher, "")

	req := Request{Kind: model.KindFeature, Op: OpAdd, Source: repo, Names: []string{"rootfeat"}, Yes: true}
	results, err := Add(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s", fmtResults(results))
	}
	installed := filepath.Join(env.Paths.UserFeaturesDir, "rootfeat")
	for _, rel := range []string{"install.sh", filepath.Join("skills", "demo", "SKILL.md")} {
		if _, statErr := os.Stat(filepath.Join(installed, rel)); statErr != nil {
			t.Fatalf("root extension installed without %s: %v", rel, statErr)
		}
	}
}

// TestAddUpdateRemoveAgainstRealGit exercises the whole lifecycle against an
// actual git repository, so the fetcher's fallback ladder and the Update
// ls-remote short-circuit are covered by more than a fake. It does not exercise
// sparse checkout itself: Add/Update/Remove never hand the opened Repo back to
// the caller, so this test has no legitimate way to inspect whether the git
// working tree was materialized sparsely or in full. Sparse materialization
// (the `sparse`/`blobless` bookkeeping and the absence of unrelated files in
// the checkout itself) is covered directly in fetch_test.go by
// TestGitFetcherMaterializeSubset and TestGitFetcherFallsBackWhenFilterUnsupported.
func TestAddUpdateRemoveAgainstRealGit(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{
		"extensions/features/demo/spec.yaml":  "schemaVersion: \"1\"\nkind: mixin\nname: demo\ndescription: Demo\n",
		"extensions/features/demo/install.sh": "#!/bin/sh\necho demo\n",
		"website/large.txt":                   "not an extension\n",
	})
	fetcher, err := NewGitFetcher()
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	env, _ := testEnv(t, fetcher, "")

	req := Request{Kind: model.KindFeature, Op: OpAdd, Source: repo, Yes: true}
	results, err := Add(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s", fmtResults(results))
	}
	installed := filepath.Join(env.Paths.UserFeaturesDir, "demo")
	if _, statErr := os.Stat(filepath.Join(installed, "install.sh")); statErr != nil {
		t.Fatalf("install.sh missing: %v", statErr)
	}
	// This proves the installed layout is scoped to the selected extension
	// directory (applyPlan/copyExtensionTree copy only plan.RepoDir), not that
	// the git checkout itself was sparse; see the doc comment above.
	if _, statErr := os.Stat(filepath.Join(installed, "website")); !os.IsNotExist(statErr) {
		t.Fatal("unrelated repository content was installed")
	}

	// Nothing changed upstream: update must not rewrite anything.
	updateResults, err := Update(context.Background(), env, Request{Kind: model.KindFeature, Op: OpUpdate, Yes: true})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updateResults) != 1 || updateResults[0].Action != ActionUnchanged {
		t.Fatalf("update results = %s", fmtResults(updateResults))
	}

	// Move the remote forward and update again.
	writeFixture(t, filepath.Join(repo, "extensions", "features", "demo", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: mixin\nname: demo\ndescription: Demo v2\n", 0o644)
	runGitFixture(t, repo, "add", "-A")
	runGitFixture(t, repo, "commit", "-qm", "second")

	updateResults, err = Update(context.Background(), env, Request{Kind: model.KindFeature, Op: OpUpdate, Yes: true})
	if err != nil {
		t.Fatalf("Update after upstream change: %v", err)
	}
	if len(updateResults) != 1 || updateResults[0].Action != ActionUpdated {
		t.Fatalf("update results = %s", fmtResults(updateResults))
	}
	content, err := os.ReadFile(filepath.Join(installed, "spec.yaml"))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(content), "Demo v2") {
		t.Fatalf("spec was not updated:\n%s", content)
	}

	removeResults, err := Remove(context.Background(), env, Request{Kind: model.KindFeature, Op: OpRemove, Names: []string{"demo"}, Yes: true})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(removeResults) != 1 || removeResults[0].Action != ActionRemoved {
		t.Fatalf("remove results = %s", fmtResults(removeResults))
	}
	if _, statErr := os.Stat(installed); !os.IsNotExist(statErr) {
		t.Fatal("extension survived removal")
	}
}
