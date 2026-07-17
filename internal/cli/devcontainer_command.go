// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"github.com/spf13/cobra"

	"enclave/internal/config"
	"enclave/internal/model"
)

func devcontainerCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devcontainer",
		Short: "Manage devcontainer configuration",
		RunE:  rejectUnknownSubcommand,
	}

	var force bool
	generateCmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate .devcontainer/devcontainer.json",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "devcontainer-generate"
			res.Options.Force = force
			return nil
		},
	}
	addOptionFlags(generateCmd.Flags(), &res.Options, &res.Sources, config.OptionGroupBuild)
	addOptionFlagsByName(generateCmd.Flags(), &res.Options, &res.Sources, "tool")
	generateCmd.Flags().BoolVar(&force, "force", false, "Overwrite existing devcontainer.json")

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Start devcontainer with tool CLI",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = "run"
			res.Options.Devcontainer = true
			res.Sources.Devcontainer = model.SourceCLI
			res.Options.CmdArgs = append(res.Options.CmdArgs, cmdArgs...)
			return nil
		},
	}
	addSessionOptionFlags(runCmd, res)

	shellCmd := &cobra.Command{
		Use:   "shell",
		Short: "Start interactive shell from devcontainer.json",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = "shell"
			res.Options.Shell = true
			res.Options.Devcontainer = true
			res.Sources.Devcontainer = model.SourceCLI
			res.Options.CmdArgs = append(res.Options.CmdArgs, cmdArgs...)
			return nil
		},
	}
	addSessionOptionFlags(shellCmd, res)
	shellCmd.Flags().BoolVar(&res.Options.Admin, "admin", false, "Enable package-management sudo")

	cmd.AddCommand(generateCmd)
	cmd.AddCommand(runCmd)
	cmd.AddCommand(shellCmd)
	return cmd
}
