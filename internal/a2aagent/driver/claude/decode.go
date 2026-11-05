// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"enclave/internal/a2aagent"
)

// maxEventBytes bounds one stream-json line; tool results can be large.
var maxEventBytes = 64 << 20

// quietEvents are known events that carry nothing for the task; they are
// skipped without a log line. Anything else unknown is logged once.
var quietEvents = map[string]bool{
	"user":                            true,
	"rate_limit_event":                true,
	"stream_event":                    true,
	"system/thinking_tokens":          true,
	"system/task_started":             true,
	"system/task_updated":             true,
	"system/task_notification":        true,
	"system/task_progress":            true,
	"system/background_tasks_changed": true,
	"system/permission_denied":        true,
	"system/compact_boundary":         true,
	"system/status":                   true,
	"system/hook_started":             true,
	"system/hook_progress":            true,
	"system/hook_response":            true,
}

// Each event kind decodes its own fields, so a field whose shape differs in
// another event kind (e.g. a string "message") cannot break the known ones.
type eventHeader struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
}

type initEvent struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
}

type assistantEvent struct {
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Message         struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type resultEvent struct {
	Subtype string            `json:"subtype"`
	IsError bool              `json:"is_error"`
	Result  string            `json:"result"`
	Errors  []json.RawMessage `json:"errors"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type decodeState struct {
	d           *Driver
	emit        func(a2aagent.HarnessEvent)
	sessionID   string
	transcripts []string
	started     bool
	turnOpen    bool
	last        *resultEvent
	protoErr    error
	logged      map[string]bool
}

// Decode implements a2aagent.Driver (CR-P2-2): it reads to EOF, logs and skips
// unknown events, and treats the last result before EOF as final.
func (d *Driver) Decode(r io.Reader, emit func(a2aagent.HarnessEvent)) (a2aagent.Outcome, error) {
	if emit == nil {
		emit = func(a2aagent.HarnessEvent) {}
	}
	s := &decodeState{d: d, emit: emit, logged: map[string]bool{}}
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, tooLong, err := readLine(br)
		if tooLong {
			s.logf("skipped a stream-json line over %d bytes", maxEventBytes)
		} else if len(bytes.TrimSpace(line)) > 0 {
			s.handle(line)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return s.outcome(), fmt.Errorf("read claude stream: %w", err)
		}
	}
	return s.finish()
}

// readLine returns one line without its newline; a line over maxEventBytes is drained and reported.
func readLine(br *bufio.Reader) ([]byte, bool, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > maxEventBytes {
				tooLong = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), tooLong, err
	}
}

func (s *decodeState) handle(line []byte) {
	var h eventHeader
	if err := json.Unmarshal(line, &h); err != nil {
		s.logf("skipped a line that is not a JSON object: %v", err)
		return
	}
	key := h.Type
	if h.Type == "system" {
		key += "/" + h.Subtype
	}
	switch key {
	case "system/init":
		var ev initEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			s.fail(fmt.Errorf("malformed system/init: %w", err))
			return
		}
		s.init(ev)
	case "assistant":
		var ev assistantEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			s.logf("skipped a malformed assistant event: %v", err)
			return
		}
		s.assistant(ev)
	case "result":
		var ev resultEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			s.fail(fmt.Errorf("malformed result: %w", err))
			return
		}
		s.result(ev)
	default:
		if !quietEvents[key] && !s.logged[key] {
			s.logged[key] = true
			s.logf("skipped unknown event %q", key)
		}
	}
}

func (s *decodeState) init(ev initEvent) {
	s.started = true
	s.turnOpen = true
	if ev.SessionID == "" {
		s.fail(errors.New("system/init without session_id"))
		return
	}
	if ev.SessionID == s.sessionID {
		return
	}
	if s.sessionID != "" {
		s.logf("session changed from %s to %s", s.sessionID, ev.SessionID)
	}
	s.sessionID = ev.SessionID
	for _, p := range TranscriptPaths(ev.Cwd, ev.SessionID) {
		if !contains(s.transcripts, p) {
			s.transcripts = append(s.transcripts, p)
		}
	}
	if projectDirName(ev.Cwd) == "" {
		s.logf("cannot derive the transcript directory for cwd %q", ev.Cwd)
	}
	s.emit(a2aagent.HarnessEvent{Kind: a2aagent.EventSession, HarnessSessionID: ev.SessionID})
}

func (s *decodeState) assistant(ev assistantEvent) {
	// Subagent messages carry a parent tool use; only the main thread is progress.
	if ev.ParentToolUseID != nil {
		return
	}
	var blocks []contentBlock
	if err := json.Unmarshal(ev.Message.Content, &blocks); err != nil {
		return
	}
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			texts = append(texts, b.Text)
		}
	}
	if len(texts) > 0 {
		s.emit(a2aagent.HarnessEvent{Kind: a2aagent.EventProgress, HarnessSessionID: s.sessionID, Text: strings.Join(texts, "\n")})
	}
}

func (s *decodeState) result(ev resultEvent) {
	if !s.started && !isErrorResult(&ev) {
		s.fail(errors.New("result before system/init"))
	}
	s.turnOpen = false
	s.last = &ev
}

func (s *decodeState) finish() (a2aagent.Outcome, error) {
	out := s.outcome()
	switch {
	case s.protoErr != nil:
		return out, s.protoErr
	case s.last == nil && !s.started:
		return out, fmt.Errorf("%w: no system/init", errNoOutcome)
	case s.last == nil:
		return out, errNoOutcome
	case isErrorResult(s.last):
		return out, resultError(s.last)
	case s.turnOpen:
		return out, fmt.Errorf("%w: a turn started after the last result", errNoOutcome)
	}
	out.FinalMessage = s.last.Result
	s.emit(a2aagent.HarnessEvent{Kind: a2aagent.EventFinal, HarnessSessionID: s.sessionID, Text: out.FinalMessage})
	return out, nil
}

func (s *decodeState) outcome() a2aagent.Outcome {
	return a2aagent.Outcome{HarnessSessionID: s.sessionID, Transcripts: append([]string(nil), s.transcripts...)}
}

func (s *decodeState) fail(err error) {
	if s.protoErr == nil {
		s.protoErr = err
	}
}

func (s *decodeState) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.d.opts.Log, "claude driver: "+format+"\n", args...)
}

// isErrorResult also treats non-success subtypes (error_max_turns and the like) as failures.
func isErrorResult(ev *resultEvent) bool {
	return ev.IsError || (ev.Subtype != "" && ev.Subtype != "success")
}

func resultError(ev *resultEvent) error {
	msgs := make([]string, 0, len(ev.Errors))
	for _, raw := range ev.Errors {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			msgs = append(msgs, text)
		} else {
			msgs = append(msgs, string(raw))
		}
	}
	if len(msgs) == 0 && ev.Result != "" {
		msgs = append(msgs, ev.Result)
	}
	subtype := ev.Subtype
	if subtype == "" {
		subtype = "error"
	}
	if len(msgs) == 0 {
		return fmt.Errorf("claude result %s", subtype)
	}
	return fmt.Errorf("claude result %s: %s", subtype, strings.Join(msgs, "; "))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
