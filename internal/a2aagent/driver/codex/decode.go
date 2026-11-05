// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"enclave/internal/a2aagent"
)

const maxEventBytes = 64 << 20

type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

// Decode consumes the whole stream: an agent_message alone is not an outcome.
// Success requires a thread ID, a completed turn and its final agent message.
func (d *Driver) Decode(r io.Reader, emit func(a2aagent.HarnessEvent)) (a2aagent.Outcome, error) {
	if emit == nil {
		emit = func(a2aagent.HarnessEvent) {}
	}
	var out a2aagent.Outcome
	d.mu.Lock()
	if d.sqlitePath != "" {
		out.Transcripts = append(out.Transcripts, d.sqlitePath)
	}
	d.mu.Unlock()
	var finalText string
	var completed, messageSeen, turnOpen bool
	var protocolErr error
	logged := map[string]bool{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxEventBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &header); err != nil {
			d.logf("skipped non-JSON event: %v", err)
			continue
		}
		switch header.Type {
		case "thread.started", "turn.started", "turn.completed", "turn.failed", "error", "item.completed":
			var ev event
			if err := json.Unmarshal(line, &ev); err != nil {
				protocolErr = errors.Join(protocolErr, fmt.Errorf("malformed %s: %w", header.Type, err))
				continue
			}
			switch ev.Type {
			case "thread.started":
				if !uuidPattern.MatchString(ev.ThreadID) {
					protocolErr = errors.Join(protocolErr, errors.New("thread.started without a valid thread_id"))
					continue
				}
				if out.HarnessSessionID != "" && out.HarnessSessionID != ev.ThreadID {
					protocolErr = errors.Join(protocolErr, errors.New("codex thread changed during a turn"))
					continue
				}
				out.HarnessSessionID = ev.ThreadID
				emit(a2aagent.HarnessEvent{Kind: a2aagent.EventSession, HarnessSessionID: ev.ThreadID})
			case "turn.started":
				if out.HarnessSessionID == "" {
					protocolErr = errors.Join(protocolErr, errors.New("turn.started before thread.started"))
				}
				turnOpen, completed, messageSeen = true, false, false
				finalText = ""
			case "item.completed":
				if ev.Item.Type == "agent_message" {
					if !turnOpen {
						protocolErr = errors.Join(protocolErr, errors.New("agent_message outside an open turn"))
					}
					finalText, messageSeen = ev.Item.Text, true
					emit(a2aagent.HarnessEvent{Kind: a2aagent.EventProgress, HarnessSessionID: out.HarnessSessionID, Text: ev.Item.Text})
				}
			case "turn.completed":
				if !turnOpen {
					protocolErr = errors.Join(protocolErr, errors.New("turn.completed without an open turn"))
				}
				turnOpen, completed = false, true
			case "turn.failed":
				protocolErr = errors.Join(protocolErr, fmt.Errorf("codex turn failed: %s", ev.Error.Message))
				turnOpen, completed = false, false
			case "error":
				// The CLI also emits transient reconnect diagnostics as error
				// events. Only turn.failed, missing completion or exit fails a run.
				d.logf("error event: %s", ev.Message)
			}
		case "item.started", "item.updated":
		default:
			if !logged[header.Type] {
				d.logf("skipped unknown event %q", header.Type)
				logged[header.Type] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		protocolErr = errors.Join(protocolErr, fmt.Errorf("read codex stream: %w", err))
	}
	if out.HarnessSessionID != "" {
		paths, err := TranscriptPaths(d.opts.ConfigDir, out.HarnessSessionID)
		out.Transcripts = append(out.Transcripts, paths...)
		if err != nil {
			protocolErr = errors.Join(protocolErr, fmt.Errorf("inventory codex transcripts: %w", err))
		}
	}
	if protocolErr != nil {
		return out, protocolErr
	}
	if out.HarnessSessionID == "" || !completed || turnOpen || !messageSeen {
		return out, errors.New("codex stream ended without a completed turn and final agent message")
	}
	out.FinalMessage = finalText
	emit(a2aagent.HarnessEvent{Kind: a2aagent.EventFinal, HarnessSessionID: out.HarnessSessionID, Text: finalText})
	return out, nil
}

func (d *Driver) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(d.opts.Log, "codex driver: "+format+"\n", args...)
}
