// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"testing"

	"enclave/internal/model"
)

func TestRunUpdateRejectsNoRebuild(t *testing.T) {
	// --no-rebuild contradicts update's always-rebuild purpose. The guard runs
	// before any Docker/context access, so a bare input is sufficient.
	input := &CommandInput{}
	input.Options.NoRebuild = true
	if code := runUpdate(input); code == 0 {
		t.Fatal("expected non-zero exit when --no-rebuild is set for update")
	}
}

func TestResolveUpdateTools(t *testing.T) {
	paths := writeTestToolsPaths(t, []string{"claude", "codex"}, false)

	t.Run("defaults to the resolved tool when no args", func(t *testing.T) {
		opts := model.Options{}
		opts.Tool = "claude"
		got, err := resolveUpdateTools(paths, opts)
		if err != nil {
			t.Fatalf("resolveUpdateTools returned error: %v", err)
		}
		if len(got) != 1 || got[0] != "claude" {
			t.Fatalf("expected [claude], got %v", got)
		}
	})

	t.Run("uses and de-duplicates explicit tools", func(t *testing.T) {
		opts := model.Options{}
		opts.Tool = "claude"
		opts.UpdateTools = []string{"codex", "Codex", "claude"}
		got, err := resolveUpdateTools(paths, opts)
		if err != nil {
			t.Fatalf("resolveUpdateTools returned error: %v", err)
		}
		if len(got) != 2 || got[0] != "codex" || got[1] != "claude" {
			t.Fatalf("expected [codex claude], got %v", got)
		}
	})

	t.Run("rejects unknown tools", func(t *testing.T) {
		opts := model.Options{}
		opts.UpdateTools = []string{"bogus"}
		if _, err := resolveUpdateTools(paths, opts); err == nil {
			t.Fatal("expected error for unknown tool")
		}
	})
}
