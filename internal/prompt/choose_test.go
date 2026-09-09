// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package prompt

import (
	"bytes"
	"strings"
	"testing"
)

func TestChoose(t *testing.T) {
	options := []string{"podman", "docker"}
	for _, tc := range []struct {
		name   string
		input  string
		want   string
		prompt int
	}{
		{name: "full word", input: "docker\n", want: "docker", prompt: 1},
		{name: "case and whitespace", input: "  PODMAN \n", want: "podman", prompt: 1},
		{name: "unique first letter", input: "p\n", want: "podman", prompt: 1},
		{name: "re-asks after unknown answer", input: "lxc\n\ndocker\n", want: "docker", prompt: 3},
		{name: "gives up after three unknown answers", input: "a\nb\nc\nd\n", want: "", prompt: 3},
		{name: "eof", input: "", want: "", prompt: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := Choose("Which engine?", options, strings.NewReader(tc.input), &out)
			if err != nil {
				t.Fatalf("Choose: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Choose() = %q, want %q", got, tc.want)
			}
			if n := strings.Count(out.String(), "Which engine? [podman/docker]: "); n != tc.prompt {
				t.Fatalf("prompted %d times, want %d: %q", n, tc.prompt, out.String())
			}
		})
	}
}

func TestChooseRejectsAmbiguousFirstLetter(t *testing.T) {
	got, err := Choose("Pick", []string{"docker", "dagger"}, strings.NewReader("d\n"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got != "" {
		t.Fatalf("ambiguous first letter must not match, got %q", got)
	}
}
