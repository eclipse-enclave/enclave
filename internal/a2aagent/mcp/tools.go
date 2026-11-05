// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"encoding/json"

	"enclave/internal/a2aagent"
)

// Tool is one MCP tool definition as sent in tools/list.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema schema `json:"inputSchema"`
}

type schema struct {
	Type       string              `json:"type"`
	Properties map[string]property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

type property struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

var (
	propString  = property{Type: "string"}
	propWait    = property{Type: "integer"}
	propHandle  = property{Type: "string", Description: "task: handle"}
	propContext = property{Type: "string", Description: "ctx: handle, to continue"}
)

// Tools are the four definitions; every byte here is paid once per orchestrator session (proposal 21).
var Tools = []Tool{
	{
		Name:        a2aagent.MCPToolListPeers,
		Description: "List worker peers.",
		InputSchema: schema{Type: "object", Properties: map[string]property{}},
	},
	{
		Name: a2aagent.MCPToolSendTask,
		Description: "Send a task to a worker; waits for its final message. If the wait ends first, returns " +
			"the handle (the task: value, not a Claude task ID); the task continues, use get_task. " +
			"retry=true only to resend one whose outcome you lost (same peer, text, context).",
		InputSchema: schema{
			Type: "object",
			Properties: map[string]property{
				"peer":         propString,
				"text":         {Type: "string", Description: "full task; worker lacks your context"},
				"context":      propContext,
				"wait_seconds": propWait,
				"retry":        {Type: "boolean"},
				"message_id":   propString,
			},
			Required: []string{"peer", "text"},
		},
	},
	{
		Name:        a2aagent.MCPToolGetTask,
		Description: "Task state and final message; waits up to wait_seconds (0: now). No handle: recent sends.",
		InputSchema: schema{
			Type:       "object",
			Properties: map[string]property{"handle": propHandle, "wait_seconds": propWait},
		},
	},
	{
		Name:        a2aagent.MCPToolCancelTask,
		Description: "Cancel a task.",
		InputSchema: schema{
			Type:       "object",
			Properties: map[string]property{"handle": propHandle},
			Required:   []string{"handle"},
		},
	},
}

// ToolsJSON is the tools/list result as sent on the wire.
func ToolsJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"tools": Tools})
}
