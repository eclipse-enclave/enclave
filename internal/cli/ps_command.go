// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import "github.com/spf13/cobra"

func psCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List enclave containers",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "ps"
			return nil
		},
	}
	// ps filters by --tool and --name; --backend selects the engine to list.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "backend", "tool", "session_name")
	cmd.Flags().BoolVar(&res.Options.PSAll, "all", false, "Include stopped containers, not just running ones")
	cmd.Flags().BoolVar(&res.Options.PSJSON, "json", false, "Emit a JSON array instead of the table")
	return cmd
}
