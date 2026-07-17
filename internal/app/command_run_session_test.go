// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"reflect"
	"testing"

	"enclave/internal/model"
)

func TestResolveSessionActionArgs(t *testing.T) {
	cases := []struct {
		name         string
		action       string
		profile      model.Profile
		wantArgs     []string
		wantMode     string
		wantFallback bool
		wantErr      bool
	}{
		{
			name:   "continue uses continue",
			action: "continue",
			profile: model.Profile{
				Name:         "tool",
				ContinueArgs: []string{"--continue"},
				ResumeArgs:   []string{"--resume"},
			},
			wantArgs: []string{"--continue"},
			wantMode: "continue",
		},
		{
			name:         "continue falls back to resume",
			action:       "continue",
			profile:      model.Profile{Name: "tool", ResumeArgs: []string{"--resume"}},
			wantArgs:     []string{"--resume"},
			wantMode:     "resume",
			wantFallback: true,
		},
		{
			name:     "resume uses resume",
			action:   "resume",
			profile:  model.Profile{Name: "tool", ResumeArgs: []string{"--resume"}},
			wantArgs: []string{"--resume"},
			wantMode: "resume",
		},
		{
			name:         "resume falls back to continue",
			action:       "resume",
			profile:      model.Profile{Name: "tool", ContinueArgs: []string{"--continue"}},
			wantArgs:     []string{"--continue"},
			wantMode:     "continue",
			wantFallback: true,
		},
		{
			name:    "unsupported continue",
			action:  "continue",
			profile: model.Profile{Name: "tool"},
			wantErr: true,
		},
		{
			name:    "unsupported resume",
			action:  "resume",
			profile: model.Profile{Name: "tool"},
			wantErr: true,
		},
		{
			name:    "non-session action",
			action:  "run",
			profile: model.Profile{Name: "tool"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, mode, fallback, err := resolveSessionActionArgs(tc.action, tc.profile)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tc.wantArgs)
			}
			if mode != tc.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tc.wantMode)
			}
			if fallback != tc.wantFallback {
				t.Fatalf("fallback = %v, want %v", fallback, tc.wantFallback)
			}
		})
	}
}

func TestCompactProfileArgsTrimsAndSkipsEmpty(t *testing.T) {
	got := compactProfileArgs([]string{" --resume ", "", "  ", "--last"})
	want := []string{"--resume", "--last"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
