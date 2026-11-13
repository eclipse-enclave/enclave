// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package spec

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"enclave/internal/filter"
)

func validateScope(scope string) error {
	if scope == "" || hasControlOrSpace(scope) {
		return fmt.Errorf("invalid scope %q: must be printable without spaces", scope)
	}
	return nil
}

func fullScope(prefix, scope string) string {
	if prefix == "" || strings.Contains(scope, "://") {
		return scope
	}
	return prefix + scope
}

func buildScopes(scopes []string, oauth *OAuth2) ([]string, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	if oauth == nil {
		return nil, fmt.Errorf("scopes need an oauth2 credential")
	}
	var out []string
	for _, s := range scopes {
		if err := validateScope(s); err != nil {
			return nil, err
		}
		out = appendUnique(out, fullScope(oauth.ScopePrefix, s))
	}
	sort.Strings(out)
	return out, nil
}

// impliesCycle returns a scope on a cycle of the implies graph, or "".
func impliesCycle(implies map[string][]string) string {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var visit func(string) string
	visit = func(s string) string {
		switch state[s] {
		case visiting:
			return s
		case done:
			return ""
		}
		state[s] = visiting
		for _, next := range implies[s] {
			if found := visit(next); found != "" {
				return found
			}
		}
		state[s] = done
		return ""
	}
	for _, s := range filter.SortedKeys(implies) {
		if found := visit(s); found != "" {
			return found
		}
	}
	return ""
}

// FullScope applies the scope prefix to a short scope name. Full URLs and
// scopes of a filter without a prefix are returned as they are.
func (f *Filter) FullScope(scope string) string {
	if f.Credential.OAuth2 == nil {
		return scope
	}
	return fullScope(f.Credential.OAuth2.ScopePrefix, scope)
}

// ExpandScopes returns the scopes plus every scope they imply, transitively.
func (f *Filter) ExpandScopes(scopes []string) map[string]bool {
	expanded := map[string]bool{}
	var implies map[string][]string
	if f.Credential.OAuth2 != nil {
		implies = f.Credential.OAuth2.ScopeImplies
	}
	var add func(string)
	add = func(s string) {
		if expanded[s] {
			return
		}
		expanded[s] = true
		for _, next := range implies[s] {
			add(next)
		}
	}
	for _, s := range scopes {
		add(f.FullScope(s))
	}
	return expanded
}

// MissingScopes returns the required scopes the granted scopes do not cover
// through scopeImplies, sorted. Empty means the grant covers the requirement.
func (f *Filter) MissingScopes(granted, required []string) []string {
	have := f.ExpandScopes(granted)
	var missing []string
	for _, s := range required {
		full := f.FullScope(s)
		if !have[full] {
			missing = appendUnique(missing, full)
		}
	}
	sort.Strings(missing)
	return missing
}

// Covers reports whether granted covers required.
func (f *Filter) Covers(granted, required []string) bool {
	return len(f.MissingScopes(granted, required)) == 0
}

// MinimizeScopes drops every scope another scope of the set implies, so a
// login requests no redundant scopes. The result is sorted.
func (f *Filter) MinimizeScopes(scopes []string) []string {
	var full []string
	for _, s := range scopes {
		full = appendUnique(full, f.FullScope(s))
	}
	var out []string
	for _, s := range full {
		redundant := false
		for _, other := range full {
			if other != s && f.ExpandScopes([]string{other})[s] {
				redundant = true
				break
			}
		}
		if !redundant {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// RequiredScopes returns the full scopes a preset needs: the scopes of every
// enabled capability plus the scopes of each resolver a "within" constraint
// uses. The result is sorted.
func (f *Filter) RequiredScopes(p *Preset) []string {
	var scopes []string
	for _, capName := range filter.SortedKeys(p.Capabilities) {
		capDef := f.Capabilities[capName]
		for _, s := range capDef.Scopes {
			scopes = appendUnique(scopes, s)
		}
		for cname, value := range p.Capabilities[capName] {
			con := capDef.Constraints[cname]
			if value.Any || len(value.Within) == 0 || con.Resolver == "" {
				continue
			}
			for _, s := range f.Resolvers[con.Resolver].Scopes {
				scopes = appendUnique(scopes, s)
			}
		}
	}
	sort.Strings(scopes)
	return scopes
}

// GrantCandidate describes a stored grant for SelectGrant.
type GrantCandidate struct {
	ID        string
	Scopes    []string
	CreatedAt time.Time
}

// SelectGrant picks the grant covering required with the fewest scopes, then
// the newest, then the smallest ID. It reports false when none covers.
func (f *Filter) SelectGrant(grants []GrantCandidate, required []string) (GrantCandidate, bool) {
	var best GrantCandidate
	found := false
	for _, g := range grants {
		if !f.Covers(g.Scopes, required) {
			continue
		}
		if !found || better(g, best) {
			best, found = g, true
		}
	}
	return best, found
}

func better(a, b GrantCandidate) bool {
	if len(a.Scopes) != len(b.Scopes) {
		return len(a.Scopes) < len(b.Scopes)
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID < b.ID
}
