// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"enclave/internal/model"
)

// Actions of the request filter verbs beyond list, add, update, and remove.
const (
	ActionFiltersShow   = "filters-show"
	ActionFiltersLogin  = "filters-login"
	ActionFiltersGrants = "filters-grants"
	ActionFiltersLogout = "filters-logout"
)

// FilterRequest is the parsed state of a request filter verb.
type FilterRequest struct {
	Name         string
	Preset       string
	Scopes       []string
	ClientSecret string
	Grant        string
	Import       string
	NoBrowser    bool
	All          bool
	NoRevoke     bool
}

func filtersCommand(res *Result) *cobra.Command {
	cmd := simpleCommand("filters", "List installed request filters", res)
	// A filter counts as enabled when the effective filters selection names
	// it, which depends on --filters and the tool's tool_overrides.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "filters", "tool")
	addExtensionSubcommands(cmd, res, model.KindFilter, "filters", "tool")
	setToolUsage(cmd, "Tool whose tool_overrides apply (default: the configured tool; never asks)")
	cmd.AddCommand(
		filtersShowCommand(res),
		filtersLoginCommand(res),
		filtersGrantsCommand(res),
		filtersLogoutCommand(res),
	)
	return cmd
}

func filtersShowCommand(res *Result) *cobra.Command {
	req := &FilterRequest{}
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a filter's hosts, capabilities, and presets",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			req.Name = args[0]
			res.Action = ActionFiltersShow
			res.FilterRequest = req
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Preset, "preset", "", "Also show this preset merged, its required scopes, and its grant")
	return cmd
}

func filtersLoginCommand(res *Result) *cobra.Command {
	req := &FilterRequest{}
	var scopes string
	cmd := &cobra.Command{
		Use:   "login <name>",
		Short: "Authorize a filter and store the grant on the host",
		Long: "Authorize an oauth2 filter in the browser and store the grant under the\n" +
			"Enclave state directory. Requests the scopes of --preset (default: the\n" +
			"filter's default preset) or exactly --scopes. --import stores an existing\n" +
			"authorized_user credential instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			req.Name = args[0]
			req.Scopes = splitCSV(scopes)
			if req.Preset != "" && len(req.Scopes) > 0 && req.Import == "" {
				return fmt.Errorf("pass --preset or --scopes, not both")
			}
			if req.Import != "" && req.ClientSecret != "" {
				return fmt.Errorf("--import carries its own client: drop --client-secret")
			}
			if req.Import != "" && req.NoBrowser {
				return fmt.Errorf("--import opens no browser: drop --no-browser")
			}
			res.Action = ActionFiltersLogin
			res.FilterRequest = req
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&req.Preset, "preset", "", "Request the scopes this preset needs")
	flags.StringVar(&scopes, "scopes", "", "Request these scopes (comma-separated, short or full names)")
	flags.StringVar(&req.ClientSecret, "client-secret", "", "OAuth client secret JSON (installed, web, or flat) to store for this filter")
	flags.StringVar(&req.Grant, "grant", "", "Grant id (default: the preset name, or custom-<hash> for --scopes)")
	flags.StringVar(&req.Import, "import", "", "Import an authorized_user JSON file instead of logging in")
	flags.BoolVar(&req.NoBrowser, "no-browser", false, "Print the authorization URL without opening a browser")
	return cmd
}

func filtersGrantsCommand(res *Result) *cobra.Command {
	return &cobra.Command{
		Use:   "grants <name>",
		Short: "List a filter's stored grants",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			res.Action = ActionFiltersGrants
			res.FilterRequest = &FilterRequest{Name: args[0]}
			return nil
		},
	}
}

func filtersLogoutCommand(res *Result) *cobra.Command {
	req := &FilterRequest{}
	cmd := &cobra.Command{
		Use:   "logout <name>",
		Short: "Delete stored grants and revoke them",
		Long: "Delete a filter's grant (--grant) or all of them (--all), revoking each at\n" +
			"the filter's revokeUrl when it declares one. Without either flag the only\n" +
			"stored grant is deleted. Imported grants are never revoked, and neither is\n" +
			"a grant whose OAuth client another remaining grant uses.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if req.All && req.Grant != "" {
				return fmt.Errorf("--grant and --all are mutually exclusive")
			}
			req.Name = args[0]
			res.Action = ActionFiltersLogout
			res.FilterRequest = req
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Grant, "grant", "", "Grant id to delete")
	cmd.Flags().BoolVar(&req.All, "all", false, "Delete every grant of the filter")
	cmd.Flags().BoolVar(&req.NoRevoke, "no-revoke", false, "Delete without revoking")
	return cmd
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
