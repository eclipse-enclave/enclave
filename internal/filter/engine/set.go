// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"enclave/internal/domainpattern"
	"enclave/internal/filter"
)

// Set holds the engines of all active filters of a session.
type Set struct {
	engines []*Engine
}

// NewSet combines engines. Two filters governing an overlapping host, or the
// same filter twice, is an error.
func NewSet(engines ...*Engine) (*Set, error) {
	for i, a := range engines {
		for _, b := range engines[i+1:] {
			if a.policy.Filter == b.policy.Filter {
				return nil, fmt.Errorf("filter %s is active twice", a.policy.Filter)
			}
			for _, ha := range a.policy.Hosts {
				for _, hb := range b.policy.Hosts {
					if filter.GovernedOverlap(ha, hb) {
						return nil, fmt.Errorf("filters %s and %s both govern %s", a.policy.Filter, b.policy.Filter, overlapLabel(ha, hb))
					}
				}
			}
		}
	}
	return &Set{engines: append([]*Engine(nil), engines...)}, nil
}

func overlapLabel(a, b string) string {
	if a == b {
		return a
	}
	return a + " and " + b
}

// Engines returns the engines in the set.
func (s *Set) Engines() []*Engine { return append([]*Engine(nil), s.engines...) }

// Empty reports whether the set has no engines.
func (s *Set) Empty() bool { return s == nil || len(s.engines) == 0 }

// For returns the engine governing host or, when the TLS server name sni is
// governed instead, that engine, whose Evaluate then denies the mismatch. It
// returns nil when neither is governed. A filter governs every name equal to
// or below its host patterns (filter.Governs), so a subdomain without a route
// reaches the engine and is denied there.
func (s *Set) For(host, sni string) *Engine {
	if s == nil {
		return nil
	}
	for _, candidate := range []string{host, sni} {
		if candidate == "" {
			continue
		}
		for _, e := range s.engines {
			if e.Governs(candidate) {
				return e
			}
		}
	}
	return nil
}

// Confined reports whether a normalized host is equal to or below a domain an
// active filter confines and no active filter governs it.
func (s *Set) Confined(host string) bool {
	if s == nil {
		return false
	}
	var confined, governed []string
	for _, e := range s.engines {
		confined = append(confined, e.policy.Confine...)
		for _, pattern := range e.policy.Hosts {
			governed = append(governed, filter.GovernedDomain(pattern))
		}
	}
	return domainpattern.Confined(host, confined, governed)
}

// CheckUngoverned enforces I11 for a request to a host no filter governs: a
// filter placeholder anywhere in the URL or headers denies the request.
func (s *Set) CheckUngoverned(req *http.Request) *Denial {
	if s == nil || req == nil {
		return nil
	}
	for _, e := range s.engines {
		if leaks(e, req) {
			return renderDenial(e.policy.Filter, nil, http.StatusForbidden, filter.StatusPermissionDenied, ReasonPlaceholder,
				"the filter placeholder may only be sent to the filter's governed hosts")
		}
	}
	return nil
}

func leaks(e *Engine, req *http.Request) bool {
	placeholder := e.policy.Placeholder
	lower := strings.ToLower(placeholder)
	if strings.Contains(strings.ToLower(req.Host), lower) {
		return true
	}
	for name, values := range req.Header {
		if strings.Contains(strings.ToLower(name), lower) {
			return true
		}
		for _, v := range values {
			if strings.Contains(v, placeholder) {
				return true
			}
		}
	}
	if req.URL == nil {
		return false
	}
	if strings.Contains(req.URL.EscapedPath(), placeholder) || strings.Contains(req.URL.Path, placeholder) ||
		strings.Contains(req.URL.RawQuery, placeholder) {
		return true
	}
	// ParseQuery returns what it could decode even on error.
	decoded, _ := url.ParseQuery(req.URL.RawQuery)
	for name, values := range decoded {
		if strings.Contains(name, placeholder) {
			return true
		}
		for _, v := range values {
			if strings.Contains(v, placeholder) {
				return true
			}
		}
	}
	return false
}
