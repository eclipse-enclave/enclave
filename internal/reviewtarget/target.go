// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package reviewtarget resolves what a code review should cover.
//
// It provides a small, reusable resolver for review targets, including
// pull-request resolution. It returns plain git facts — the requested and
// effective refs, the base and merge-base, the changed-file list, and the
// diff — and deliberately does not compute review-record provenance (hashes,
// modes) or emit any review format; that layering lives with the review
// capture helpers.
//
// A target is one of the following. Precedence is applied in this order to the
// trimmed input, so it is deterministic:
//
//	""  | "uncommitted"   the working tree's uncommitted changes vs HEAD (default)
//	"pr:<n>"              a GitHub pull request (e.g. pr:42), resolved via gh
//	"<a>...<b>"           a three-dot range: the diff of <b> since its merge-base with <a>
//	"<a>..<b>"            a two-dot range (e.g. main..HEAD), merge-base normalized
//	"<ref>"              a branch (diffed from HEAD's merge-base) or a single commit
//
// A bare branch is compared against its merge-base with HEAD, so reviewing the
// branch you are currently on resolves to an empty range (reported with
// NoBranchCommits); name an explicit range (e.g. main..feature) to review a
// branch from a fixed base. A tag or raw commit SHA resolves to that single
// commit's own diff.
//
// Malformed input returns a deterministic error: a "pr:" prefix without a
// positive integer, or a ref/range whose ref begins with "-" (which could be
// mistaken for a git option).
package reviewtarget

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind classifies what a resolved target covers.
type Kind string

const (
	KindUncommitted Kind = "uncommitted"
	KindRef         Kind = "ref"
	KindRange       Kind = "range"
	KindPR          Kind = "pr"
)

// File is one changed file in a resolved diff.
type File struct {
	Path   string `json:"path"`
	Status string `json:"status"` // added | modified | deleted | renamed | copied | typechange | unmerged
}

// PRInfo carries the pull-request metadata for a pr:<n> target. The effective
// diff base and head commit SHAs live in Result.Base and Result.Head.
type PRInfo struct {
	Number      int    `json:"number"`
	BaseRefName string `json:"base_ref_name"`
	HeadRefName string `json:"head_ref_name"`
}

// Result is the resolved review target: plain git facts, with no review-format
// envelope and no provenance hashing.
type Result struct {
	Target       string `json:"target"` // the input, canonicalized
	Kind         Kind   `json:"kind"`
	RequestedRef string `json:"requested_ref"` // what was asked for
	EffectiveRef string `json:"effective_ref"` // after merge-base normalization
	Base         string `json:"base,omitempty"`
	MergeBase    string `json:"merge_base,omitempty"`
	Head         string `json:"head,omitempty"`
	Files        []File `json:"files"`
	Diff         string `json:"diff"`

	// Normalization flags, reported instead of the skill's prose notes.
	BaseMovedAhead  bool `json:"base_moved_ahead,omitempty"`  // base has commits the branch lacks; effective range starts at the merge-base
	Diverged        bool `json:"diverged,omitempty"`          // both sides have unique commits (possible force-push/cherry-pick)
	NoBranchCommits bool `json:"no_branch_commits,omitempty"` // nothing branch-only to review
	MergeBaseFailed bool `json:"merge_base_failed,omitempty"` // no merge-base (unrelated or rewritten history)

	PR *PRInfo `json:"pr,omitempty"`
}

// Target is a parsed, validated review target, ready to resolve.
type Target struct {
	Kind     Kind
	Raw      string // canonical input
	Ref      string // KindRef
	BaseRef  string // KindRange: left side
	HeadRef  string // KindRange: right side
	ThreeDot bool   // KindRange: a...b rather than a..b
	PRNumber int    // KindPR
}

// ParseTarget parses a target string into a Target, applying the documented
// precedence. See the package comment for the accepted forms and errors.
func ParseTarget(s string) (Target, error) {
	raw := strings.TrimSpace(s)
	if raw == "" || raw == "uncommitted" {
		return Target{Kind: KindUncommitted, Raw: "uncommitted"}, nil
	}
	if rest, ok := cutPRPrefix(raw); ok {
		n, err := strconv.Atoi(rest)
		if err != nil || n <= 0 {
			return Target{}, fmt.Errorf("invalid pull-request target %q: expected pr:<positive-integer>", s)
		}
		return Target{Kind: KindPR, Raw: raw, PRNumber: n}, nil
	}
	// "..." is checked before ".." because the former contains the latter.
	if a, b, ok := strings.Cut(raw, "..."); ok {
		return newRange(raw, a, b, true)
	}
	if a, b, ok := strings.Cut(raw, ".."); ok {
		return newRange(raw, a, b, false)
	}
	if strings.HasPrefix(raw, "-") {
		return Target{}, fmt.Errorf("invalid ref target %q: must not begin with '-'", s)
	}
	return Target{Kind: KindRef, Raw: raw, Ref: raw}, nil
}

// newRange builds a range Target, defaulting an omitted side to HEAD (matching
// git's own "a.." / "..b" shorthand) and rejecting option-like refs.
func newRange(raw, a, b string, threeDot bool) (Target, error) {
	if a == "" {
		a = "HEAD"
	}
	if b == "" {
		b = "HEAD"
	}
	if strings.HasPrefix(a, "-") || strings.HasPrefix(b, "-") {
		return Target{}, fmt.Errorf("invalid range target %q: refs must not begin with '-'", raw)
	}
	return Target{Kind: KindRange, Raw: raw, BaseRef: a, HeadRef: b, ThreeDot: threeDot}, nil
}

// cutPRPrefix reports whether s begins with a case-insensitive "pr:" and
// returns the remainder.
func cutPRPrefix(s string) (string, bool) {
	if len(s) >= 3 && strings.EqualFold(s[:3], "pr:") {
		return s[3:], true
	}
	return "", false
}
