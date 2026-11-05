// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package pi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"enclave/internal/a2aagent"
)

type message struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
}

type event struct {
	Type      string    `json:"type"`
	ID        string    `json:"id"`
	Message   message   `json:"message"`
	Messages  []message `json:"messages"`
	WillRetry *bool     `json:"willRetry"`
	Aborted   *bool     `json:"aborted"`
}

func messageText(m message) (string, error) {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return "", fmt.Errorf("malformed assistant content: %w", err)
	}
	if blocks == nil {
		return "", errors.New("assistant content is not an array")
	}
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

// Decode requires agent_settled after a non-retrying agent_end. Pi can emit
// several agent_end events while retrying or compacting; none is final alone.
func (d *Driver) Decode(r io.Reader, emit func(a2aagent.HarnessEvent)) (a2aagent.Outcome, error) {
	if emit == nil {
		emit = func(a2aagent.HarnessEvent) {}
	}
	var out a2aagent.Outcome
	var last *message
	var protocolErr error
	settled, ended := false, false
	logged := map[string]bool{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		var h struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &h); err != nil {
			_, _ = fmt.Fprintf(d.opts.Log, "pi driver: skipped non-JSON event: %v\n", err)
			continue
		}
		switch h.Type {
		case "session", "agent_start", "turn_start", "message_end", "agent_end", "agent_settled":
			var e event
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				protocolErr = fmt.Errorf("malformed %s: %w", h.Type, err)
				continue
			}
			if e.Type != "session" && out.HarnessSessionID == "" {
				protocolErr = errors.New("pi event before session header")
				continue
			}
			switch e.Type {
			case "session":
				if !uuidPattern.MatchString(e.ID) || (out.HarnessSessionID != "" && out.HarnessSessionID != e.ID) {
					protocolErr = errors.New("pi session header has an invalid or changed ID")
					continue
				}
				out.HarnessSessionID = e.ID
				out.Transcripts = TranscriptPaths(e.ID)
				emit(a2aagent.HarnessEvent{Kind: a2aagent.EventSession, HarnessSessionID: e.ID})
			case "agent_start", "turn_start":
				settled, ended, last = false, false, nil
			case "message_end":
				if e.Message.Role == "assistant" {
					if text, err := messageText(e.Message); err == nil {
						emit(a2aagent.HarnessEvent{Kind: a2aagent.EventProgress, HarnessSessionID: out.HarnessSessionID, Text: text})
					}
				}
			case "agent_end":
				if e.WillRetry == nil {
					protocolErr = errors.New("pi agent_end without willRetry")
					continue
				}
				ended, settled, last = !*e.WillRetry, false, nil
				for i := len(e.Messages) - 1; i >= 0; i-- {
					if e.Messages[i].Role == "assistant" {
						m := e.Messages[i]
						last = &m
						break
					}
				}
			case "agent_settled":
				if e.Aborted == nil {
					protocolErr = errors.New("pi agent_settled without aborted")
					continue
				}
				settled = !*e.Aborted && ended
				if *e.Aborted {
					protocolErr = errors.New("pi agent aborted")
				}
			}
		case "message_start", "message_update", "turn_end", "tool_execution_start", "tool_execution_update", "tool_execution_end", "auto_retry_start", "auto_retry_end", "auto_compaction_start", "auto_compaction_end", "compaction_start", "compaction_end":
		default:
			if !logged[h.Type] {
				_, _ = fmt.Fprintf(d.opts.Log, "pi driver: skipped unknown event %q\n", h.Type)
				logged[h.Type] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("read pi stream: %w", err)
	}
	if protocolErr != nil {
		return out, protocolErr
	}
	if out.HarnessSessionID == "" || !settled || last == nil {
		return out, errors.New("pi stream ended without a settled outcome")
	}
	if last.StopReason != "stop" {
		return out, fmt.Errorf("pi assistant stopped with %q: %s", last.StopReason, last.ErrorMessage)
	}
	text, err := messageText(*last)
	if err != nil {
		return out, err
	}
	out.FinalMessage = text
	emit(a2aagent.HarnessEvent{Kind: a2aagent.EventFinal, HarnessSessionID: out.HarnessSessionID, Text: out.FinalMessage})
	return out, nil
}
