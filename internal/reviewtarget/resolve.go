// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package reviewtarget

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
)

// runner runs an external command (git or gh) and returns its stdout. It is an
// interface so tests can substitute canned output for git and gh. internal/git
// also wraps git but has no such seam; if a shared runner ever grows there,
// fold this one into it rather than letting the two drift.
type runner interface {
	run(ctx context.Context, name string, args ...string) (string, error)
}

// execRunner runs commands for real, rooted at dir.
type execRunner struct{ dir string }

func (r *execRunner) run(ctx context.Context, name string, args ...string) (string, error) {
	// #nosec G204 -- name is a fixed binary (git or gh); ref and PR-number args
	// are validated (no leading '-', PR number is a positive int) and passed as
	// separate argv entries, so they cannot inject options or shell syntax.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.String(), fmt.Errorf("%s: %s: %w", commandLabel(name, args), msg, err)
		}
		return stdout.String(), fmt.Errorf("%s: %w", commandLabel(name, args), err)
	}
	return stdout.String(), nil
}

func commandLabel(name string, args []string) string {
	if len(args) > 0 {
		return name + " " + args[0]
	}
	return name
}

// Resolve resolves t against the git repository at dir, shelling out to git and,
// for pull requests, gh.
func Resolve(ctx context.Context, dir string, t Target) (*Result, error) {
	return resolve(ctx, &execRunner{dir: dir}, t)
}

func resolve(ctx context.Context, r runner, t Target) (*Result, error) {
	switch t.Kind {
	case KindUncommitted:
		return resolveUncommitted(ctx, r)
	case KindRef:
		return resolveRef(ctx, r, t)
	case KindRange:
		return resolveRange(ctx, r, t)
	case KindPR:
		return resolvePR(ctx, r, t)
	default:
		return nil, fmt.Errorf("unsupported target kind %q", t.Kind)
	}
}

func resolveUncommitted(ctx context.Context, r runner) (*Result, error) {
	head, err := revParse(ctx, r, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve uncommitted target: %w", err)
	}
	nameStatus, err := r.run(ctx, "git", "diff", "--name-status", "-z", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	diff, err := r.run(ctx, "git", "diff", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	return &Result{
		Target:       "uncommitted",
		Kind:         KindUncommitted,
		RequestedRef: "uncommitted",
		EffectiveRef: "uncommitted",
		Head:         head,
		Files:        parseNameStatus(nameStatus),
		Diff:         diff,
	}, nil
}

func resolveRef(ctx context.Context, r runner, t Target) (*Result, error) {
	// A branch name is reviewed as its changes since it diverged from HEAD
	// (HEAD...<branch>); tags and raw commits fall through to their own diff.
	if isBranchRef(ctx, r, t.Ref) {
		return resolveRange(ctx, r, Target{
			Kind:     KindRange,
			Raw:      t.Raw,
			BaseRef:  "HEAD",
			HeadRef:  t.Ref,
			ThreeDot: true,
		})
	}
	sha, err := revParse(ctx, r, t.Ref)
	if err != nil {
		return nil, err
	}
	// --cc renders a merge commit as its combined diff (the `git show`
	// behavior); without it diff-tree emits nothing for merges. Non-merge
	// commits are unaffected, and a clean merge still yields an empty diff,
	// exactly as git show would.
	nameStatus, err := r.run(ctx, "git", "diff-tree", "--root", "--no-commit-id", "--name-status", "-z", "-r", "--cc", sha, "--")
	if err != nil {
		return nil, err
	}
	diff, err := r.run(ctx, "git", "diff-tree", "--root", "--no-commit-id", "-p", "-r", "--cc", sha, "--")
	if err != nil {
		return nil, err
	}
	return &Result{
		Target:       t.Raw,
		Kind:         KindRef,
		RequestedRef: t.Ref,
		EffectiveRef: sha,
		Head:         sha,
		Files:        parseNameStatus(nameStatus),
		Diff:         diff,
	}, nil
}

func isBranchRef(ctx context.Context, r runner, ref string) bool {
	if ref == "HEAD" || ref == "@" {
		return false
	}
	out, err := r.run(ctx, "git", "rev-parse", "--symbolic-full-name", "--verify", "--quiet", ref)
	if err != nil {
		return false
	}
	fullName := strings.TrimSpace(out)
	return strings.HasPrefix(fullName, "refs/heads/") || strings.HasPrefix(fullName, "refs/remotes/")
}

func resolveRange(ctx context.Context, r runner, t Target) (*Result, error) {
	baseSHA, err := revParse(ctx, r, t.BaseRef)
	if err != nil {
		return nil, err
	}
	headSHA, err := revParse(ctx, r, t.HeadRef)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Target:       t.Raw,
		Kind:         KindRange,
		RequestedRef: t.Raw,
		Head:         headSHA,
	}

	// Default (two-dot, base is an ancestor of head): review base..head.
	effLeft := baseSHA
	normalized := false
	mb, mbErr := computeMergeBase(ctx, r, baseSHA, headSHA)
	if mbErr != nil {
		// No merge-base: unrelated or rewritten history. Keep the requested
		// range and flag it rather than guessing.
		res.MergeBaseFailed = true
	} else {
		res.MergeBase = mb
		ahead, err := revListCount(ctx, r, mb, headSHA) // commits on head since the merge-base
		if err != nil {
			return nil, err
		}
		behind, err := revListCount(ctx, r, mb, baseSHA) // commits on base since the merge-base
		if err != nil {
			return nil, err
		}
		res.NoBranchCommits = ahead == 0
		res.Diverged = ahead > 0 && behind > 0
		// Three-dot means "diff since the merge-base" by definition; two-dot
		// switches to the merge-base only when the base has moved ahead.
		if t.ThreeDot || behind > 0 {
			effLeft = mb
			res.BaseMovedAhead = behind > 0
			normalized = true
		}
	}

	res.Base = effLeft
	res.EffectiveRef = effectiveRangeRef(t, mb, normalized)

	rangeArg := effLeft + ".." + headSHA
	nameStatus, err := r.run(ctx, "git", "diff", "--name-status", "-z", rangeArg, "--")
	if err != nil {
		return nil, err
	}
	diff, err := r.run(ctx, "git", "diff", rangeArg, "--")
	if err != nil {
		return nil, err
	}
	res.Files = parseNameStatus(nameStatus)
	res.Diff = diff
	return res, nil
}

// effectiveRangeRef renders the effective range for display. When the range was
// normalized to the merge-base it reads "<short-merge-base>..<head-ref>";
// otherwise it echoes the requested symbolic range (filling in a defaulted
// HEAD), so a range that needed no normalization matches its requested ref.
func effectiveRangeRef(t Target, mb string, normalized bool) string {
	if normalized {
		return short(mb) + ".." + t.HeadRef
	}
	return t.BaseRef + ".." + t.HeadRef
}

func resolvePR(ctx context.Context, r runner, t Target) (*Result, error) {
	n := strconv.Itoa(t.PRNumber)
	// PR metadata comes from the REST endpoint rather than `gh pr view --json`:
	// gh rejects --json fields its release does not know (older gh lacks
	// baseRefOid), while the REST payload does not depend on the gh version.
	meta, err := r.run(ctx, "gh", "api", "repos/{owner}/{repo}/pulls/"+n)
	if err != nil {
		return nil, fmt.Errorf("resolve pr:%d: %w", t.PRNumber, err)
	}
	var pv struct {
		Number int `json:"number"`
		Base   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
			// Repo is null when the head repository (a fork) was deleted.
			Repo *struct {
				Owner struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := json.Unmarshal([]byte(meta), &pv); err != nil {
		return nil, fmt.Errorf("resolve pr:%d: parse gh output: %w", t.PRNumber, err)
	}
	var headOwner string
	if pv.Head.Repo != nil {
		headOwner = pv.Head.Repo.Owner.Login
	}
	diff, err := r.run(ctx, "gh", "pr", "diff", n)
	if err != nil {
		return nil, fmt.Errorf("resolve pr:%d: %w", t.PRNumber, err)
	}
	mergeBase, mbErr := prMergeBase(ctx, r, pv.Base.Ref, pv.Head.Ref, headOwner)
	base := pv.Base.SHA
	effectiveRef := pv.Base.Ref + "..." + pv.Head.Ref
	if mbErr == nil {
		base = mergeBase
		effectiveRef = short(mergeBase) + ".." + pv.Head.Ref
	}
	res := &Result{
		Target:       t.Raw,
		Kind:         KindPR,
		RequestedRef: t.Raw,
		EffectiveRef: effectiveRef,
		Base:         base,
		Head:         pv.Head.SHA,
		Files:        parseDiffNameStatus(diff),
		Diff:         diff,
		PR: &PRInfo{
			Number:      pv.Number,
			BaseRefName: pv.Base.Ref,
			HeadRefName: pv.Head.Ref,
		},
	}
	if mbErr == nil {
		res.MergeBase = mergeBase
	} else {
		res.MergeBaseFailed = true
	}
	return res, nil
}

func prMergeBase(ctx context.Context, r runner, baseRef, headRef, headOwner string) (string, error) {
	head := headRef
	if headOwner != "" {
		head = headOwner + ":" + headRef
	}
	// The compare endpoint accepts percent-encoded slashes in ref names
	// (main...owner:feat%2Fx resolves like the raw form), so the whole
	// basehead expression can be escaped as one path segment.
	endpoint := "repos/{owner}/{repo}/compare/" + url.PathEscape(baseRef+"..."+head)
	out, err := r.run(ctx, "gh", "api", endpoint, "--jq", ".merge_base_commit.sha")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("empty merge-base from gh compare")
	}
	return sha, nil
}

// revParse resolves a ref to a full commit SHA, validating that it exists and
// is not an option-like string.
func revParse(ctx context.Context, r runner, ref string) (string, error) {
	out, err := r.run(ctx, "git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("unknown revision %q", ref)
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("unknown revision %q", ref)
	}
	return sha, nil
}

func computeMergeBase(ctx context.Context, r runner, a, b string) (string, error) {
	out, err := r.run(ctx, "git", "merge-base", a, b)
	if err != nil {
		return "", err
	}
	mb := strings.TrimSpace(out)
	if mb == "" {
		return "", fmt.Errorf("no merge base for %s and %s", a, b)
	}
	return mb, nil
}

func revListCount(ctx context.Context, r runner, from, to string) (int, error) {
	out, err := r.run(ctx, "git", "rev-list", "--count", from+".."+to)
	if err != nil {
		return 0, err
	}
	trimmed := strings.TrimSpace(out)
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("parse rev-list count %q: %w", trimmed, err)
	}
	return n, nil
}

// parseNameStatus parses `git diff --name-status` output into a file list.
// Rename and copy lines carry three tab-separated fields (code, old, new); the
// new path is used.
func parseNameStatus(out string) []File {
	if strings.Contains(out, "\x00") {
		return parseNameStatusNUL(out)
	}
	files := []File{}
	sc := newLineScanner(out)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		files = append(files, File{
			Path:   decodeGitPath(fields[len(fields)-1]),
			Status: statusFromCode(fields[0]),
		})
	}
	return files
}

func parseNameStatusNUL(out string) []File {
	fields := strings.Split(out, "\x00")
	files := make([]File, 0, len(fields)/2)
	for i := 0; i < len(fields)-1; {
		code := fields[i]
		i++
		if code == "" {
			continue
		}
		if i >= len(fields)-1 {
			break
		}
		path := fields[i]
		i++
		if code[0] == 'R' || code[0] == 'C' {
			if i >= len(fields)-1 {
				break
			}
			path = fields[i]
			i++
		}
		files = append(files, File{
			Path:   decodeGitPath(path),
			Status: statusFromCode(code),
		})
	}
	return files
}

func statusFromCode(code string) string {
	if code == "" {
		return "modified"
	}
	switch code[0] {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'T':
		return "typechange"
	case 'U':
		return "unmerged"
	default:
		return "modified"
	}
}

// parseDiffNameStatus derives a file list from a unified git diff. It is used
// for pull requests, where gh returns the patch but not a name-status listing.
// Status is best-effort, read from the per-file diff headers.
func parseDiffNameStatus(diff string) []File {
	files := []File{}
	var cur *File
	flush := func() {
		if cur != nil {
			files = append(files, *cur)
			cur = nil
		}
	}
	sc := newLineScanner(diff)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &File{Path: pathFromDiffHeader(line), Status: "modified"}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
		case strings.HasPrefix(line, "rename to "):
			cur.Status = "renamed"
			cur.Path = decodeGitPath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "copy to "):
			cur.Status = "copied"
			cur.Path = decodeGitPath(strings.TrimPrefix(line, "copy to "))
		case strings.HasPrefix(line, "+++ "):
			if p := strings.TrimPrefix(line, "+++ "); p != "/dev/null" {
				cur.Path = stripDiffPrefix(p)
			}
		}
	}
	flush()
	return files
}

// pathFromDiffHeader extracts the new-side path from a "diff --git a/... b/..."
// line. Git quotes paths containing unusual characters; ordinary paths may
// contain spaces, so the unquoted form is split on the second " b/" prefix.
func pathFromDiffHeader(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if strings.HasPrefix(rest, "\"") {
		if _, tail, ok := cutQuotedGitPath(rest); ok {
			tail = strings.TrimSpace(tail)
			if p, _, ok := cutQuotedGitPath(tail); ok {
				return stripDiffPrefix(p)
			}
		}
	}
	if i := strings.Index(rest, " b/"); i >= 0 {
		return stripDiffPrefix(rest[i+1:])
	}
	return stripDiffPrefix(rest)
}

func cutQuotedGitPath(s string) (path, rest string, ok bool) {
	if !strings.HasPrefix(s, "\"") {
		return "", "", false
	}
	escaped := false
	for i := 1; i < len(s); i++ {
		switch {
		case escaped:
			escaped = false
		case s[i] == '\\':
			escaped = true
		case s[i] == '"':
			unquoted, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return "", "", false
			}
			return unquoted, s[i+1:], true
		}
	}
	return "", "", false
}

func stripDiffPrefix(p string) string {
	return stripABPrefix(decodeGitPath(p))
}

// decodeGitPath undoes git's C-style quoting. Only double-quoted strings are
// git quoting; everything else is returned verbatim — paths may legitimately
// begin or end with spaces (NUL- and tab-delimited output preserves them), so
// no trimming.
func decodeGitPath(p string) string {
	if strings.HasPrefix(p, "\"") {
		if unquoted, err := strconv.Unquote(p); err == nil {
			return unquoted
		}
	}
	return p
}

func stripABPrefix(p string) string {
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

func newLineScanner(s string) *bufio.Scanner {
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return sc
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
