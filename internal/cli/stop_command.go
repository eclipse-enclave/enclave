// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import "github.com/spf13/cobra"

func stopCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop [container-name]",
		Short: "Stop background containers",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = "stop"
			res.Options.CmdArgs = append(res.Options.CmdArgs, cmdArgs...)
			return nil
		},
	}
	// stop accepts --tool and --name to filter background containers.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool", "session_name")
	return cmd
}
