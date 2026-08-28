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
	"github.com/spf13/pflag"

	"enclave/internal/extinstall"
	"enclave/internal/model"
)

// ActionExtensionManage is the action every extension-management verb reports.
// Which kind and which verb it was are carried as typed values on
// Result.ExtRequest.
const ActionExtensionManage = "extension-manage"

// addExtensionSubcommands registers the extension-management verbs under a
// kind's parent command. listOptionNames are the parent's option flags that
// `list` repeats.
func addExtensionSubcommands(cmd *cobra.Command, res *Result, kind model.ExtensionKind, listOptionNames ...string) {
	cmd.AddCommand(
		listSubcommand(res, kind, listOptionNames...),
		addSubcommand(res, kind),
		updateSubcommand(res, kind),
		removeSubcommand(res, kind),
	)
}

// listSubcommand adds an explicit `list` verb that reuses the parent's action,
// so `enclave features` and `enclave features list` are one code path.
func listSubcommand(res *Result, kind model.ExtensionKind, optionNames ...string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List " + kind.Label() + " extensions",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = kind.Verb()
			res.ExtRequest = &extinstall.Request{Kind: kind, Op: extinstall.OpList, JSON: asJSON}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit machine-readable JSON")
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, optionNames...)
	return cmd
}

// addExtensionActionFlags registers the flags every extension-management verb
// accepts. forceUsage varies: --force overrides a different check per verb.
func addExtensionActionFlags(flags *pflag.FlagSet, request *extinstall.Request, forceUsage string) {
	flags.BoolVarP(&request.Yes, "yes", "y", false, "Do not prompt for confirmation")
	flags.BoolVar(&request.Force, "force", false, forceUsage)
	flags.BoolVar(&request.JSON, "json", false, "Emit machine-readable JSON (requires --yes)")
}

// publishExtensionRequest records the request on the result and validates it.
// The request is published even when validation fails, so the caller can report
// the rejection in the verb's own output format.
func publishExtensionRequest(res *Result, request *extinstall.Request) error {
	res.Action = ActionExtensionManage
	res.ExtRequest = request
	return validateExtensionRequest(request)
}

// validateExtensionRequest rejects the flag combinations no extension verb can
// honour and derives Request.Interactive from them.
func validateExtensionRequest(request *extinstall.Request) error {
	if request.JSON && !request.Yes {
		return fmt.Errorf("--json cannot prompt; pass --yes")
	}
	if request.All && len(request.Names) > 0 {
		return fmt.Errorf("--all and --name are mutually exclusive")
	}
	if request.Path != "" && len(request.Names) > 0 {
		return fmt.Errorf("--path and --name are mutually exclusive")
	}
	request.Interactive = !request.Yes && !request.JSON
	return nil
}

func addSubcommand(res *Result, kind model.ExtensionKind) *cobra.Command {
	request := extinstall.Request{Kind: kind, Op: extinstall.OpAdd}
	cmd := &cobra.Command{
		Use:   "add <source>",
		Short: "Install a " + kind.Label() + " extension from a git repository",
		Long: "Install a " + kind.Label() + " extension from a git repository.\n\n" +
			"Sources may be owner/repo shorthands (optionally followed by a path inside\n" +
			"the repository), https or ssh repository URLs, forge tree URLs, or local\n" +
			"paths to a git repository. With no path, the repository is searched for\n" +
			kind.Label() + " extensions.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			request.Source = args[0]
			return publishExtensionRequest(res, &request)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&request.Path, "path", "", "Path to the extension directory inside the repository")
	flags.StringVar(&request.Ref, "ref", "", "Branch, tag, or commit to install")
	flags.StringArrayVar(&request.Names, "name", nil, "Install only the named extension (repeatable)")
	flags.BoolVar(&request.All, "all", false, "Install every matching extension found in the source")
	flags.BoolVar(&request.DryRun, "dry-run", false, "Show what would be installed and write nothing")
	addExtensionActionFlags(flags, &request, "Replace an existing or hand-edited extension")
	return cmd
}

func updateSubcommand(res *Result, kind model.ExtensionKind) *cobra.Command {
	request := extinstall.Request{Kind: kind, Op: extinstall.OpUpdate}
	cmd := &cobra.Command{
		Use:   "update [<name>...]",
		Short: "Update installed " + kind.Label() + " extensions",
		Long: "Update installed " + kind.Label() + " extensions to the newest commit of the ref\n" +
			"they were installed from. With no names, every extension installed from a\n" +
			"git source is updated. This does not rebuild images; see `enclave update`.",
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			request.Names = append(request.Names, args...)
			return publishExtensionRequest(res, &request)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&request.Ref, "ref", "", "Update to this branch, tag, or commit (single extension only)")
	flags.BoolVar(&request.DryRun, "dry-run", false, "Show what would change and write nothing")
	addExtensionActionFlags(flags, &request, "Discard local modifications")
	return cmd
}

func removeSubcommand(res *Result, kind model.ExtensionKind) *cobra.Command {
	request := extinstall.Request{Kind: kind, Op: extinstall.OpRemove}
	cmd := &cobra.Command{
		Use:   "remove <name>...",
		Short: "Remove installed " + kind.Label() + " extensions",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			request.Names = append(request.Names, args...)
			return publishExtensionRequest(res, &request)
		},
	}
	addExtensionActionFlags(cmd.Flags(), &request, "Remove an extension enclave did not install")
	return cmd
}
