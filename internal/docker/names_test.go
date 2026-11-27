// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"testing"
)

func TestPrimaryContainerName(t *testing.T) {
	tests := []struct {
		name    string
		summary Summary
		want    string
	}{
		{name: "docker name", summary: Summary{Names: []string{"/demo"}}, want: "demo"},
		{name: "id fallback", summary: Summary{ID: "1234567890abcdef"}, want: "1234567890ab"},
		{name: "raw short id", summary: Summary{ID: "shortid"}, want: "shortid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrimaryContainerName(tt.summary); got != tt.want {
				t.Fatalf("PrimaryContainerName() = %q, want %q", got, tt.want)
			}
		})
	}
}
