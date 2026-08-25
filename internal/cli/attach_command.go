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
			target := ""
			if len(cmdArgs) > 0 {
				target = cmdArgs[0]
			}
			res.Options.CmdArgs = append(res.Options.CmdArgs, target, detachKeys)
			return nil
		},
	}
	cmd.Flags().StringVar(&detachKeys, "detach-keys", model.DetachKeysDefault, "Override the key sequence for detaching")
	return cmd
}
