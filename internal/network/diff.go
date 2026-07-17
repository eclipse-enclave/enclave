// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import (
	"fmt"
	"sort"
	"strings"

	"enclave/internal/model"
)

// DiffResult describes the differences between effective policy and built-in defaults.
type DiffResult struct {
	ModeChanged                  bool
	EffectiveMode, DefaultMode   string
	DomainsAdded, DomainsRemoved []string
	ToolDomainsAdded             map[string][]string
}

// Diff computes the difference between an effective policy and built-in domain list.
func Diff(effective EffectivePolicy, builtInDomains []string, tool string) DiffResult {
	result := DiffResult{
		DefaultMode:      model.NetworkModeRestricted,
		EffectiveMode:    effective.Mode,
		ToolDomainsAdded: make(map[string][]string),
	}

	if effective.Mode != result.DefaultMode {
		result.ModeChanged = true
	}

	builtInSet := make(map[string]bool, len(builtInDomains))
	for _, d := range builtInDomains {
		builtInSet[strings.ToLower(d)] = true
	}

	effectiveSet := make(map[string]bool, len(effective.Domains))
	for _, d := range effective.Domains {
		effectiveSet[strings.ToLower(d)] = true
	}

	// Find added domains (in effective but not in built-in)
	for _, d := range effective.Domains {
		dl := strings.ToLower(d)
		if !builtInSet[dl] {
			result.DomainsAdded = append(result.DomainsAdded, dl)
		}
	}

	// Find removed domains (in built-in but not in effective)
	for _, d := range builtInDomains {
		dl := strings.ToLower(d)
		if !effectiveSet[dl] {
			result.DomainsRemoved = append(result.DomainsRemoved, dl)
		}
	}

	sort.Strings(result.DomainsAdded)
	sort.Strings(result.DomainsRemoved)

	// Tool-specific domains are all additions (no built-in per-tool domains)
	for t, domains := range effective.ToolDomains {
		if len(domains) > 0 {
			sorted := make([]string, len(domains))
			copy(sorted, domains)
			sort.Strings(sorted)
			result.ToolDomainsAdded[t] = sorted
		}
	}

	return result
}

// FormatDiff returns a human-readable diff output.
func FormatDiff(diff DiffResult) string {
	var b strings.Builder

	if diff.ModeChanged {
		fmt.Fprintf(&b, "Mode: %s (default: %s)\n", diff.EffectiveMode, diff.DefaultMode)
	} else {
		fmt.Fprintf(&b, "Mode: %s (unchanged)\n", diff.EffectiveMode)
	}

	if len(diff.DomainsAdded) == 0 && len(diff.DomainsRemoved) == 0 &&
		len(diff.ToolDomainsAdded) == 0 {
		b.WriteString("No domain changes from built-in defaults.\n")
		return b.String()
	}

	if len(diff.DomainsAdded) > 0 {
		b.WriteString("\nDomains added:\n")
		for _, d := range diff.DomainsAdded {
			fmt.Fprintf(&b, "  + %s\n", d)
		}
	}

	if len(diff.DomainsRemoved) > 0 {
		b.WriteString("\nDomains removed:\n")
		for _, d := range diff.DomainsRemoved {
			fmt.Fprintf(&b, "  - %s\n", d)
		}
	}

	tools := make([]string, 0, len(diff.ToolDomainsAdded))
	for t := range diff.ToolDomainsAdded {
		tools = append(tools, t)
	}
	sort.Strings(tools)
	for _, t := range tools {
		domains := diff.ToolDomainsAdded[t]
		if len(domains) > 0 {
			fmt.Fprintf(&b, "\nTool %s domains added:\n", t)
			for _, d := range domains {
				fmt.Fprintf(&b, "  + %s\n", d)
			}
		}
	}

	return b.String()
}
