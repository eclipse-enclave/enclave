// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package copilot

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"enclave/internal/a2aagent"
)

type event struct {
	Type      string          `json:"type"`
	AgentID   string          `json:"agentId"`
	Data      json.RawMessage `json:"data"`
	SessionID string          `json:"sessionId"`
	ExitCode  *int            `json:"exitCode"`
}

// Decode consumes through EOF. Only a successful result commits the last main
// assistant message; message deltas, tool output and subagents are never final.
func (d *Driver) Decode(r io.Reader, emit func(a2aagent.HarnessEvent)) (a2aagent.Outcome, error) {
	if emit == nil {
		emit = func(a2aagent.HarnessEvent) {}
	}
	var out a2aagent.Outcome
	var finalText string
	var hasFinalMessage, turnOpen bool
	var result *event
	var protocolErr error
	logged := map[string]bool{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			var header struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(line, &header) == nil && (header.Type == "result" || header.Type == "assistant.message" || header.Type == "session.error") {
				protocolErr = fmt.Errorf("malformed copilot %s: %w", header.Type, err)
			}
			d.logf("skipped malformed JSON event: %v", err)
			continue
		}
		if ev.AgentID != "" {
			continue
		}
		switch ev.Type {
		case "assistant.turn_start":
			turnOpen = true
			hasFinalMessage = false
		case "assistant.message":
			turnOpen = true
			var message struct {
				Content          *string           `json:"content"`
				ParentToolCallID string            `json:"parentToolCallId"`
				ToolRequests     []json.RawMessage `json:"toolRequests"`
			}
			if err := json.Unmarshal(ev.Data, &message); err != nil || message.Content == nil {
				protocolErr = errors.New("malformed copilot assistant.message")
				continue
			}
			if message.ParentToolCallID != "" {
				continue
			}
			// Invalidate the previous answer when the model starts another tool turn.
			finalText = ""
			hasFinalMessage = len(message.ToolRequests) == 0
			if len(message.ToolRequests) == 0 {
				finalText = *message.Content
			}
			if *message.Content != "" {
				emit(a2aagent.HarnessEvent{Kind: a2aagent.EventProgress, Text: *message.Content})
			}
		case "session.error":
			var failure struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(ev.Data, &failure); err != nil {
				protocolErr = errors.New("malformed copilot session.error")
			} else {
				protocolErr = fmt.Errorf("copilot session error: %s", failure.Message)
			}
		case "result":
			result = &ev
			turnOpen = false
			if !uuidPattern.MatchString(ev.SessionID) || ev.ExitCode == nil {
				protocolErr = errors.New("copilot result without UUID sessionId or exitCode")
				continue
			}
			if out.HarnessSessionID != "" && out.HarnessSessionID != ev.SessionID {
				protocolErr = errors.New("copilot session changed within a stream")
				continue
			}
			if out.HarnessSessionID == "" {
				out.HarnessSessionID = ev.SessionID
				out.Transcripts = TranscriptPaths(ev.SessionID)
				emit(a2aagent.HarnessEvent{Kind: a2aagent.EventSession, HarnessSessionID: ev.SessionID})
			}
		default:
			if !quietEvents[ev.Type] && !logged[ev.Type] {
				logged[ev.Type] = true
				d.logf("skipped unknown event %q", ev.Type)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("read copilot stream: %w", err)
	}
	if protocolErr != nil {
		return out, protocolErr
	}
	if result == nil {
		return out, errors.New("copilot stream ended without a result")
	}
	if *result.ExitCode != 0 {
		return out, fmt.Errorf("copilot result exitCode %d", *result.ExitCode)
	}
	if turnOpen || !hasFinalMessage {
		return out, errors.New("copilot result without a completed assistant message")
	}
	out.FinalMessage = finalText
	emit(a2aagent.HarnessEvent{Kind: a2aagent.EventFinal, HarnessSessionID: out.HarnessSessionID, Text: finalText})
	return out, nil
}

var quietEvents = map[string]bool{
	"session.skills_loaded": true, "session.mcp_servers_loaded": true, "session.tools_updated": true,
	"user.message": true, "assistant.turn_start": true, "assistant.turn_end": true, "assistant.idle": true,
	"assistant.message_start": true, "assistant.message_delta": true, "assistant.reasoning": true,
	"assistant.reasoning_delta": true, "model.call_start": true, "model.call_finished": true,
	"model.call_final_result": true, "tool.execution_start": true, "tool.execution_complete": true,
	"tool.execution_partial_result": true, "tool.execution_progress": true,
}

func (d *Driver) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(d.opts.Log, "copilot driver: "+format+"\n", args...)
}
