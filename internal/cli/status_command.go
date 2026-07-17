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
	// status accepts --tool and --name to filter running containers, mirroring
	// ps. Other Run-group flags don't apply (status doesn't start anything).
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool", "session_name")
	cmd.Flags().BoolVar(&res.Options.StatusJSON, "json", false, "Emit one JSON snapshot object per session")
	cmd.Flags().BoolVar(&res.Options.StatusAll, "all", false, "Show sessions from all projects, not just the current one")
	return cmd
}
