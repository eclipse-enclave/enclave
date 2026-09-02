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
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runGitFixture executes git in dir and fails the test on error.
func runGitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// fixtureRepo builds a real git repository with the given files and returns its
// path. allowFilter controls uploadpack.allowFilter, so both the partial-fetch
// path and its fallback can be exercised.
func fixtureRepo(t *testing.T, allowFilter bool, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGitFixture(t, dir, "init", "-q")
	runGitFixture(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	for rel, content := range files {
		writeFixture(t, filepath.Join(dir, filepath.FromSlash(rel)), content, 0o644)
	}
	runGitFixture(t, dir, "add", "-A")
	runGitFixture(t, dir, "commit", "-qm", "initial")
	runGitFixture(t, dir, "config", "uploadpack.allowFilter", boolString(allowFilter))
	return dir
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// TestGitFetcherEnvGovernsTerminalPrompts covers the credential prompt: git
// reads it from /dev/tty, so a run reporting into a JSON envelope would block
// on a question nobody sees.
func TestGitFetcherEnvGovernsTerminalPrompts(t *testing.T) {
	// An inherited value would make both halves of this meaningless: exec keeps
	// the last occurrence of a variable, so the assertions are about what env
	// appends, not about what the developer's shell happens to export.
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_ASKPASS", "/usr/bin/some-dialog")

	quiet := &gitFetcher{git: "git"}
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="} {
		if !slices.Contains(quiet.env(), want) {
			t.Errorf("%s missing: git may still ask for credentials in a run that cannot answer", want)
		}
	}
	asking := &gitFetcher{git: "git", allowPrompts: true}
	for _, unwanted := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="} {
		if slices.Contains(asking.env(), unwanted) {
			t.Errorf("%s set: a user cannot answer a question git never asks", unwanted)
		}
	}
}

func TestGitFetcherResolveRefBranch(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{"README.md": "hi\n"})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}

	got, err := fetcher.ResolveRef(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if len(got.Commit) != 40 {
		t.Fatalf("commit = %q, want a full sha", got.Commit)
	}
	if got.Ref != "main" || got.RefType != RefTypeBranch {
		t.Fatalf("ref = %q/%q", got.Ref, got.RefType)
	}
}

func TestGitFetcherResolveRefDefaultHead(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{"README.md": "hi\n"})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}

	got, err := fetcher.ResolveRef(context.Background(), repo, "")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if got.Ref != "main" {
		t.Fatalf("ref = %q, want the branch HEAD points at", got.Ref)
	}
	if got.RefType != RefTypeBranch {
		t.Fatalf("refType = %q, want branch", got.RefType)
	}
}

// TestParseSymrefHeadIgnoresRemoteTrackingHead covers a local clone as a
// source. Such a repository advertises refs/remotes/origin/HEAD next to its
// own HEAD, and only the latter says which branch to follow: pinning to
// refs/remotes/origin/<branch> records a ref no later fetch can resolve, and
// tracks a stale copy rather than the branch that moves.
func TestParseSymrefHeadIgnoresRemoteTrackingHead(t *testing.T) {
	out := strings.Join([]string{
		"ref: refs/heads/main\tHEAD",
		"69dd41f035711f42971f17f29d4b59986700ec2a\tHEAD",
		"ref: refs/remotes/origin/main\trefs/remotes/origin/HEAD",
		"8f0bbc7b0db8fe144e01f5af23e331bc7adbb922\trefs/remotes/origin/HEAD",
	}, "\n")

	got, err := parseSymrefHead(out, "/tmp/clone")
	if err != nil {
		t.Fatalf("parseSymrefHead: %v", err)
	}
	if got.Ref != "main" {
		t.Errorf("ref = %q, want the branch the repository's own HEAD names", got.Ref)
	}
	if got.Commit != "69dd41f035711f42971f17f29d4b59986700ec2a" {
		t.Errorf("commit = %q, want HEAD's commit, not the tracking ref's", got.Commit)
	}
}

// TestGitFetcherResolveRefFromDetachedClone covers a source whose HEAD names no
// branch, which a local clone parked on a tag or a commit does. There is no
// branch to follow, so the install pins the commit: recording a ref named
// "HEAD" would leave every later update looking for a branch or tag of that
// name, which no ls-remote can ever match.
func TestGitFetcherResolveRefFromDetachedClone(t *testing.T) {
	origin := fixtureRepo(t, true, map[string]string{"README.md": "hi\n"})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	runGitFixture(t, "", "clone", "--quiet", origin, clone)
	runGitFixture(t, clone, "checkout", "--quiet", "--detach", "HEAD")

	got, err := fetcher.ResolveRef(context.Background(), clone, "")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if got.RefType != RefTypeCommit {
		t.Fatalf("refType = %q, want commit", got.RefType)
	}
	if got.Ref != got.Commit {
		t.Fatalf("ref = %q, want the commit %q", got.Ref, got.Commit)
	}
	if _, err := fetcher.ResolveRef(context.Background(), clone, got.Ref); err != nil {
		t.Fatalf("the recorded ref does not resolve again: %v", err)
	}
}

// TestGitFetcherResolveRefFromLocalClone is the end-to-end counterpart: a
// clone of a clone still resolves to the branch it is actually on.
func TestGitFetcherResolveRefFromLocalClone(t *testing.T) {
	origin := fixtureRepo(t, true, map[string]string{"README.md": "hi\n"})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	runGitFixture(t, "", "clone", "--quiet", origin, clone)

	got, err := fetcher.ResolveRef(context.Background(), clone, "")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if got.Ref != "main" {
		t.Fatalf("ref = %q, want main", got.Ref)
	}
	if got.RefType != RefTypeBranch {
		t.Fatalf("refType = %q, want branch", got.RefType)
	}
}

func TestGitFetcherResolveRefTagAndCommit(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{"README.md": "hi\n"})
	runGitFixture(t, repo, "tag", "v1.0.0")
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}

	tagRef, err := fetcher.ResolveRef(context.Background(), repo, "v1.0.0")
	if err != nil {
		t.Fatalf("ResolveRef(tag): %v", err)
	}
	if tagRef.RefType != RefTypeTag {
		t.Fatalf("refType = %q, want tag", tagRef.RefType)
	}

	pinned, err := fetcher.ResolveRef(context.Background(), repo, tagRef.Commit)
	if err != nil {
		t.Fatalf("ResolveRef(sha): %v", err)
	}
	if pinned.RefType != RefTypeCommit || pinned.Commit != tagRef.Commit {
		t.Fatalf("pinned = %+v", pinned)
	}
}

func TestGitFetcherOpenListsFilesWithoutMaterializing(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{
		"extensions/features/foo/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: foo\n",
		"website/big.txt":                   "unrelated\n",
	})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}

	opened, err := fetcher.Open(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer opened.Close()

	if !slices.Contains(opened.Files(), "extensions/features/foo/spec.yaml") {
		t.Fatalf("Files() = %v, want the spec path", opened.Files())
	}
	if !slices.Contains(opened.Files(), "website/big.txt") {
		t.Fatalf("Files() = %v, want the full tree listing", opened.Files())
	}
	if len(opened.Commit()) != 40 {
		t.Fatalf("Commit = %q", opened.Commit())
	}
}

func TestGitFetcherMaterializeSubset(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{
		"extensions/features/foo/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: foo\n",
		"website/big.txt":                   "unrelated\n",
	})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	opened, err := fetcher.Open(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer opened.Close()

	if err := opened.Materialize(context.Background(), []string{"extensions/features/foo"}); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opened.Dir(), "extensions", "features", "foo", "spec.yaml")); err != nil {
		t.Fatalf("selected dir not materialized: %v", err)
	}
	// A regression that silently fell back to a full checkout would still
	// produce the file above; only checking for its absence, and the sparse
	// bookkeeping, proves the checkout was actually narrowed.
	if _, err := os.Stat(filepath.Join(opened.Dir(), "website", "big.txt")); !os.IsNotExist(err) {
		t.Fatalf("unrelated file present after Materialize: stat err = %v", err)
	}
	if repoImpl, ok := opened.(*gitRepo); !ok || !repoImpl.sparse {
		t.Fatalf("sparse = %v (ok=%v), want true", ok && repoImpl.sparse, ok)
	}
}

// TestGitFetcherMaterializeHonorsContext proves the checkout runs under the
// caller's context, so cancelling an add or update stops it rather than letting
// a full-repository checkout run to completion.
func TestGitFetcherMaterializeHonorsContext(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{
		"extensions/features/foo/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: foo\n",
	})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	opened, err := fetcher.Open(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer opened.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := opened.Materialize(ctx, []string{"extensions/features/foo"}); err == nil {
		t.Fatal("Materialize succeeded under a cancelled context")
	}
}

// TestGitFetcherListsQuotedPathsVerbatim covers git's default core.quotePath:
// a path with non-ASCII bytes is C-quoted in ls-tree's line-oriented output, so
// a listing parsed as plain lines would hide every extension below it.
func TestGitFetcherListsQuotedPathsVerbatim(t *testing.T) {
	const spec = "extensions/features/日本/spec.yaml"
	repo := fixtureRepo(t, true, map[string]string{spec: "schemaVersion: \"1\"\nkind: mixin\nname: foo\n"})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	opened, err := fetcher.Open(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer opened.Close()

	if got := opened.Files(); len(got) != 1 || got[0] != spec {
		t.Fatalf("Files() = %q, want [%q]", got, spec)
	}
}

func TestGitFetcherFallsBackWhenFilterUnsupported(t *testing.T) {
	repo := fixtureRepo(t, false, map[string]string{
		"extensions/features/foo/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: foo\n",
		"website/big.txt":                   "unrelated\n",
	})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}

	opened, err := fetcher.Open(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("Open with filters disabled: %v", err)
	}
	defer opened.Close()

	repoImpl, ok := opened.(*gitRepo)
	if !ok {
		t.Fatalf("opened = %T, want *gitRepo", opened)
	}
	if repoImpl.blobless {
		t.Fatal("blobless fetch reported success even though the server disallows object filters")
	}

	if err := opened.Materialize(context.Background(), []string{"extensions/features/foo"}); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opened.Dir(), "extensions", "features", "foo", "spec.yaml")); err != nil {
		t.Fatalf("fallback path did not materialize: %v", err)
	}
	// The object-filter fallback and sparse checkout are independent
	// mechanisms: losing the filter must not silently lose the sparse
	// narrowing too.
	if _, err := os.Stat(filepath.Join(opened.Dir(), "website", "big.txt")); !os.IsNotExist(err) {
		t.Fatalf("unrelated file present after Materialize: stat err = %v", err)
	}
	if !repoImpl.sparse {
		t.Fatal("sparse = false, want true: the filter fallback should not have degraded sparse checkout")
	}
}

func TestGitFetcherOpenUnknownRef(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{"README.md": "hi\n"})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	if _, err := fetcher.ResolveRef(context.Background(), repo, "nope"); err == nil {
		t.Fatal("ResolveRef accepted a missing ref")
	}
}

// TestGitFetcherRejectsOptionLikeRemoteHead covers a remote-triggered
// arbitrary-code-execution vector: a hostile repository can point its symbolic
// HEAD at a branch named like a git command-line option (for example
// "--upload-pack=/path/to/payload"), and if that name reaches "git
// fetch" as a bare operand, git runs it as the upload-pack program. This is
// the ordinary no-"--ref" install path (Open(ctx, remote, "")) against an
// untrusted repository, so it must be refused before any fetch is attempted,
// and the payload must never run.
func TestGitFetcherRejectsOptionLikeRemoteHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "pwned")
	payload := filepath.Join(markerDir, "payload.sh")
	writeFixture(t, payload, "#!/bin/sh\ntouch "+marker+"\n", 0o755)

	repoDir := t.TempDir()
	runGitFixture(t, repoDir, "init", "-q")
	runGitFixture(t, repoDir, "symbolic-ref", "HEAD", "refs/heads/--upload-pack="+payload)
	writeFixture(t, filepath.Join(repoDir, "README.md"), "hi\n", 0o644)
	runGitFixture(t, repoDir, "add", "-A")
	runGitFixture(t, repoDir, "commit", "-qm", "initial")

	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}

	if _, err := fetcher.Open(context.Background(), repoDir, ""); err == nil {
		t.Fatal("Open accepted a remote HEAD naming an option-like branch")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("payload executed despite the rejection: marker stat err = %v", statErr)
	}
}

// TestGitFetcherRejectsOptionLikeRequestedRef covers the second place an
// untrusted ref reaches git as a bare operand: an explicit --ref value.
func TestGitFetcherRejectsOptionLikeRequestedRef(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{"README.md": "hi\n"})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	if _, err := fetcher.ResolveRef(context.Background(), repo, "--upload-pack=/tmp/pwn.sh"); err == nil {
		t.Fatal("ResolveRef accepted an option-like requested ref")
	}
}

// TestGitFetcherMaterializeRejectsOptionLikePath covers the same class of
// defect for Materialize's directory list.
func TestGitFetcherMaterializeRejectsOptionLikePath(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{
		"extensions/features/foo/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: foo\n",
	})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	opened, err := fetcher.Open(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer opened.Close()

	if err := opened.Materialize(context.Background(), []string{"--upload-pack=/tmp/pwn.sh"}); err == nil {
		t.Fatal("Materialize accepted an option-like path")
	}
}

// TestGitRepoFetchPinnedCommitFromDefaultBranch exercises the third fallback
// rung directly. git's local transport ignores the uploadpack.allow* settings
// that would make these fixtures refuse a fetch-by-SHA, so the rung is entered
// by hand, in the state Open leaves behind once the plain fetch-by-SHA has
// failed: an initialized repo with "origin" added and nothing fetched yet.
func TestGitRepoFetchPinnedCommitFromDefaultBranch(t *testing.T) {
	src := fixtureRepo(t, true, map[string]string{"extensions/features/foo/spec.yaml": "v1\n"})
	firstCommit := strings.TrimSpace(runGitFixture(t, src, "rev-parse", "HEAD"))
	writeFixture(t, filepath.Join(src, "extensions", "features", "foo", "spec.yaml"), "v2\n", 0o644)
	runGitFixture(t, src, "add", "-A")
	runGitFixture(t, src, "commit", "-qm", "second")

	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	gf, ok := fetcher.(*gitFetcher)
	if !ok {
		t.Fatalf("fetcher = %T, want *gitFetcher", fetcher)
	}

	dstDir := t.TempDir()
	runGitFixture(t, dstDir, "init", "-q")
	runGitFixture(t, dstDir, "remote", "add", "origin", src)

	repo := &gitRepo{fetcher: gf, dir: dstDir, ref: RemoteRef{Commit: firstCommit, RefType: RefTypeCommit}}
	if err := repo.fetchPinnedCommitFromDefaultBranch(context.Background(), repo.ref); err != nil {
		t.Fatalf("fetchPinnedCommitFromDefaultBranch: %v", err)
	}
	if repo.checkoutTarget != firstCommit {
		t.Fatalf("checkoutTarget = %q, want the pinned commit %q (not FETCH_HEAD, which update-ref cannot write)", repo.checkoutTarget, firstCommit)
	}
	if repo.ref.Commit != firstCommit {
		t.Fatalf("ref.Commit = %q, want %q", repo.ref.Commit, firstCommit)
	}

	if err := repo.Materialize(context.Background(), []string{"extensions/features/foo"}); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(repo.Dir(), "extensions", "features", "foo", "spec.yaml"))
	if err != nil {
		t.Fatalf("read materialized file: %v", err)
	}
	if string(content) != "v1\n" {
		t.Fatalf("content = %q, want the pinned first commit's content, not the branch tip's", content)
	}
}

// TestGitRepoFetchPinnedCommitFromDefaultBranchMissing proves the fallback
// reports a clear error instead of proceeding with a checkout target that
// does not exist when the pinned commit is absent even after fetching the
// default branch.
func TestGitRepoFetchPinnedCommitFromDefaultBranchMissing(t *testing.T) {
	src := fixtureRepo(t, true, map[string]string{"README.md": "hi\n"})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	gf, ok := fetcher.(*gitFetcher)
	if !ok {
		t.Fatalf("fetcher = %T, want *gitFetcher", fetcher)
	}

	dstDir := t.TempDir()
	runGitFixture(t, dstDir, "init", "-q")
	runGitFixture(t, dstDir, "remote", "add", "origin", src)

	missing := strings.Repeat("f", 40)
	repo := &gitRepo{fetcher: gf, dir: dstDir, ref: RemoteRef{Commit: missing, RefType: RefTypeCommit}}
	if err := repo.fetchPinnedCommitFromDefaultBranch(context.Background(), repo.ref); err == nil {
		t.Fatal("fetchPinnedCommitFromDefaultBranch accepted a commit absent from the default branch")
	}
}

// TestGitFetcherErrorRedactsRemoteInArgv covers the failing-command half of a
// git error: the remote appears in the argv of ls-remote and `remote add`, not
// only in git's stderr, and the error string ends up in the --json envelope.
func TestGitFetcherErrorRedactsRemoteInArgv(t *testing.T) {
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	// An unknown subcommand fails immediately and without a network round trip,
	// while still carrying the remote through as an operand.
	_, _, runErr := fetcher.(*gitFetcher).runCombined(context.Background(), t.TempDir(),
		"definitely-not-a-git-subcommand", "https://user:s3cr3t@example.invalid/repo.git")
	if runErr == nil {
		t.Fatal("expected an error from an unknown git subcommand")
	}
	if strings.Contains(runErr.Error(), "s3cr3t") {
		t.Fatalf("the error leaks the remote's credentials: %v", runErr)
	}
}

// TestMaterializeAfterSparseSelectionRestoresFullTree covers the fetch cache
// serving one repository to several extensions: a sparse cone set for the first
// one must not truncate the checkout of a second that owns the whole tree.
func TestMaterializeAfterSparseSelectionRestoresFullTree(t *testing.T) {
	repo := fixtureRepo(t, true, map[string]string{
		"spec.yaml":              "schemaVersion: \"1\"\nkind: mixin\nname: root\n",
		"skills/review/SKILL.md": "root extension content\n",
		"tools/alpha/spec.yaml":  "schemaVersion: \"1\"\nkind: sandbox\nname: alpha\n",
	})
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	opened, err := fetcher.Open(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer opened.Close()

	if err := opened.Materialize(context.Background(), []string{"tools/alpha"}); err != nil {
		t.Fatalf("Materialize(subpath): %v", err)
	}
	// The repository root is an extension too, and no set of paths describes it,
	// so it materializes with a full checkout.
	if err := opened.Materialize(context.Background(), nil); err != nil {
		t.Fatalf("Materialize(root): %v", err)
	}
	if _, err := os.Stat(filepath.Join(opened.Dir(), "skills", "review", "SKILL.md")); err != nil {
		t.Fatalf("the full checkout was reduced to the previous sparse cone: %v", err)
	}
}
