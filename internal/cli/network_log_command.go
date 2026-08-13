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
)

func networkLogCommand(res *Result) *cobra.Command {
	var allRunning bool
	view := &res.NetworkLogView

	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show gateway network audit events",
		Long: `Show the network audit events the gateway records.

Without --session or --all-running the log of the current project and tool is
read, including sessions that have already exited.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if view.Follow && view.Summary {
				return fmt.Errorf("--follow and --summary are mutually exclusive")
			}
			if view.JSON {
				// --json is the integration contract and already machine form.
				view.Plain = false
			}
			if strings.EqualFold(strings.TrimSpace(view.Since), "session") && allRunning {
				return fmt.Errorf("--since session needs a single session in scope; drop --all-running or pass --session <name>")
			}
			if strings.TrimSpace(view.Session) != "" && allRunning {
				return fmt.Errorf("--session and --all-running are mutually exclusive")
			}
			res.Action = "network-log"
			res.Options.AllRunning = allRunning
			return nil
		},
	}

	cmd.Flags().BoolVarP(&view.Follow, "follow", "f", false, "Stream new events as they arrive")
	cmd.Flags().BoolVar(&view.Summary, "summary", false, "Show a per-domain aggregate instead of rows")
	cmd.Flags().BoolVar(&view.JSON, "json", false, "Emit the raw JSONL event stream")
	cmd.Flags().BoolVar(&view.Plain, "plain", false, "Force machine-readable output in a terminal")
	cmd.Flags().StringVar(&view.Since, "since", "", "Only events since a duration (10m), an RFC3339 timestamp, or \"session\"")
	cmd.Flags().StringVar(&view.Verdict, "verdict", "", "Filter by verdict: pass|deny")
	cmd.Flags().StringVar(&view.Domain, "domain", "", "Filter by domain pattern (example.com or *.example.com)")
	cmd.Flags().StringVar(&view.Type, "type", "", "Filter by event type: dns|http|tcp")
	cmd.Flags().StringVar(&view.Session, "session", "", "Read one session's log, named by its container")
	cmd.Flags().BoolVar(&allRunning, "all-running", false, "Read the logs of all running gateways on the host")
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool")
	return cmd
}
