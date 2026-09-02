// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"testing"

	"enclave/internal/extinstall"
	"enclave/internal/model"
)

// The rejection messages are user-visible, so they are asserted verbatim.
func TestParseExtensionFlagCombinations(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "add --json without --yes",
			args: []string{"features", "add", "acme/kits", "--json"},
			want: "--json cannot prompt; pass --yes",
		},
		{
			name: "update --json without --yes",
			args: []string{"features", "update", "--json"},
			want: "--json cannot prompt; pass --yes",
		},
		{
			name: "remove --json without --yes",
			args: []string{"tools", "remove", "demo", "--json"},
			want: "--json cannot prompt; pass --yes",
		},
		{
			name: "add --all with --name",
			args: []string{"features", "add", "acme/kits", "--all", "--name", "foo"},
			want: "--all and --name are mutually exclusive",
		},
		{
			name: "add --path with --name",
			args: []string{"features", "add", "acme/kits", "--path", "extensions/features/foo", "--name", "foo"},
			want: "--path and --name are mutually exclusive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.args, model.Options{})
			if err == nil {
				t.Fatalf("Parse(%v) was accepted, request = %+v", tc.args, res.ExtRequest)
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err.Error(), tc.want)
			}
			if res.Action != ActionExtensionManage || res.ExtRequest == nil {
				t.Fatalf("action = %q, request = %+v: a rejected request must still be published", res.Action, res.ExtRequest)
			}
		})
	}
}

// Interactive is derived at the CLI boundary from --yes and --json.
func TestParseExtensionInteractivity(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "plain add prompts", args: []string{"features", "add", "acme/kits"}, want: true},
		{name: "--yes does not prompt", args: []string{"features", "add", "acme/kits", "--yes"}, want: false},
		{name: "--json --yes does not prompt", args: []string{"features", "add", "acme/kits", "--json", "--yes"}, want: false},
		{name: "plain update prompts", args: []string{"tools", "update"}, want: true},
		{name: "remove --yes does not prompt", args: []string{"tools", "remove", "demo", "--yes"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.args, model.Options{})
			if err != nil {
				t.Fatalf("Parse(%v): %v", tc.args, err)
			}
			if res.ExtRequest == nil {
				t.Fatal("ExtRequest is nil")
			}
			if res.ExtRequest.Interactive != tc.want {
				t.Fatalf("Interactive = %v, want %v", res.ExtRequest.Interactive, tc.want)
			}
		})
	}
}

// A request built without the CLI must not be able to block on a prompt.
func TestExtensionRequestZeroValueIsNonInteractive(t *testing.T) {
	if (extinstall.Request{}).Interactive {
		t.Error("a zero-value Request is interactive")
	}
}
