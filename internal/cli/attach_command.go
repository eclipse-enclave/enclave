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
		Use:   "attach <container-name>",
		Short: "Attach to a running container",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = "attach"
			res.Options.CmdArgs = append(res.Options.CmdArgs, cmdArgs[0], detachKeys)
			return nil
		},
	}
	cmd.Flags().StringVar(&detachKeys, "detach-keys", model.DetachKeysDefault, "Override the key sequence for detaching")
	return cmd
}
