// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"sort"
	"strings"
	"time"

	"enclave/internal/domainpattern"
)

// DomainSummary aggregates every event seen for one domain.
type DomainSummary struct {
	Domain    string
	Pass      int
	Deny      int
	Sent      int64
	Received  int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// Total is the number of events counted for the domain.
func (d DomainSummary) Total() int {
	return d.Pass + d.Deny
}

// Summary is the per-domain aggregate behind `network log --summary`.
type Summary struct {
	Domains   []DomainSummary
	TotalPass int
	TotalDeny int
}

// Aggregate counts events per domain. Session markers are not audit events and
// are skipped, as are events with no domain.
func Aggregate(events []Event) Summary {
	byDomain := map[string]*DomainSummary{}
	var summary Summary

	for _, event := range events {
		if event.IsSessionMarker() {
			continue
		}
		// The same normalization the --domain filter applies, so the two views of
		// one log cannot disagree about what counts as the same host.
		domain, err := domainpattern.NormalizeHost(event.Domain)
		if err != nil {
			continue
		}
		entry, ok := byDomain[domain]
		if !ok {
			entry = &DomainSummary{Domain: domain}
			byDomain[domain] = entry
		}
		switch strings.ToLower(strings.TrimSpace(event.Verdict)) {
		case VerdictDeny:
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
	// Busiest domains first, then alphabetical so equal counts stay stable.
	sort.Slice(summary.Domains, func(i, j int) bool {
		left, right := summary.Domains[i], summary.Domains[j]
		if left.Total() != right.Total() {
			return left.Total() > right.Total()
		}
		return left.Domain < right.Domain
	})
	return summary
}
