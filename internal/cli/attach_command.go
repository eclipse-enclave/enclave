// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"github.com/spf13/cobra"

	"enclave/internal/model"
)

func attachCommand(res *Result) *cobra.Command {
	var detachKeys string
	cmd := &cobra.Command{
		Use:   "attach [container-or-session-name]",
		Short: "Attach to a running session by container name or --name session name",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = "attach"
			// Detach keys first: an omitted session argument must stay
			// distinguishable from an empty one, which is rejected.
			res.Options.CmdArgs = append(append(res.Options.CmdArgs, detachKeys), cmdArgs...)
			return nil
		},
	}
	cmd.Flags().StringVar(&detachKeys, "detach-keys", model.DetachKeysDefault, "Override the key sequence for detaching")
	// --tool disambiguates a session name used by more than one tool.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool")
	return cmd
}
