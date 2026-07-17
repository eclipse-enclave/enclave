// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import "github.com/spf13/cobra"

// reviewTargetCommand is an internal plumbing command that resolves a review
// target to a normalized diff and metadata, emitted as JSON. It is hidden:
// external review tooling calls it, but it is not part of the stable
// user-facing surface.
func reviewTargetCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review-target [target]",
		Short: "Resolve what a review should cover (internal helper)",
		Long: `Resolve a review target to a normalized diff and metadata, printed as JSON.

This is internal plumbing for external review tooling; it is not part of the
stable user-facing command surface.

Targets are matched in this order:

  (empty) | uncommitted   the working tree's uncommitted changes vs HEAD (default)
  pr:<n>                  a GitHub pull request, resolved via gh
  <a>...<b>               a range diffed since the merge-base (e.g. main...HEAD)
  <a>..<b>                a two-dot range, merge-base normalized (e.g. main..HEAD)
  <ref>                   a branch diffed from HEAD's merge-base, or a single commit

The output is a JSON object of git facts: the requested and effective refs, the
base and merge-base, the changed-file list, and the diff. It computes no review
provenance and emits no review format.`,
		Args:   cobra.MaximumNArgs(1),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			res.Action = "review-target"
			if len(args) == 1 {
				res.ReviewTarget = args[0]
			}
			return nil
		},
	}
	return cmd
}
