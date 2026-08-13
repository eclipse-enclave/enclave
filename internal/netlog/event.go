// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package netlog defines the gateway network audit log format and the reader
// side that turns it back into events. The gateway writes it; `enclave network
// log` reads it.
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

// TimeFormat is the timestamp layout every writer uses.
const TimeFormat = time.RFC3339Nano

// IsSessionMarker reports whether the event is a session boundary rather than
// an audited request. Markers are excluded from aggregation and from verdict,
// domain and type filtering.
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
