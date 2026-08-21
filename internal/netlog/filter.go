// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"fmt"
	"strings"
	"time"

	"enclave/internal/domainpattern"
)

// SinceSession is the --since value that resolves to a session boundary instead
// of a clock value. It is shared so the CLI's flag validation and the reader
// cannot disagree about the keyword.
const SinceSession = "session"

// Filter selects events for display. The zero value matches everything.
type Filter struct {
	// Since drops events older than this instant. The zero value disables the
	// bound.
	Since time.Time
	// Verdict, Domain and Type are optional. Domain is an allowlist-style
	// pattern ("api.github.com" or "*.github.com").
	Verdict string
	Domain  string
	Type    string
	// Session restricts events to the named session, matched exactly against
	// the event's session field. Concurrent sessions of the same project and
	// tool share one log file, so scoping the file is not enough to tell them
	// apart. Events written before session stamping existed carry no session and
	// are dropped by this filter.
	Session string
}

// selective reports whether any event-shaped filter is set. Session markers are
// dropped as soon as one is, so `--verdict deny` can never return a marker.
func (f Filter) selective() bool {
	return strings.TrimSpace(f.Verdict) != "" ||
		strings.TrimSpace(f.Domain) != "" ||
		strings.TrimSpace(f.Type) != ""
}

// Match reports whether the event passes the filter.
func (f Filter) Match(event Event) bool {
	if !f.Since.IsZero() {
		at, ok := event.Time()
		if !ok || at.Before(f.Since) {
			return false
		}
	}
	// The session bound is applied before the marker exemption: a marker only
	// belongs in a session-scoped read if it is that session's own marker, which
	// is also what `--since session` anchors on.
	if session := strings.TrimSpace(f.Session); session != "" && event.Session != session {
		return false
	}
	if event.IsSessionMarker() {
		return !f.selective()
	}
	if verdict := strings.TrimSpace(f.Verdict); verdict != "" && !strings.EqualFold(verdict, event.Verdict) {
		return false
	}
	if eventType := strings.TrimSpace(f.Type); eventType != "" && !strings.EqualFold(eventType, event.Type) {
		return false
	}
	if pattern := strings.TrimSpace(f.Domain); pattern != "" {
		host, err := domainpattern.NormalizeHost(event.Domain)
		if err != nil || !domainpattern.MatchNormalizedHost(host, pattern) {
			return false
		}
	}
	return true
}

// Normalize validates the user-supplied fields and returns a filter Match can
// use directly. Callers run it once so a bad pattern fails before any log is
// read, and because Domain matching requires a normalized pattern; the other
// fields Match tolerates unnormalized.
func (f Filter) Normalize() (Filter, error) {
	normalized := f
	normalized.Session = strings.TrimSpace(f.Session)
	normalized.Verdict = strings.ToLower(strings.TrimSpace(f.Verdict))
	switch normalized.Verdict {
	case "", VerdictPass, VerdictDeny:
	default:
		return f, fmt.Errorf("invalid verdict %q (use %s or %s)", f.Verdict, VerdictPass, VerdictDeny)
	}

	normalized.Type = strings.ToLower(strings.TrimSpace(f.Type))
	switch normalized.Type {
	case "", TypeDNS, TypeHTTP, TypeTCP:
	default:
		return f, fmt.Errorf("invalid type %q (use %s, %s or %s)", f.Type, TypeDNS, TypeHTTP, TypeTCP)
	}

	if pattern := strings.TrimSpace(f.Domain); pattern != "" {
		domain, err := domainpattern.Normalize(pattern)
		if err != nil {
			return f, fmt.Errorf("invalid domain %q: %w", f.Domain, err)
		}
		normalized.Domain = domain
	} else {
		normalized.Domain = ""
	}
	return normalized, nil
}

// Apply returns the events matching the filter.
func (f Filter) Apply(events []Event) []Event {
	matched := make([]Event, 0, len(events))
	for _, event := range events {
		if f.Match(event) {
			matched = append(matched, event)
		}
	}
	return matched
}
