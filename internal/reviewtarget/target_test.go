// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package reviewtarget

import "testing"

func TestParseTarget(t *testing.T) {
	tests := []struct {
		in   string
		want Target
	}{
		{"", Target{Kind: KindUncommitted, Raw: "uncommitted"}},
		{"   ", Target{Kind: KindUncommitted, Raw: "uncommitted"}},
		{"uncommitted", Target{Kind: KindUncommitted, Raw: "uncommitted"}},
		{"pr:42", Target{Kind: KindPR, Raw: "pr:42", PRNumber: 42}},
		{"PR:7", Target{Kind: KindPR, Raw: "PR:7", PRNumber: 7}},
		{"main..HEAD", Target{Kind: KindRange, Raw: "main..HEAD", BaseRef: "main", HeadRef: "HEAD"}},
		{"main...HEAD", Target{Kind: KindRange, Raw: "main...HEAD", BaseRef: "main", HeadRef: "HEAD", ThreeDot: true}},
		{"main..", Target{Kind: KindRange, Raw: "main..", BaseRef: "main", HeadRef: "HEAD"}},
		{"..HEAD", Target{Kind: KindRange, Raw: "..HEAD", BaseRef: "HEAD", HeadRef: "HEAD"}},
		{"abc1234", Target{Kind: KindRef, Raw: "abc1234", Ref: "abc1234"}},
		{"feature/x", Target{Kind: KindRef, Raw: "feature/x", Ref: "feature/x"}},
	}
	for _, tc := range tests {
		got, err := ParseTarget(tc.in)
		if err != nil {
			t.Errorf("ParseTarget(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseTarget(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestParseTargetErrors(t *testing.T) {
	for _, in := range []string{"pr:", "pr:abc", "pr:0", "pr:-3", "-x", "-x..HEAD", "main..-x"} {
		if _, err := ParseTarget(in); err == nil {
			t.Errorf("ParseTarget(%q) = nil error, want error", in)
		}
	}
}
