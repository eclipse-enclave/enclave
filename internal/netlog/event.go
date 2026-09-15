// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package netlog defines the gateway network audit log format and the file
// handling around it: appending, scanning, following and rotation. The gateway
// writes the log; `enclave network log` reads it back and presents it through
// internal/netlogview.
package netlog

import "time"

// Event is one line of the network audit log. Field names and their JSON tags
// are a consumer-facing contract (`enclave network log --json` emits them
// verbatim); keep them stable.
type Event struct {
	Timestamp    string `json:"ts"`
	Type         string `json:"type"`
	Method       string `json:"method,omitempty"`
	Domain       string `json:"domain,omitempty"`
	Path         string `json:"path,omitempty"`
	Port         int    `json:"port,omitempty"`
	Status       int    `json:"status,omitempty"`
	RequestSize  int64  `json:"req_bytes,omitempty"`
	ResponseSize int64  `json:"resp_bytes,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	Verdict      string `json:"verdict"`
	Rule         string `json:"rule,omitempty"`
	Session      string `json:"session,omitempty"`
}

// Event types.
const (
	TypeHTTP    = "http"
	TypeTCP     = "tcp"
	TypeDNS     = "dns"
	TypeSession = "session"
)

// Verdicts.
const (
	VerdictPass = "pass"
	VerdictDeny = "deny"
	VerdictInfo = "info"
)

// RuleSessionStart marks the event a gateway writes when it starts.
const RuleSessionStart = "start"

// SinceSession is the --since value that anchors on a session boundary instead
// of a clock value. It is shared so the CLI's flag validation and the reader
// cannot disagree about the keyword.
const SinceSession = "session"

// TimeFormat is the timestamp layout every writer uses.
const TimeFormat = time.RFC3339Nano

// IsSessionMarker reports whether the event is a session boundary rather than
// an audited request.
func (e Event) IsSessionMarker() bool {
	return e.Type == TypeSession
}

// Time parses the event timestamp. The second return is false when the
// timestamp is missing or unparseable.
func (e Event) Time() (time.Time, bool) {
	if e.Timestamp == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(TimeFormat, e.Timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// SessionStartEvent builds the marker a gateway writes once per start.
func SessionStartEvent(session string, at time.Time) Event {
	return Event{
		Timestamp: at.UTC().Format(TimeFormat),
		Type:      TypeSession,
		Verdict:   VerdictInfo,
		Rule:      RuleSessionStart,
		Session:   session,
	}
}
