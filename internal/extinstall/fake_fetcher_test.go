// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"enclave/internal/model"
)

// fakeRepo serves a fixed file tree from a temp directory. Materialize is a
// no-op because everything is already on disk, which is exactly the observable
// contract a sparse checkout provides.
type fakeRepo struct {
	dir     string
	commit  string
	ref     string
	refType string
	files   []string
}

func (r *fakeRepo) Commit() string  { return r.commit }
func (r *fakeRepo) Ref() string     { return r.ref }
func (r *fakeRepo) RefType() string { return r.refType }
func (r *fakeRepo) Files() []string { return r.files }
func (r *fakeRepo) Dir() string     { return r.dir }
func (r *fakeRepo) Close()          {}

func (r *fakeRepo) Materialize(context.Context, []string) error { return nil }

type fakeFetcher struct {
	repo     *fakeRepo
	openErr  error
	resolves int
	opens    int
}

func (f *fakeFetcher) ResolveRef(_ context.Context, _ string, ref string) (RemoteRef, error) {
	f.resolves++
	if f.openErr != nil {
		return RemoteRef{}, f.openErr
	}
	resolved := RemoteRef{Commit: f.repo.commit, Ref: f.repo.ref, RefType: f.repo.refType}
	if ref != "" && ref != f.repo.ref {
		resolved.Ref = ref
	}
	return resolved, nil
}

func (f *fakeFetcher) Open(ctx context.Context, remote string, ref string) (Repo, error) {
	resolved, err := f.ResolveRef(ctx, remote, ref)
	if err != nil {
		return nil, err
	}
	return f.OpenAt(ctx, remote, resolved)
}

func (f *fakeFetcher) OpenAt(_ context.Context, _ string, _ RemoteRef) (Repo, error) {
	f.opens++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.repo, nil
}

// newFakeFetcher writes files into a temp dir and serves them as one commit.
func newFakeFetcher(t *testing.T, commit string, files map[string]string) *fakeFetcher {
	t.Helper()
	dir := t.TempDir()
	names := make([]string, 0, len(files))
	for rel, content := range files {
		writeFixture(t, filepath.Join(dir, filepath.FromSlash(rel)), content, 0o644)
		names = append(names, rel)
	}
	sort.Strings(names)
	return &fakeFetcher{repo: &fakeRepo{dir: dir, commit: commit, ref: "main", refType: RefTypeBranch, files: names}}
}

// testEnv builds an Env rooted in a temp home with empty built-in extension
// dirs, so installs and validation see only what a test puts there.
func testEnv(t *testing.T, fetcher Fetcher, stdin string) (Env, *bytesBuffer) {
	t.Helper()
	root := t.TempDir()
	paths := model.Paths{
		ToolsDir:          filepath.Join(root, "app", "extensions", "tools"),
		FeaturesDir:       filepath.Join(root, "app", "extensions", "features"),
		UserExtensionsDir: filepath.Join(root, "home", ".config", "enclave", "extensions"),
		UserToolsDir:      filepath.Join(root, "home", ".config", "enclave", "extensions", "tools"),
		UserFeaturesDir:   filepath.Join(root, "home", ".config", "enclave", "extensions", "features"),
	}
	for _, dir := range []string{paths.ToolsDir, paths.FeaturesDir, paths.UserToolsDir, paths.UserFeaturesDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	out := &bytesBuffer{}
	return Env{
		Paths:     paths,
		Home:      filepath.Join(root, "home"),
		Fetcher:   fetcher,
		Stdin:     stringReader(stdin),
		Narration: out,
		Now:       func() time.Time { return time.Date(2026, 8, 26, 10, 15, 0, 0, time.UTC) },
		Version:   "enclave test",
	}, out
}

// assertResultsDoNotNameExtensions is the invariant guard for per-extension
// error text: ActionResult.Name is the only place an extension is named, so a
// caller can prefix it onto Error unconditionally.
func assertResultsDoNotNameExtensions(t *testing.T, results []ActionResult) {
	t.Helper()
	for _, result := range results {
		if result.Name == "" || result.Error == "" {
			continue
		}
		if strings.Contains(result.Error, result.Name) {
			t.Errorf("error for %q repeats the extension name: %q", result.Name, result.Error)
		}
	}
}

func fmtResults(results []ActionResult) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		parts = append(parts, fmt.Sprintf("%s=%s(%s)", result.Name, result.Action, result.Error))
	}
	return fmt.Sprint(parts)
}

type bytesBuffer = bytes.Buffer

func stringReader(value string) io.Reader { return strings.NewReader(value) }
