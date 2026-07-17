// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import "github.com/spf13/cobra"

func extensionCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "extension",
		Short:  "Inspect extension discovery",
		Hidden: true,
		RunE:   rejectUnknownSubcommand,
	}
	cmd.AddCommand(extensionListCommand(res))
	return cmd
}

func extensionListCommand(res *Result) *cobra.Command {
	return &cobra.Command{
		Use:    "list",
		Short:  "List discovered tool and feature extensions",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "extension-list"
			return nil
		},
	}
}
