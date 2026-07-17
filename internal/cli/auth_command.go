// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import "github.com/spf13/cobra"

func authCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage auth files",
		RunE:  rejectUnknownSubcommand,
	}
	cmd.AddCommand(
		authSubcommand("import", "Import host auth files into volume", "auth-import", res),
		authSubcommand("export", "Export auth files from volume to host", "auth-export", res),
	)
	return cmd
}

func authSubcommand(name string, description string, action string, res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   name,
		Short: description,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = action
			return nil
		},
	}
	// auth import/export only consume Tool, AuthScope, and AuthName. The other
	// Auth flags (--reset-auth, --no-api-key, --pass-api-key, --pass-env,
	// --secrets-scope) are runtime-only and have no effect here.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool", "auth_scope", "auth_name")
	return cmd
}
