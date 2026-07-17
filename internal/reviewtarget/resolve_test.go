// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package reviewtarget

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunner returns canned output keyed by the exact command line, so tests
// exercise resolution without a real git repository or gh.
type fakeRunner struct {
	t    *testing.T
	out  map[string]string
	errs map[string]error
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	k := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if err, ok := f.errs[k]; ok {
		return f.out[k], err
	}
	if out, ok := f.out[k]; ok {
		return out, nil
	}
	f.t.Fatalf("unexpected command: %q", k)
	return "", nil
}

func mustResolve(t *testing.T, r runner, in string) *Result {
	t.Helper()
	tgt, err := ParseTarget(in)
	if err != nil {
		t.Fatalf("ParseTarget(%q): %v", in, err)
	}
	res, err := resolve(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("resolve(%q): %v", in, err)
	}
	return res
}

func TestResolveUncommitted(t *testing.T) {
	r := &fakeRunner{t: t, out: map[string]string{
		"git rev-parse --verify --quiet HEAD^{commit}": "headsha\n",
		"git diff --name-status -z HEAD --":            "M\x00foo.go\x00A\x00bar.go\x00D\x00old.go\x00",
		"git diff HEAD --":                             "diff --git a/foo.go b/foo.go\n",
	}}
	res := mustResolve(t, r, "")

	if res.Kind != KindUncommitted || res.RequestedRef != "uncommitted" || res.EffectiveRef != "uncommitted" {
		t.Fatalf("unexpected refs: %+v", res)
	}
	if res.Head != "headsha" {
		t.Fatalf("Head = %q, want headsha", res.Head)
	}
	if res.Base != "" || res.MergeBase != "" {
		t.Fatalf("expected empty base/merge_base, got %+v", res)
	}
	want := []File{
		{Path: "foo.go", Status: "modified"},
		{Path: "bar.go", Status: "added"},
		{Path: "old.go", Status: "deleted"},
	}
	assertFiles(t, res.Files, want)
}

func TestResolveRefSingleCommit(t *testing.T) {
	r := &fakeRunner{t: t, out: map[string]string{
		"git rev-parse --verify --quiet abc1234^{commit}":                         "fullsha\n",
		"git diff-tree --root --no-commit-id --name-status -z -r --cc fullsha --": "M\x00foo.go\x00",
		"git diff-tree --root --no-commit-id -p -r --cc fullsha --":               "diff --git a/foo.go b/foo.go\n",
	}, errs: map[string]error{
		"git rev-parse --symbolic-full-name --verify --quiet abc1234": errors.New("not a branch"),
	}}
	res := mustResolve(t, r, "abc1234")

	if res.Kind != KindRef || res.RequestedRef != "abc1234" || res.EffectiveRef != "fullsha" || res.Head != "fullsha" {
		t.Fatalf("unexpected result: %+v", res)
	}
	assertFiles(t, res.Files, []File{{Path: "foo.go", Status: "modified"}})
}

func TestResolveRefMergeCommit(t *testing.T) {
	// A merge commit renders as a combined diff (--cc): name-status codes carry
	// one character per parent, and only files differing from all parents show.
	r := &fakeRunner{t: t, out: map[string]string{
		"git rev-parse --verify --quiet merge12^{commit}":                          "mergesha\n",
		"git diff-tree --root --no-commit-id --name-status -z -r --cc mergesha --": "MM\x00conflict.go\x00",
		"git diff-tree --root --no-commit-id -p -r --cc mergesha --":               "diff --cc conflict.go\n",
	}, errs: map[string]error{
		"git rev-parse --symbolic-full-name --verify --quiet merge12": errors.New("not a branch"),
	}}
	res := mustResolve(t, r, "merge12")

	assertFiles(t, res.Files, []File{{Path: "conflict.go", Status: "modified"}})
	if res.Diff != "diff --cc conflict.go\n" {
		t.Fatalf("Diff = %q, want combined diff", res.Diff)
	}
}

func TestResolveBranchTargetUsesMergeBaseRange(t *testing.T) {
	r := &fakeRunner{t: t, out: map[string]string{
		"git rev-parse --symbolic-full-name --verify --quiet feature": "refs/heads/feature\n",
		"git rev-parse --verify --quiet HEAD^{commit}":                baseSHA + "\n",
		"git rev-parse --verify --quiet feature^{commit}":             headSHA + "\n",
		"git merge-base " + baseSHA + " " + headSHA:                   mbSHA + "\n",
		"git rev-list --count " + mbSHA + ".." + headSHA:              "2\n",
		"git rev-list --count " + mbSHA + ".." + baseSHA:              "1\n",
		"git diff --name-status -z " + mbSHA + ".." + headSHA + " --": "A\x00one.go\x00A\x00two.go\x00",
		"git diff " + mbSHA + ".." + headSHA + " --":                  "diff --git a/one.go b/one.go\n",
	}}
	res := mustResolve(t, r, "feature")

	if res.Kind != KindRange || res.RequestedRef != "feature" {
		t.Fatalf("unexpected branch target result: %+v", res)
	}
	if res.Base != mbSHA || res.MergeBase != mbSHA || res.Head != headSHA {
		t.Fatalf("unexpected refs: %+v", res)
	}
	if res.EffectiveRef != short(mbSHA)+"..feature" {
		t.Fatalf("EffectiveRef = %q, want %s..feature", res.EffectiveRef, short(mbSHA))
	}
	assertFiles(t, res.Files, []File{
		{Path: "one.go", Status: "added"},
		{Path: "two.go", Status: "added"},
	})
}

const (
	baseSHA = "b0b0b0b0b0b0b0b0"
	headSHA = "a1a1a1a1a1a1a1a1"
	mbSHA   = "m2m2m2m2m2m2m2m2"
)

// rangeRunner builds a fake runner for a main..HEAD resolution with the given
// ahead/behind counts and the effective range used for the diff.
func rangeRunner(t *testing.T, ahead, behind string, effLeft string, mergeBaseErr error) *fakeRunner {
	rangeArg := effLeft + ".." + headSHA
	f := &fakeRunner{t: t, out: map[string]string{
		"git rev-parse --verify --quiet main^{commit}":   baseSHA + "\n",
		"git rev-parse --verify --quiet HEAD^{commit}":   headSHA + "\n",
		"git merge-base " + baseSHA + " " + headSHA:      mbSHA + "\n",
		"git rev-list --count " + mbSHA + ".." + headSHA: ahead + "\n",
		"git rev-list --count " + mbSHA + ".." + baseSHA: behind + "\n",
		"git diff --name-status -z " + rangeArg + " --":  "M\x00foo.go\x00",
		"git diff " + rangeArg + " --":                   "diff --git a/foo.go b/foo.go\n",
	}}
	if mergeBaseErr != nil {
		f.errs = map[string]error{"git merge-base " + baseSHA + " " + headSHA: mergeBaseErr}
	}
	return f
}

func TestResolveRangeNormal(t *testing.T) {
	// Base is an ancestor of head (behind == 0): no normalization.
	res := mustResolve(t, rangeRunner(t, "2", "0", baseSHA, nil), "main..HEAD")

	if res.EffectiveRef != "main..HEAD" {
		t.Fatalf("EffectiveRef = %q, want main..HEAD", res.EffectiveRef)
	}
	if res.Base != baseSHA || res.MergeBase != mbSHA || res.Head != headSHA {
		t.Fatalf("unexpected refs: %+v", res)
	}
	if res.BaseMovedAhead || res.Diverged || res.NoBranchCommits || res.MergeBaseFailed {
		t.Fatalf("unexpected flags: %+v", res)
	}
}

func TestResolveRangeBaseMovedAheadDiverged(t *testing.T) {
	// Both sides have unique commits: normalize to the merge-base and flag both.
	res := mustResolve(t, rangeRunner(t, "2", "1", mbSHA, nil), "main..HEAD")

	if res.EffectiveRef != short(mbSHA)+"..HEAD" {
		t.Fatalf("EffectiveRef = %q, want %s..HEAD", res.EffectiveRef, short(mbSHA))
	}
	if res.Base != mbSHA {
		t.Fatalf("Base = %q, want merge-base %s", res.Base, mbSHA)
	}
	if !res.BaseMovedAhead || !res.Diverged {
		t.Fatalf("want BaseMovedAhead and Diverged, got %+v", res)
	}
	if res.NoBranchCommits {
		t.Fatalf("did not expect NoBranchCommits: %+v", res)
	}
}

func TestResolveRangeFullyBehind(t *testing.T) {
	// No branch-only commits (ahead == 0), base ahead (behind > 0).
	res := mustResolve(t, rangeRunner(t, "0", "3", mbSHA, nil), "main..HEAD")

	if !res.BaseMovedAhead || !res.NoBranchCommits {
		t.Fatalf("want BaseMovedAhead and NoBranchCommits, got %+v", res)
	}
	if res.Diverged {
		t.Fatalf("did not expect Diverged: %+v", res)
	}
	if res.EffectiveRef != short(mbSHA)+"..HEAD" {
		t.Fatalf("EffectiveRef = %q, want normalized", res.EffectiveRef)
	}
}

func TestResolveRangeMergeBaseFailed(t *testing.T) {
	res := mustResolve(t, rangeRunner(t, "0", "0", baseSHA, errors.New("no merge base")), "main..HEAD")

	if !res.MergeBaseFailed {
		t.Fatalf("want MergeBaseFailed, got %+v", res)
	}
	if res.EffectiveRef != "main..HEAD" || res.Base != baseSHA {
		t.Fatalf("unexpected fallback: %+v", res)
	}
	if res.MergeBase != "" {
		t.Fatalf("MergeBase should be empty when merge-base fails, got %q", res.MergeBase)
	}
}

func TestResolveRangeThreeDot(t *testing.T) {
	// Three-dot diffs since the merge-base by definition, even when the base has
	// not moved ahead (behind == 0).
	f := rangeRunner(t, "2", "0", mbSHA, nil)
	res := mustResolve(t, f, "main...HEAD")

	if res.EffectiveRef != short(mbSHA)+"..HEAD" {
		t.Fatalf("EffectiveRef = %q, want merge-base range", res.EffectiveRef)
	}
	if res.BaseMovedAhead {
		t.Fatalf("three-dot without behind should not set BaseMovedAhead: %+v", res)
	}
}

func TestResolvePR(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/added.go b/added.go",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/added.go",
		"@@ -0,0 +1 @@",
		"+package main",
		"diff --git a/gone.go b/gone.go",
		"deleted file mode 100644",
		"--- a/gone.go",
		"+++ /dev/null",
		"diff --git a/edit.go b/edit.go",
		"--- a/edit.go",
		"+++ b/edit.go",
		"@@ -1 +1 @@",
		"-old",
		"+new",
	}, "\n")
	r := &fakeRunner{t: t, out: map[string]string{
		"gh api repos/{owner}/{repo}/pulls/7": `{"number":7,"base":{"ref":"main","sha":"basesha"},"head":{"ref":"feat","sha":"headsha","repo":{"owner":{"login":"acme"}}}}`,
		"gh pr diff 7":                        diff,
		"gh api repos/{owner}/{repo}/compare/main...acme:feat --jq .merge_base_commit.sha": "mergebasesha\n",
	}}
	res := mustResolve(t, r, "pr:7")

	if res.Kind != KindPR || res.RequestedRef != "pr:7" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.PR == nil || res.PR.Number != 7 || res.PR.BaseRefName != "main" || res.PR.HeadRefName != "feat" {
		t.Fatalf("unexpected PR metadata: %+v", res.PR)
	}
	if res.Base != "mergebasesha" || res.MergeBase != "mergebasesha" || res.Head != "headsha" {
		t.Fatalf("unexpected SHAs: base=%q head=%q", res.Base, res.Head)
	}
	if res.EffectiveRef != "mergebasesha..feat" {
		t.Fatalf("EffectiveRef = %q, want mergebasesha..feat", res.EffectiveRef)
	}
	assertFiles(t, res.Files, []File{
		{Path: "added.go", Status: "added"},
		{Path: "gone.go", Status: "deleted"},
		{Path: "edit.go", Status: "modified"},
	})
}

func TestResolvePRMergeBaseFailureFallsBackToBaseRef(t *testing.T) {
	// head.repo is null when the fork was deleted; the compare then runs with
	// the bare branch name and its failure falls back to the base branch tip.
	r := &fakeRunner{
		t: t,
		out: map[string]string{
			"gh api repos/{owner}/{repo}/pulls/7": `{"number":7,"base":{"ref":"main","sha":"basesha"},"head":{"ref":"feat","sha":"headsha","repo":null}}`,
			"gh pr diff 7":                        "diff --git a/edit.go b/edit.go\n",
		},
		errs: map[string]error{
			"gh api repos/{owner}/{repo}/compare/main...feat --jq .merge_base_commit.sha": errors.New("compare unavailable"),
		},
	}
	res := mustResolve(t, r, "pr:7")

	if !res.MergeBaseFailed {
		t.Fatalf("want MergeBaseFailed, got %+v", res)
	}
	if res.Base != "basesha" || res.MergeBase != "" || res.EffectiveRef != "main...feat" {
		t.Fatalf("unexpected fallback metadata: %+v", res)
	}
}

func TestResolvePRForkWithSlashedHeadBranch(t *testing.T) {
	// A fork head is addressed as owner:branch, with slashes in the branch name
	// percent-encoded; GitHub's compare endpoint accepts the encoded form.
	r := &fakeRunner{t: t, out: map[string]string{
		"gh api repos/{owner}/{repo}/pulls/7": `{"number":7,"base":{"ref":"main","sha":"basesha"},"head":{"ref":"feat/x","sha":"headsha","repo":{"owner":{"login":"forkowner"}}}}`,
		"gh pr diff 7":                        "diff --git a/edit.go b/edit.go\n",
		"gh api repos/{owner}/{repo}/compare/main...forkowner:feat%2Fx --jq .merge_base_commit.sha": "mergebasesha\n",
	}}
	res := mustResolve(t, r, "pr:7")

	if res.MergeBase != "mergebasesha" || res.Base != "mergebasesha" {
		t.Fatalf("unexpected merge-base resolution: %+v", res)
	}
	if res.EffectiveRef != "mergebasesha..feat/x" {
		t.Fatalf("EffectiveRef = %q, want mergebasesha..feat/x", res.EffectiveRef)
	}
	if res.PR == nil || res.PR.HeadRefName != "feat/x" {
		t.Fatalf("unexpected PR metadata: %+v", res.PR)
	}
}

func TestParseNameStatusRename(t *testing.T) {
	got := parseNameStatus("R100\told/path.go\tnew/path.go\nM\tkept.go\n")
	assertFiles(t, got, []File{
		{Path: "new/path.go", Status: "renamed"},
		{Path: "kept.go", Status: "modified"},
	})
}

func TestParseNameStatusNUL(t *testing.T) {
	got := parseNameStatus("M\x00has\ttab.go\x00R100\x00old path.go\x00new\ttab.go\x00")
	assertFiles(t, got, []File{
		{Path: "has\ttab.go", Status: "modified"},
		{Path: "new\ttab.go", Status: "renamed"},
	})
}

func TestParseNameStatusQuoted(t *testing.T) {
	got := parseNameStatus("M\t\"has\\ttab.go\"\n")
	assertFiles(t, got, []File{{Path: "has\ttab.go", Status: "modified"}})
}

func TestParseNameStatusPreservesSpaces(t *testing.T) {
	// -z output carries paths verbatim: leading and trailing spaces are part of
	// the file name, not formatting.
	got := parseNameStatus("M\x00 lead.go\x00A\x00trail.go \x00")
	assertFiles(t, got, []File{
		{Path: " lead.go", Status: "modified"},
		{Path: "trail.go ", Status: "added"},
	})
}

func TestParseDiffNameStatusRename(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/old.go b/new.go",
		"similarity index 100%",
		"rename from old.go",
		"rename to new.go",
	}, "\n")
	assertFiles(t, parseDiffNameStatus(diff), []File{{Path: "new.go", Status: "renamed"}})
}

func TestParseDiffNameStatusQuotedPaths(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git \"a/has\\ttab.go\" \"b/has\\ttab.go\"",
		"--- \"a/has\\ttab.go\"",
		"+++ \"b/has\\ttab.go\"",
		"@@ -1 +1 @@",
		"-old",
		"+new",
	}, "\n")
	assertFiles(t, parseDiffNameStatus(diff), []File{{Path: "has\ttab.go", Status: "modified"}})
}

func assertFiles(t *testing.T, got, want []File) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("files = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("file[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
