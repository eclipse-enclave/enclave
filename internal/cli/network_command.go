// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"enclave/internal/model"
)

func networkCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Inspect and manage network policy",
		RunE:  rejectUnknownSubcommand,
	}

	cmd.AddCommand(
		networkStatusCommand(res),
		networkPrintCommand(res),
		networkDiffCommand(res),
		networkApplyCommand(res),
		networkAddDomainCommand(res),
		networkRemoveDomainCommand(res),
		networkSetModeCommand(res),
	)

	return cmd
}

func networkApplyCommand(res *Result) *cobra.Command {
	var allRunning bool

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply policy to running gateway containers",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "network-apply"
			res.Options.AllRunning = allRunning
			return nil
		},
	}
	cmd.Flags().BoolVar(&allRunning, "all-running", false, "Target all running gateways on the host")
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool")
	return cmd
}

// networkQueryCommand builds a read-only network subcommand: NoArgs, a fixed
// action string, and the shared --tool option flags.
func networkQueryCommand(name string, short string, action string, res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = action
			return nil
		},
	}
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool")
	return cmd
}

func networkStatusCommand(res *Result) *cobra.Command {
	return networkQueryCommand("status", "Show effective network policy status", "network-status", res)
}

func networkPrintCommand(res *Result) *cobra.Command {
	return networkQueryCommand("print", "Print effective dnsmasq configuration", "network-print", res)
}

func networkDiffCommand(res *Result) *cobra.Command {
	return networkQueryCommand("diff", "Show changes from built-in defaults", "network-diff", res)
}

func networkMutationCommand(name string, short string, action string, nArgs int, res *Result) *cobra.Command {
	return networkScopedMutationCommand(name, short, action, nArgs, res, nil)
}

func networkScopedMutationCommand(name string, short string, action string, nArgs int, res *Result, validate func(args []string) error) *cobra.Command {
	var global, project bool
	var noApply, allRunning bool

	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.ExactArgs(nArgs),
		RunE: func(_ *cobra.Command, args []string) error {
			if validate != nil {
				if err := validate(args); err != nil {
					return err
				}
			}
			if err := validateNetworkMutationScope(global, project); err != nil {
				return err
			}
			res.Action = action
			res.Options.CmdArgs = append(res.Options.CmdArgs, args...)
			res.Options.NoApply = noApply
			res.Options.AllRunning = allRunning
			return nil
		},
	}
	registerNetworkMutationFlags(cmd, &global, &project, &noApply, &allRunning)
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool")
	return cmd
}

func networkAddDomainCommand(res *Result) *cobra.Command {
	return networkMutationCommand(
		"add-domain <domain>",
		"Add a domain to the network allowlist",
		"network-add-domain",
		1,
		res,
	)
}

func networkRemoveDomainCommand(res *Result) *cobra.Command {
	return networkMutationCommand(
		"remove-domain <domain>",
		"Remove a domain from the network allowlist",
		"network-remove-domain",
		1,
		res,
	)
}

func networkSetModeCommand(res *Result) *cobra.Command {
	return networkScopedMutationCommand(
		"set-mode <mode>",
		"Set network mode (restricted or unrestricted)",
		"network-set-mode",
		1,
		res,
		func(args []string) error {
			mode := args[0]
			if mode != model.NetworkModeRestricted && mode != model.NetworkModeUnrestricted {
				return fmt.Errorf("mode must be \"restricted\" or \"unrestricted\", got %q", mode)
			}
			return nil
		},
	)
}

func registerNetworkMutationFlags(cmd *cobra.Command, global *bool, project *bool, noApply *bool, allRunning *bool) {
	cmd.Flags().BoolVar(global, "global", false, "Apply to global policy (~/.config/enclave/network.jsonc)")
	cmd.Flags().BoolVar(project, "project", false, "Apply to project policy (not yet supported)")
	cmd.Flags().BoolVar(noApply, "no-apply", false, "Persist changes only; do not apply to running gateways")
	cmd.Flags().BoolVar(allRunning, "all-running", false, "When applying, target all running gateways on the host")
}

func validateNetworkMutationScope(global bool, project bool) error {
	if project {
		return fmt.Errorf("--project scope is not yet supported")
	}
	if !global {
		return fmt.Errorf("a scope flag is required: use --global (--project is planned)")
	}
	return nil
}
