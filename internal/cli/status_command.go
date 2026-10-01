// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"github.com/spf13/cobra"
)

func statusCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show terminal snapshots of running sessions",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "status"
			return nil
		},
	}
	// status filters by --tool and --name; --backend selects the engine to list.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "backend", "tool", "session_name")
	cmd.Flags().BoolVar(&res.Options.StatusJSON, "json", false, "Emit one JSON snapshot object per session")
	cmd.Flags().BoolVar(&res.Options.StatusAll, "all", false, "Show sessions from all projects, not just the current one")
	return cmd
}
