// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"context"
	"enclave/internal/model"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostHookInstallInventoryUpdateRemoveNeverExecute(t *testing.T) {
	files := map[string]string{
		"extensions/features/host-test/spec.yaml":                "schemaVersion: \"1\"\nkind: mixin\nname: host-test\ndefaultEnabled: false\n",
		"extensions/features/host-test/hooks/host/manifest.json": `{"schemaVersion":"1","phases":{"prepare":"prepare"}}`,
		"extensions/features/host-test/hooks/host/prepare":       "#!/bin/sh\ntouch SHOULD_NOT_EXIST\n",
	}
	fetcher := newFakeFetcher(t, "abc111", files)
	executable := filepath.Join(fetcher.repo.dir, "extensions/features/host-test/hooks/host/prepare")
	if err := os.Chmod(executable, 0755); err != nil {
		t.Fatal(err)
	}
	env, out := testEnv(t, fetcher, "")
	req := Request{Kind: model.KindFeature, Op: OpAdd, Source: "acme/kits", Names: []string{"host-test"}, Yes: true, DryRun: true}
	results, err := Add(context.Background(), env, req)
	if err != nil || HasFailure(results) || len(results[0].HostHooks) != 1 {
		t.Fatalf("dry-run: %+v %v", results, err)
	}
	req.DryRun = false
	results, err = Add(context.Background(), env, req)
	if err != nil || HasFailure(results) || len(results[0].HostHooks) != 1 {
		t.Fatalf("add: %+v %v", results, err)
	}
	installed := filepath.Join(env.Paths.UserFeaturesDir, "host-test")
	inventory, err := Inventory(env.Paths, model.KindFeature, nil, InventoryModifications)
	if err != nil || len(inventory["host-test"].HostHooks) != 1 || inventory["host-test"].Modified {
		t.Fatalf("inventory %+v %v", inventory, err)
	}
	if strings.Contains(out.String(), "the next run rebuilds") || strings.Contains(out.String(), "--features +host-test") || !strings.Contains(out.String(), "host_hooks") {
		t.Fatalf("hint: %s", out.String())
	}
	out.Reset()
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ntouch SHOULD_NOT_EXIST # version two\n"), 0755); err != nil {
		t.Fatal(err)
	}
	fetcher.repo.commit = "abc222"
	results, err = Update(context.Background(), env, Request{Kind: model.KindFeature, Op: OpUpdate, Yes: true})
	if err != nil || HasFailure(results) || len(results[0].HostHooks) != 1 {
		t.Fatalf("update %+v %v", results, err)
	}
	if strings.Contains(out.String(), "the next run rebuilds") || !strings.Contains(out.String(), "host hook handler/content") {
		t.Fatalf("update diff: %s", out.String())
	}
	for _, dir := range []string{installed, filepath.Dir(executable)} {
		if _, err := os.Stat(filepath.Join(dir, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
			t.Fatal("installer executed hook")
		}
	}
	// Host changes are part of integrity, even though they never reach the image.
	if err := os.WriteFile(filepath.Join(installed, "hooks/host/prepare"), []byte("#!/bin/sh\n# local edit\n"), 0755); err != nil {
		t.Fatal(err)
	}
	inventory, err = Inventory(env.Paths, model.KindFeature, nil, InventoryModifications)
	if err != nil || !inventory["host-test"].Modified {
		t.Fatal("host local edit not detected", err)
	}
	results, err = Remove(context.Background(), env, Request{Kind: model.KindFeature, Op: OpRemove, Names: []string{"host-test"}, Yes: true, Force: true})
	if err != nil || HasFailure(results) || len(results[0].HostHooks) != 1 {
		t.Fatalf("remove %+v %v", results, err)
	}
}
func TestInvalidHostManifestRejectedAndInventoried(t *testing.T) {
	files := map[string]string{"extensions/features/bad/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: bad\n", "extensions/features/bad/hooks/host/manifest.json": `{"schemaVersion":"99","phases":{"prepare":"prepare"}}`}
	env, _ := testEnv(t, newFakeFetcher(t, "abc", files), "")
	results, err := Add(context.Background(), env, Request{Kind: model.KindFeature, Op: OpAdd, Source: "acme/kits", Names: []string{"bad"}, Yes: true})
	if err == nil && !HasFailure(results) {
		t.Fatal("invalid manifest installed")
	}
	dir := filepath.Join(env.Paths.UserFeaturesDir, "bad", "hooks", "host")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"schemaVersion":"99"}`), 0644); err != nil {
		t.Fatal(err)
	}
	inventory, err := Inventory(env.Paths, model.KindFeature, nil, InventoryNames)
	if err != nil || len(inventory["bad"].Warnings) == 0 {
		t.Fatal("invalid declaration invisible", err)
	}
}

func TestReferenceHostPackagesInstallFromExplicitGitPath(t *testing.T) {
	source := filepath.Join("..", "..", "examples", "host-hooks", "features")
	files := map[string]string{}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(filepath.Join("examples/host-hooks/features", rel))] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"herdr", "session-tint", "session-title"}
	repo := fixtureRepo(t, true, files,
		"examples/host-hooks/features/herdr/hooks/host/prepare",
		"examples/host-hooks/features/session-tint/hooks/host/terminal-enter",
		"examples/host-hooks/features/session-title/hooks/host/terminal-enter",
	)
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skip(err)
	}
	env, _ := testEnv(t, fetcher, "")
	for _, name := range names {
		results, err := Add(context.Background(), env, Request{Kind: model.KindFeature, Op: OpAdd, Source: repo, Path: "examples/host-hooks/features/" + name, Yes: true})
		if err != nil || HasFailure(results) || len(results) != 1 || len(results[0].HostHooks) != 1 || results[0].HostHooks[0].ID != "feature:"+name {
			t.Fatalf("install %s: %+v %v", name, results, err)
		}
	}
	inventory, err := Inventory(env.Paths, model.KindFeature, nil, InventoryModifications)
	if err != nil || len(inventory) != len(names) {
		t.Fatal(inventory, err)
	}
	for name, entry := range inventory {
		if entry.Modified || len(entry.HostHooks) != 1 || entry.Origin == nil {
			t.Fatal(name, entry)
		}
	}
	results, err := Update(context.Background(), env, Request{Kind: model.KindFeature, Op: OpUpdate, Yes: true})
	if err != nil || HasFailure(results) || len(results) != len(names) {
		t.Fatal(results, err)
	}
	for _, result := range results {
		if result.Action != ActionUnchanged || len(result.HostHooks) != 1 {
			t.Fatal(result)
		}
	}
	results, err = Remove(context.Background(), env, Request{Kind: model.KindFeature, Op: OpRemove, Names: names, Yes: true})
	if err != nil || HasFailure(results) || len(results) != len(names) {
		t.Fatal(results, err)
	}
	for _, result := range results {
		if result.Action != ActionRemoved || len(result.HostHooks) != 1 {
			t.Fatal(result)
		}
	}
}
