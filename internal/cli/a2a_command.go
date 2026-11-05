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
	"time"

	"github.com/spf13/cobra"
)

// ActionA2A is the action of every `enclave a2a` verb; Result.A2A says which.
const ActionA2A = "a2a"

// A2A verbs carried in A2ARequest.Verb.
const (
	A2AVerbPeers      = "peers"
	A2AVerbLog        = "log"
	A2AVerbSend       = "send"
	A2AVerbTaskGet    = "task-get"
	A2AVerbTaskCancel = "task-cancel"
	A2AVerbTaskRetry  = "task-retry"
	A2AVerbReconcile  = "reconcile"
)

// A2ARequest is one parsed `enclave a2a` verb. The group comes from the
// peer_group option (--peer-group or config).
type A2ARequest struct {
	Verb string
	JSON bool
	// Tail limits `log` to the last n lines; 0 prints all.
	Tail    int
	Peer    string
	Text    string
	Detach  bool
	Context string
	Handle  string
	// Wait is set only when --wait was given.
	Wait *time.Duration
}

func a2aCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "a2a",
		Short: "Inspect and talk to A2A peer groups (experimental)",
		RunE:  rejectUnknownSubcommand,
	}
	cmd.AddCommand(
		a2aPeersCommand(res),
		a2aLogCommand(res),
		a2aSendCommand(res),
		a2aTaskCommand(res),
		a2aReconcileCommand(res),
	)
	return cmd
}

// a2aVerb builds a leaf whose RunE records the request; bind registers the
// verb's own flags on the request it fills.
func a2aVerb(res *Result, use, short, verb string, args cobra.PositionalArgs, bind func(*cobra.Command, *A2ARequest), fill func(*A2ARequest, []string) error) *cobra.Command {
	req := &A2ARequest{Verb: verb}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			if fill != nil {
				if err := fill(req, cmdArgs); err != nil {
					return err
				}
			}
			res.Action = ActionA2A
			res.A2A = req
			return nil
		},
	}
	if bind != nil {
		bind(cmd, req)
	}
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "peer_group")
	return cmd
}

func bindA2AJSON(cmd *cobra.Command, req *A2ARequest) {
	cmd.Flags().BoolVar(&req.JSON, "json", false, "Emit a JSON object (schemaVersion \"1\")")
}

// bindA2AWait registers --wait; the value is kept only when the flag is set,
// so the verb's default applies otherwise.
func bindA2AWait(cmd *cobra.Command, req *A2ARequest, usage string) {
	var wait time.Duration
	cmd.Flags().DurationVar(&wait, "wait", 0, usage)
	prev := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if c.Flags().Changed("wait") {
			if wait < 0 {
				return fmt.Errorf("--wait must not be negative")
			}
			req.Wait = &wait
		}
		if prev != nil {
			return prev(c, args)
		}
		return nil
	}
}

func a2aPeersCommand(res *Result) *cobra.Command {
	return a2aVerb(res, "peers", "List the members of a peer group with their cards and status", A2AVerbPeers,
		cobra.NoArgs, bindA2AJSON, nil)
}

func a2aLogCommand(res *Result) *cobra.Command {
	return a2aVerb(res, "log", "Print the hub audit log of a peer group", A2AVerbLog, cobra.NoArgs,
		func(cmd *cobra.Command, req *A2ARequest) {
			cmd.Flags().IntVar(&req.Tail, "tail", 0, "Print only the last n lines")
		},
		func(req *A2ARequest, _ []string) error {
			if req.Tail < 0 {
				return fmt.Errorf("--tail must not be negative")
			}
			return nil
		})
}

func a2aSendCommand(res *Result) *cobra.Command {
	cmd := a2aVerb(res, "send <peer> <text>", "Send a task to a worker as the host principal", A2AVerbSend,
		cobra.ExactArgs(2),
		func(cmd *cobra.Command, req *A2ARequest) {
			bindA2AJSON(cmd, req)
			cmd.Flags().BoolVar(&req.Detach, "detach", false, "Return the task handle once the worker admitted the task")
			cmd.Flags().StringVar(&req.Context, "context", "", "Continue a context: a ctx: handle from an earlier result")
			bindA2AWait(cmd, req, "How long to wait for the result (default: a2a_host_send_wait)")
		},
		func(req *A2ARequest, args []string) error {
			req.Peer = strings.TrimSpace(args[0])
			req.Text = args[1]
			if req.Peer == "" {
				return fmt.Errorf("peer must not be empty")
			}
			if req.Detach && req.Wait != nil {
				return fmt.Errorf("--detach and --wait are mutually exclusive")
			}
			return nil
		})
	withA2ABackend(cmd, res)
	cmd.Long = `Send a task to a worker of the peer group as the group's host principal.

The command blocks until the task ends or the wait elapses; then it prints the
task handle and its state, and the task continues. --detach returns as soon as
the worker admitted the task. <text> "-" reads the task text from stdin.`
	return cmd
}

func a2aTaskCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Query, cancel or retry a task sent by the host principal",
		RunE:  rejectUnknownSubcommand,
	}
	handleArg := func(req *A2ARequest, args []string) error {
		req.Handle = strings.TrimSpace(args[0])
		if req.Handle == "" {
			return fmt.Errorf("handle must not be empty")
		}
		return nil
	}
	cmd.AddCommand(
		withA2ABackend(a2aVerb(res, "get <handle>", "Show a task's state and result", A2AVerbTaskGet, cobra.ExactArgs(1),
			func(cmd *cobra.Command, req *A2ARequest) {
				bindA2AJSON(cmd, req)
				bindA2AWait(cmd, req, "Wait up to this long for the task to end (default: return at once)")
			}, handleArg), res),
		withA2ABackend(a2aVerb(res, "cancel <handle>", "Cancel a task", A2AVerbTaskCancel, cobra.ExactArgs(1), bindA2AJSON, handleArg), res),
		withA2ABackend(a2aVerb(res, "retry <handle-or-message-id>", "Resend a host task flagged as a retry with its original message ID", A2AVerbTaskRetry, cobra.ExactArgs(1),
			func(cmd *cobra.Command, req *A2ARequest) {
				bindA2AJSON(cmd, req)
				bindA2AWait(cmd, req, "How long to wait for the result (default: a2a_host_send_wait)")
			}, handleArg), res),
	)
	return cmd
}

func a2aReconcileCommand(res *Result) *cobra.Command {
	return withA2ABackend(a2aVerb(res, "reconcile", "Repair peer group state (all groups without --peer-group)", A2AVerbReconcile, cobra.NoArgs, nil, nil), res)
}

// withA2ABackend adds --backend to the verbs that reach the engine.
func withA2ABackend(cmd *cobra.Command, res *Result) *cobra.Command {
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "backend")
	return cmd
}
