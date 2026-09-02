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
		Use:   "stop [container-or-session-name]",
		Short: "Stop background containers, or one session by name",
		Long: `Stop and remove enclave containers.

With an argument, exactly the session it names is removed: a container name from
` + "`enclave ps`" + `, a container ID, or a session name of the current project.
A session name of another project is not accepted here — pass its container name.

Without an argument, every background container of the selected tool is removed,
across all projects; --name narrows that to matching sessions of the current
project.`,
		Args: cobra.MaximumNArgs(1),
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
