// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2aagent

import (
	"context"
	"io"
	"time"

	"enclave/internal/a2a"
)

// Config is the adapter and client configuration read from the ENCLAVE_A2A_*
// environment (interfaces.md section 6). Zero bounds mean "use the default".
type Config struct {
	Role               a2a.Role
	Group              string
	GroupGeneration    string
	Member             string
	MemberGeneration   string
	HubHost            string
	HubPort            int
	AdapterPort        int
	InboundToken       string
	OutboundToken      string
	TableDir           string
	Description        string
	QueueDepth         int
	TaskDeadline       time.Duration
	SendWait           time.Duration
	MaxRequestBytes    int
	MaxResultBytes     int
	RecordLimit        int
	RecordMaxAge       time.Duration
	ReconnectWindow    time.Duration
	ContextTTL         time.Duration
	TerminationTimeout time.Duration
	HeartbeatInterval  time.Duration
}

// TaskEvent is a status or artifact event for one task, streamed to the hub.
type TaskEvent struct {
	Task  *TaskRecord
	Event a2a.Result
}

// Executor runs admitted tasks serially, each in its own process group, and
// confirms its subreaper descendant tree empty before the next (proposal 9,
// 12; CR-P2-1: harness tools leave the group).
type Executor interface {
	// Run dequeues from the table until ctx ends.
	Run(ctx context.Context) error
	// Submit hands a newly admitted task its bare text, held in memory only (CR-A8-2).
	Submit(taskID, text string)
	// HubLost ends running and queued tasks once the reconnect window elapsed (CR-A8-1).
	HubLost(ctx context.Context) error
	// Subscribe streams events of one task until its terminal event.
	Subscribe(taskID string) (<-chan TaskEvent, func())
	// Cancel terminates the group, then kills the descendant tree; the result carries the termination field.
	Cancel(ctx context.Context, taskID string) (*TaskRecord, error)
	// EpochChanged ends old-epoch running and queued tasks.
	EpochChanged(ctx context.Context, epoch string) error
	// Depart applies the three facts when authoritative; when suspected it stops
	// admission and discards queued work, and running work continues.
	Depart(ctx context.Context, principal PrincipalRef, authoritative bool) (a2a.DepartureAck, error)
	// DescendantsAlive reports whether the executor's descendant tree is non-empty.
	DescendantsAlive() (bool, error)
}

// Server is the worker adapter's HTTP server and hub control connection.
type Server interface {
	// Serve listens on IPv4 at the adapter port until ctx ends.
	Serve(ctx context.Context) error
}

// MCPTool names exposed to the orchestrator harness (proposal 9).
const (
	MCPToolListPeers  = "list_peers"
	MCPToolSendTask   = "send_task"
	MCPToolGetTask    = "get_task"
	MCPToolCancelTask = "cancel_task"
)

// MCPClient is the orchestrator's stdio MCP server; it also holds the
// orchestrator liveness connection while it runs.
type MCPClient interface {
	Serve(ctx context.Context, in io.Reader, out io.Writer) error
}
