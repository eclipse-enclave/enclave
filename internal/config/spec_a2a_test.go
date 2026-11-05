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

const a2aSpecHead = `
schemaVersion: "1"
kind: sandbox
name: demo
sandbox:
  entrypoint: { run: [demo] }
`

func TestSpecA2ABlockProjectsOntoProfile(t *testing.T) {
	src := a2aSpecHead + `  a2a:
    command: [demo, -p]
    newSessionArgs: [--session-id, "{sessionId}"]
    resumeArgs: [--resume, "{sessionId}"]
    eventFormat: claude-stream-json
    versionCommand: [demo, --version]
    versions: ">=1.0.0 <2.0.0"
    skills:
      - { id: coding, name: Coding, description: Writes code., tags: [code] }
`
	doc, err := decodeSpecDocument([]byte(src), "spec.yaml")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	p := specToProfile(doc)
	if p.A2A == nil {
		t.Fatal("profile lost the a2a block")
	}
	if got := strings.Join(p.A2A.ResumeArgs, " "); got != "--resume {sessionId}" {
		t.Fatalf("resumeArgs = %q", got)
	}
	if p.A2A.EventFormat != "claude-stream-json" || p.A2A.Versions != ">=1.0.0 <2.0.0" || len(p.A2A.Skills) != 1 {
		t.Fatalf("a2a block = %+v", p.A2A)
	}
}

func TestSpecWithoutA2ABlockHasNilProfileField(t *testing.T) {
	doc, err := decodeSpecDocument([]byte(a2aSpecHead), "spec.yaml")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p := specToProfile(doc); p.A2A != nil {
		t.Fatalf("A2A = %+v, want nil", p.A2A)
	}
}

func TestSpecA2ABlockRejectsInvalidContent(t *testing.T) {
	cases := map[string]struct {
		block string
		want  string
	}{
		"unknown key": {
			block: "command: [demo]\n    resumeArgs: [--resume, \"{sessionId}\"]\n    eventFormat: claude-stream-json\n    resumeArg: x\n",
			want:  `unknown field "resumeArg"`,
		},
		"missing command": {
			block: "resumeArgs: [--resume, \"{sessionId}\"]\n    eventFormat: claude-stream-json\n",
			want:  "sandbox.a2a.command is required",
		},
		"unknown event format": {
			block: "command: [demo]\n    resumeArgs: [--resume, \"{sessionId}\"]\n    eventFormat: jsonl\n",
			want:  `sandbox.a2a.eventFormat "jsonl" is not supported`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeSpecDocument([]byte(a2aSpecHead+"  a2a:\n    "+tc.block), "spec.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "spec.yaml") {
				t.Fatalf("err = %v, want the spec path", err)
			}
		})
	}
}
