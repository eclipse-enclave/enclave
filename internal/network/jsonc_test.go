// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import (
	"testing"
)

func TestStripLineComments(t *testing.T) {
	input := []byte(`{
  // this is a comment
  "key": "value"
}`)
	want := "{" + "\n" + "  " + "\n" + "  \"key\": \"value\"" + "\n" + "}"
	got := string(StripJSONCComments(input))
	if got != want {
		t.Fatalf("line comment:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestStripBlockComments(t *testing.T) {
	input := []byte(`{
  /* block comment */
  "key": "value"
}`)
	want := "{" + "\n" + "  " + "\n" + "  \"key\": \"value\"" + "\n" + "}"
	got := string(StripJSONCComments(input))
	if got != want {
		t.Fatalf("block comment:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestPreserveCommentsInStrings(t *testing.T) {
	input := []byte(`{"url": "https://example.com"}`)
	want := `{"url": "https://example.com"}`
	got := string(StripJSONCComments(input))
	if got != want {
		t.Fatalf("string preservation:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestPreserveSlashesInStrings(t *testing.T) {
	input := []byte(`{"comment": "// not a comment", "block": "/* also not */"}`)
	want := `{"comment": "// not a comment", "block": "/* also not */"}`
	got := string(StripJSONCComments(input))
	if got != want {
		t.Fatalf("string slashes:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEscapedQuotes(t *testing.T) {
	input := []byte(`{"key": "val\"ue"} // comment`)
	want := `{"key": "val\"ue"} `
	got := string(StripJSONCComments(input))
	if got != want {
		t.Fatalf("escaped quotes:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEmptyInput(t *testing.T) {
	got := string(StripJSONCComments([]byte{}))
	if got != "" {
		t.Fatalf("empty input: got %q, want empty", got)
	}
}

func TestMultilineBlockComment(t *testing.T) {
	input := []byte(`{
  /*
   * multi-line
   * block comment
   */
  "key": "value"
}`)
	want := "{" + "\n" + "  " + "\n" + "  \"key\": \"value\"" + "\n" + "}"
	got := string(StripJSONCComments(input))
	if got != want {
		t.Fatalf("multiline block:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestNoComments(t *testing.T) {
	input := []byte(`{"key": "value", "num": 42}`)
	want := `{"key": "value", "num": 42}`
	got := string(StripJSONCComments(input))
	if got != want {
		t.Fatalf("no comments:\ngot:  %q\nwant: %q", got, want)
	}
}
