// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"fmt"
	"strings"

	"enclave/internal/domainpattern"
)

// Constraint kinds.
const (
	KindEmail    = "email"
	KindResource = "resource"
	KindValue    = "value"
)

// ValidConstraintKind reports whether kind is a constraint kind.
func ValidConstraintKind(kind string) bool {
	return kind == KindEmail || kind == KindResource || kind == KindValue
}

const maxConstraintValueLength = 1024

// ConstraintValue is what a preset sets for a constraint. Any is the "*"
// value. Values holds email patterns or allowed values, IDs and Within the
// resource form.
type ConstraintValue struct {
	Any    bool     `json:"any,omitempty"`
	Values []string `json:"values,omitempty"`
	IDs    []string `json:"ids,omitempty"`
	Within []string `json:"within,omitempty"`
}

// Validate checks that the value fits kind. hasResolver tells whether the
// constraint declares a resolver, which "within" needs.
func (v ConstraintValue) Validate(kind string, hasResolver bool) error {
	if v.Any {
		if len(v.Values)+len(v.IDs)+len(v.Within) > 0 {
			return fmt.Errorf("\"*\" cannot be combined with other values")
		}
		return nil
	}
	switch kind {
	case KindEmail:
		if len(v.IDs)+len(v.Within) > 0 {
			return fmt.Errorf("an email constraint takes \"*\" or a list of addresses and domains")
		}
		if len(v.Values) == 0 {
			return fmt.Errorf("must not be empty, use \"*\" or list addresses and domains")
		}
		for _, pattern := range v.Values {
			if err := ValidateEmailPattern(pattern); err != nil {
				return err
			}
		}
	case KindValue:
		if len(v.IDs)+len(v.Within) > 0 {
			return fmt.Errorf("a value constraint takes \"*\" or a list of strings")
		}
		if len(v.Values) == 0 {
			return fmt.Errorf("must not be empty, use \"*\" or list values")
		}
		if err := validateConstraintStrings(v.Values, "value"); err != nil {
			return err
		}
	case KindResource:
		if len(v.Values) > 0 {
			return fmt.Errorf("a resource constraint takes \"*\" or {ids, within}")
		}
		if len(v.IDs)+len(v.Within) == 0 {
			return fmt.Errorf("must not be empty, use \"*\" or set ids or within")
		}
		if len(v.Within) > 0 && !hasResolver {
			return fmt.Errorf("within needs a resolver on the constraint")
		}
		if err := validateConstraintStrings(v.IDs, "id"); err != nil {
			return err
		}
		if err := validateConstraintStrings(v.Within, "within id"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown constraint kind %q", kind)
	}
	return nil
}

func validateConstraintStrings(values []string, label string) error {
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("%s must not be empty", label)
		}
		if len(value) > maxConstraintValueLength {
			return fmt.Errorf("%s %.40q... is longer than %d bytes", label, value, maxConstraintValueLength)
		}
		if hasControl(value) {
			return fmt.Errorf("%s %q contains control characters", label, value)
		}
	}
	return nil
}

// ValidateEmailPattern checks "user@domain", "@domain", or "@*.domain".
func ValidateEmailPattern(pattern string) error {
	at := strings.LastIndex(pattern, "@")
	if at < 0 {
		return fmt.Errorf("email pattern %q must be user@domain, @domain, or @*.domain", pattern)
	}
	local, domain := pattern[:at], pattern[at+1:]
	if !isASCII(pattern) || hasControl(pattern) || strings.ContainsAny(pattern, " \t") {
		return fmt.Errorf("email pattern %q must be printable ASCII without spaces", pattern)
	}
	if strings.Contains(local, "@") {
		return fmt.Errorf("email pattern %q contains more than one \"@\"", pattern)
	}
	normalized, err := domainpattern.Normalize(domain)
	if err != nil || normalized == "" {
		return fmt.Errorf("email pattern %q has an invalid domain", pattern)
	}
	if normalized != asciiLower(domain) {
		return fmt.Errorf("email pattern %q has an invalid domain", pattern)
	}
	if local != "" && strings.HasPrefix(domain, "*.") {
		return fmt.Errorf("email pattern %q: a wildcard domain cannot have a user part", pattern)
	}
	return nil
}

// MatchEmail reports whether address matches one of patterns. Matching is
// ASCII case-insensitive. The domain is the part after the last "@". A
// non-ASCII domain never matches.
func MatchEmail(patterns []string, address string) bool {
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return false
	}
	local, domain := address[:at], address[at+1:]
	if !isASCII(domain) {
		return false
	}
	domain = asciiLower(domain)
	for _, pattern := range patterns {
		pat := asciiLower(pattern)
		patAt := strings.LastIndex(pat, "@")
		patLocal, patDomain := pat[:patAt], pat[patAt+1:]
		if suffix, ok := strings.CutPrefix(patDomain, "*."); ok {
			if strings.HasSuffix(domain, "."+suffix) && len(domain) > len(suffix)+1 {
				return true
			}
			continue
		}
		if domain != patDomain {
			continue
		}
		if patLocal == "" || asciiLower(local) == patLocal {
			return true
		}
	}
	return false
}
