// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import "github.com/spf13/cobra"

const (
	ActionSecretsSet   = "secrets-set"
	ActionSecretsUnset = "secrets-unset"
	ActionSecretsList  = "secrets-list"
)

// SecretsRequest is the parsed `enclave secrets` invocation. Key is empty
// for `list` without a key; Tool is empty unless --tool narrows the request to
// one tool's secrets files.
type SecretsRequest struct {
	Key     string
	Tool    string
	Project bool
}

func secretsCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage API keys in the secrets files",
		RunE:  rejectUnknownSubcommand,
	}
	cmd.AddCommand(
		secretsMutationCommand("set <key>", "Save an API key for the tools that use it (prompts, or reads stdin)", ActionSecretsSet, res),
		secretsMutationCommand("unset <key>", "Remove an API key", ActionSecretsUnset, res),
		secretsListCommand(res),
	)
	return cmd
}

func secretsMutationCommand(use string, short string, action string, res *Result) *cobra.Command {
	req := &SecretsRequest{}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			req.Key = args[0]
			res.Action = action
			res.Secrets = req
			return nil
		},
	}
	cmd.Flags().BoolVar(&req.Project, "project", false, "Use the current project's secrets files instead of the ones for all projects")
	cmd.Flags().StringVar(&req.Tool, "tool", "", "Use only this tool's secrets file instead of the one every tool reads")
	// --secrets-scope does not change the file written; it selects the scope
	// the follow-up checks assume sessions run with.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "secrets_scope")
	return cmd
}

func secretsListCommand(res *Result) *cobra.Command {
	req := &SecretsRequest{}
	cmd := &cobra.Command{
		Use:   "list [key]",
		Short: "Show where each tool's API keys come from in the current project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				req.Key = args[0]
			}
			res.Action = ActionSecretsList
			res.Secrets = req
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Tool, "tool", "", "Show only this tool")
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "secrets_scope")
	return cmd
}
