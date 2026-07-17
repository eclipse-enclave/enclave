// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"strings"
	"testing"
)

func TestValidateStartupCommandsRejectsRoot(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr bool
	}{
		{
			name: "user 0 rejected",
			src: `
schemaVersion: "1"
kind: sandbox
name: demo
sandbox: { entrypoint: { run: [demo] } }
commands:
  startup:
    - { command: some-daemon, user: "0" }
`,
			wantErr: true,
		},
		{
			name: "user root rejected",
			src: `
schemaVersion: "1"
kind: sandbox
name: demo
sandbox: { entrypoint: { run: [demo] } }
commands:
  startup:
    - { command: some-daemon, user: "root" }
`,
			wantErr: true,
		},
		{
			name: "non-root user allowed",
			src: `
schemaVersion: "1"
kind: sandbox
name: demo
sandbox: { entrypoint: { run: [demo] } }
commands:
  startup:
    - { command: some-daemon, user: "1000" }
`,
			wantErr: false,
		},
		{
			name: "unset user allowed",
			src: `
schemaVersion: "1"
kind: sandbox
name: demo
sandbox: { entrypoint: { run: [demo] } }
commands:
  startup:
    - { command: some-daemon }
`,
			wantErr: false,
		},
		{
			name: "no commands block allowed",
			src: `
schemaVersion: "1"
kind: sandbox
name: demo
sandbox: { entrypoint: { run: [demo] } }
`,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustDoc(t, tc.src)
			err := validateStartupCommands(doc, "demo/spec.yaml")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), "root startup") {
					t.Fatalf("error %q does not mention %q", err.Error(), "root startup")
				}
			} else if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// TestLoadSpecRejectsRootStartup proves the validation is wired into the load
// path (LoadSpec) and the real profile-load path (LoadProfile).
func TestLoadSpecRejectsRootStartup(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")

	_, err := LoadSpec(paths, "rootstartup", KindSandbox)
	if err == nil {
		t.Fatalf("LoadSpec: expected root-startup rejection, got nil")
	}
	if !strings.Contains(err.Error(), "root startup") {
		t.Fatalf("LoadSpec error %q does not mention %q", err.Error(), "root startup")
	}

	if _, err := LoadProfile(paths, "rootstartup"); err == nil {
		t.Fatalf("LoadProfile: expected root-startup rejection, got nil")
	}
}
