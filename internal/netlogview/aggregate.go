// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlogview

import (
	"sort"
	"strings"
	"time"

	"enclave/internal/domainpattern"
	"enclave/internal/netlog"
)

// DomainSummary aggregates every event seen for one domain. The JSON tags are a
// consumer-facing contract (`network log --summary --json`); keep them stable.
type DomainSummary struct {
	Domain    string    `json:"domain"`
	Pass      int       `json:"pass"`
	Deny      int       `json:"deny"`
	Sent      int64     `json:"sent_bytes"`
	Received  int64     `json:"received_bytes"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Total is the number of events counted for the domain.
func (d DomainSummary) Total() int {
	return d.Pass + d.Deny
}

// Summary is the per-domain aggregate behind `network log --summary`.
type Summary struct {
	Domains   []DomainSummary `json:"domains"`
	TotalPass int             `json:"total_pass"`
	TotalDeny int             `json:"total_deny"`
}

// Aggregate counts events per domain. Session markers are not audit events and
// are skipped. Events with no domain are grouped under the empty domain: the
// proxy denies a connection before it learns a host name, and those denials
// have to reach the totals.
func Aggregate(events []netlog.Event) Summary {
	byDomain := map[string]*DomainSummary{}
	var summary Summary

	for _, event := range events {
		if event.IsSessionMarker() {
			continue
		}
		// The same normalization the --domain filter applies, so the two views of
		// one log cannot disagree about what counts as the same host. A host it
		// cannot use is the domainless group.
		domain, _ := domainpattern.NormalizeHost(event.Domain)
		entry, ok := byDomain[domain]
		if !ok {
			entry = &DomainSummary{Domain: domain}
			byDomain[domain] = entry
		}
		switch strings.ToLower(strings.TrimSpace(event.Verdict)) {
		case netlog.VerdictDeny:
			entry.Deny++
			summary.TotalDeny++
		default:
			entry.Pass++
			summary.TotalPass++
		}
		entry.Sent += event.RequestSize
		entry.Received += event.ResponseSize
		if at, ok := event.Time(); ok {
			if entry.FirstSeen.IsZero() || at.Before(entry.FirstSeen) {
				entry.FirstSeen = at
			}
			if at.After(entry.LastSeen) {
				entry.LastSeen = at
			}
		}
	}

	summary.Domains = make([]DomainSummary, 0, len(byDomain))
	for _, entry := range byDomain {
		summary.Domains = append(summary.Domains, *entry)
	}
	// Busiest domains first, then alphabetical so equal counts stay stable. The
	// domainless group is the least informative row, so it sinks below named
	// domains it ties with.
	sort.Slice(summary.Domains, func(i, j int) bool {
		left, right := summary.Domains[i], summary.Domains[j]
		if left.Total() != right.Total() {
			return left.Total() > right.Total()
		}
		if left.Domain == "" || right.Domain == "" {
			return right.Domain == ""
		}
		return left.Domain < right.Domain
	})
	return summary
}
