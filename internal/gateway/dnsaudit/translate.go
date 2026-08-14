// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package dnsaudit turns dnsmasq's own log into network audit events. It runs
// as its own process next to dnsmasq so DNS denials are recorded even when the
// MITM proxy is disabled.
package dnsaudit

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"enclave/internal/domainpattern"
	"enclave/internal/netlog"
)

// Options configures the translator.
type Options struct {
	// DNSMasqLogPath is the log dnsmasq writes with --log-queries.
	DNSMasqLogPath string
	// NetworkLogPath is the shared audit log to append events to.
	NetworkLogPath string
	// Session names the gateway, matching what the proxy stamps.
	Session string
	// Interval overrides the poll interval.
	Interval time.Duration
	// FromStart translates the existing log contents before following it.
	FromStart bool
}

// Run follows the dnsmasq log until the context is cancelled, appending an
// event for every denied or failed lookup.
func Run(ctx context.Context, opts Options) error {
	if strings.TrimSpace(opts.DNSMasqLogPath) == "" {
		return fmt.Errorf("dnsmasq log path is required")
	}
	appender, err := netlog.NewAppender(opts.NetworkLogPath)
	if err != nil {
		return fmt.Errorf("open network log: %w", err)
	}
	defer func() { _ = appender.Close() }()

	follower := netlog.NewFollower([]string{opts.DNSMasqLogPath}, netlog.FollowOptions{
		Interval:  opts.Interval,
		FromStart: opts.FromStart,
	})
	return follower.RunLines(ctx, func(line []byte) {
		// Queries, forwards and replies are most of the log and none of them can
		// match, so they are rejected before the line is even copied to a string.
		if !bytes.Contains(line, answerSeparatorBytes) {
			return
		}
		// The dnsmasq line carries a local timestamp with no year or zone, so
		// the event is stamped when it is read instead. The translator tails
		// the log live, which keeps the two within milliseconds.
		event, ok := ParseLine(string(line), time.Now(), opts.Session)
		if !ok {
			return
		}
		appender.Append(event)
	})
}

// answerSeparator is what every answer line dnsmasq logs has in common:
// "<source> <domain> is <status>".
const answerSeparator = " is "

var answerSeparatorBytes = []byte(answerSeparator)

// rules maps a dnsmasq answer status to the rule recorded on the event. Only
// answers that denied or failed a lookup are listed; anything else is normal
// traffic and produces no event.
//
// NODATA is deliberately absent. dnsmasq answers NODATA-IPv6 for every AAAA
// lookup of an allowlisted host that has no IPv6 record, so recording it would
// report allowed domains as denied.
var rules = map[string]string{
	"NXDOMAIN": "nxdomain",
	"SERVFAIL": "servfail",
	"REFUSED":  "refused",
}

// ParseLine converts one dnsmasq log line into a DNS event. The second return
// is false for the lines that are not denials, which is most of them.
//
// The shapes handled, taken from real `--log-queries` output:
//
//	Aug 13 12:57:32 dnsmasq[455]: config telemetry.example.com is NXDOMAIN
//	Aug 13 12:57:32 dnsmasq[455]: reply example.com is SERVFAIL
//	Aug 13 12:57:32 dnsmasq[455]: cached example.com is REFUSED
//
// "config" means the answer came from enclave's own blackhole, so it is a
// policy denial. "reply" and "cached" mean upstream failed the lookup, which
// the rule records separately.
func ParseLine(line string, at time.Time, session string) (netlog.Event, bool) {
	// Queries, forwards, replies and ipset updates are the bulk of the log and
	// none of them can match. Rejecting them on a substring search keeps the
	// per-line cost at zero allocations.
	if !strings.Contains(line, answerSeparator) {
		return netlog.Event{}, false
	}

	message := strings.TrimSpace(line)
	if index := strings.Index(message, "]: "); index >= 0 {
		message = strings.TrimSpace(message[index+len("]: "):])
	}

	// Split on the answer separator and check the status first: every resolved
	// query logs a "reply <domain> is <address>" line, and rejecting those on the
	// status alone keeps them from being split into fields for nothing.
	separator := strings.LastIndex(message, answerSeparator)
	if separator < 0 {
		return netlog.Event{}, false
	}
	status := message[separator+len(answerSeparator):]
	rule, denied := rules[strings.ToUpper(status)]
	if !denied {
		return netlog.Event{}, false
	}

	fields := strings.Fields(message[:separator])
	if len(fields) != 2 {
		return netlog.Event{}, false
	}
	source, domain := fields[0], fields[1]
	switch source {
	case "config":
	case "reply", "cached":
		rule = "upstream-" + rule
	default:
		return netlog.Event{}, false
	}

	// dnsmasq is the writer enclave controls least, so the domain goes through
	// the same normalization the reader filters and aggregates by.
	domain, err := domainpattern.NormalizeHost(domain)
	if err != nil {
		return netlog.Event{}, false
	}

	return netlog.Event{
		Timestamp: at.UTC().Format(netlog.TimeFormat),
		Type:      netlog.TypeDNS,
		Domain:    domain,
		Verdict:   netlog.VerdictDeny,
		Rule:      rule,
		Session:   session,
	}, true
}
