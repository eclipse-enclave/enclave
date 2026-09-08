// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import "github.com/spf13/cobra"

func projectCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Inspect and manage project identity",
		RunE:  rejectUnknownSubcommand,
	}
	cmd.AddCommand(
		projectTagCommand(res),
		projectShowCommand(res),
	)
	return cmd
}

func projectTagCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tag",
		Short: "Manage host-owned project tags",
		RunE:  rejectUnknownSubcommand,
	}
	cmd.AddCommand(
		projectTagSetCommand(res),
		projectTagUnsetCommand(res),
		projectTagListCommand(res),
	)
	return cmd
}

func projectTagSetCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Assign the current directory's project tag (first use creates the tag)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			res.Action = "project-tag-set"
			res.ProjectTag = args[0]
			return nil
		},
	}
	cmd.Flags().BoolVar(&res.ProjectYes, "yes", false, "Create or assign the tag without prompting")
	cmd.Flags().BoolVar(&res.ProjectTagNew, "new", false, "Fail instead of assigning when the tag already exists")
	cmd.Flags().BoolVar(&res.ProjectTagExisting, "existing", false, "Fail instead of creating when the tag does not exist")
	cmd.MarkFlagsMutuallyExclusive("new", "existing")
	return cmd
}

func projectTagUnsetCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unset [path]",
		Short: "Remove the current directory or an exact registered path from its tag",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			res.Action = "project-tag-unset"
			if len(args) == 1 {
				res.ProjectPath = args[0]
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&res.ProjectYes, "yes", false, "Remove an explicit path without prompting")
	return cmd
}

func projectTagListCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List project tags, their namespaces, and members",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "project-tag-list"
			return nil
		},
	}
	cmd.Flags().BoolVar(&res.ProjectJSON, "json", false, "Emit JSON output")
	return cmd
}

func projectShowCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the current project namespace and tag",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "project-show"
			return nil
		},
	}
	cmd.Flags().BoolVar(&res.ProjectJSON, "json", false, "Emit JSON output")
	return cmd
}
