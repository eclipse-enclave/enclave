// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"strings"
	"testing"
)

func TestConstraintValueValidate(t *testing.T) {
	tests := []struct {
		name        string
		kind        string
		value       ConstraintValue
		hasResolver bool
		want        string
	}{
		{"any email", KindEmail, ConstraintValue{Any: true}, false, ""},
		{"any mixed", KindValue, ConstraintValue{Any: true, Values: []string{"x"}}, false, "cannot be combined"},
		{"email list", KindEmail, ConstraintValue{Values: []string{"a@x.com", "@y.com", "@*.z.com"}}, false, ""},
		{"email empty", KindEmail, ConstraintValue{}, false, "must not be empty"},
		{"email ids", KindEmail, ConstraintValue{IDs: []string{"x"}}, false, "takes"},
		{"email bad pattern", KindEmail, ConstraintValue{Values: []string{"nodomain"}}, false, "email pattern"},
		{"value list", KindValue, ConstraintValue{Values: []string{"STARRED"}}, false, ""},
		{"value empty", KindValue, ConstraintValue{}, false, "must not be empty"},
		{"value empty string", KindValue, ConstraintValue{Values: []string{""}}, false, "must not be empty"},
		{"value control", KindValue, ConstraintValue{Values: []string{"a\nb"}}, false, "control characters"},
		{"value long", KindValue, ConstraintValue{Values: []string{strings.Repeat("a", maxConstraintValueLength+1)}}, false, "longer than"},
		{"value within", KindValue, ConstraintValue{Within: []string{"x"}}, false, "takes"},
		{"resource ids", KindResource, ConstraintValue{IDs: []string{"1"}}, false, ""},
		{"resource within", KindResource, ConstraintValue{Within: []string{"F"}}, true, ""},
		{"resource within without resolver", KindResource, ConstraintValue{Within: []string{"F"}}, false, "needs a resolver"},
		{"resource empty", KindResource, ConstraintValue{}, true, "must not be empty"},
		{"resource values", KindResource, ConstraintValue{Values: []string{"x"}}, true, "takes"},
		{"resource empty id", KindResource, ConstraintValue{IDs: []string{""}}, true, "must not be empty"},
		{"unknown kind", "other", ConstraintValue{Values: []string{"x"}}, false, "unknown constraint kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.value.Validate(tt.kind, tt.hasResolver)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateEmailPattern(t *testing.T) {
	valid := []string{"user@example.com", "@example.com", "@*.example.com", "User@Example.COM", "@localhost"}
	for _, p := range valid {
		if err := ValidateEmailPattern(p); err != nil {
			t.Errorf("ValidateEmailPattern(%q) = %v", p, err)
		}
	}
	invalid := []string{"example.com", "@", "user@", "@*.com", "user@*.example.com", "@exa mple.com", "@exämple.com", "a@b@c.com", "@example.com.", "@-bad.com", "u\x01@x.com"}
	for _, p := range invalid {
		if err := ValidateEmailPattern(p); err == nil {
			t.Errorf("ValidateEmailPattern(%q) = nil, want error", p)
		}
	}
}

func TestMatchEmail(t *testing.T) {
	patterns := []string{"boss@partner.org", "@eclipsesource.com", "@*.eclipse.org"}
	tests := []struct {
		address string
		want    bool
	}{
		{"alice@eclipsesource.com", true},
		{"ALICE@EclipseSource.COM", true},
		{"boss@partner.org", true},
		{"Boss@Partner.org", true},
		{"other@partner.org", false},
		{"dev@ci.eclipse.org", true},
		{"dev@eclipse.org", false},
		{"dev@xeclipse.org", false},
		{"alice@eclipsesource.com.evil.com", false},
		{"alice@evil.com", false},
		// The domain is after the last "@", so a quoted local part with "@"
		// cannot borrow an allowed domain.
		{"x@eclipsesource.com@evil.com", false},
		{"a@b@eclipsesource.com", true},
		{"alice@eclipsesource.com.", false},
		{"alice@éclipsesource.com", false},
		{"@eclipsesource.com", false},
		{"alice@", false},
		{"no-at-sign", false},
	}
	for _, tt := range tests {
		if got := MatchEmail(patterns, tt.address); got != tt.want {
			t.Errorf("MatchEmail(%q) = %v, want %v", tt.address, got, tt.want)
		}
	}
}
