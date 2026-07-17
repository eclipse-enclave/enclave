// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package git

import "testing"

func TestParseWorktreeList(t *testing.T) {
	out := []byte(`worktree /tmp/repo
HEAD 1111111111111111111111111111111111111111
branch refs/heads/main

worktree /tmp/repo-feature
HEAD 2222222222222222222222222222222222222222
branch refs/heads/feature

worktree /tmp/repo-detached
HEAD 3333333333333333333333333333333333333333
detached
`)

	got := ParseWorktreeList(out)
	if len(got) != 3 {
		t.Fatalf("expected 3 worktrees, got %d", len(got))
	}
	if !got[0].IsMain {
		t.Fatalf("expected first worktree to be main")
	}
	if got[0].Path != "/tmp/repo" || got[0].Branch != "main" {
		t.Fatalf("unexpected first worktree: %+v", got[0])
	}
	if got[1].Path != "/tmp/repo-feature" || got[1].Branch != "feature" {
		t.Fatalf("unexpected second worktree: %+v", got[1])
	}
	if got[2].Path != "/tmp/repo-detached" || got[2].Branch != "(detached)" {
		t.Fatalf("unexpected detached worktree: %+v", got[2])
	}
}
