// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSource(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		remote  string
		ref     string
		subpath string
	}{
		{"shorthand", "acme/kits", "https://github.com/acme/kits", "", ""},
		{"shorthand with subpath", "acme/kits/extensions/features/foo", "https://github.com/acme/kits", "", "extensions/features/foo"},
		{"shorthand strips git suffix", "acme/kits.git", "https://github.com/acme/kits", "", ""},
		{"https repo", "https://gitlab.com/acme/kits", "https://gitlab.com/acme/kits", "", ""},
		{"https repo strips git suffix", "https://gitlab.com/acme/kits.git", "https://gitlab.com/acme/kits", "", ""},
		{"github tree url", "https://github.com/acme/kits/tree/v1.2.0/extensions/tools/foo", "https://github.com/acme/kits", "v1.2.0", "extensions/tools/foo"},
		{"gitlab tree url", "https://gitlab.com/acme/kits/-/tree/main/extensions/features/foo", "https://gitlab.com/acme/kits", "main", "extensions/features/foo"},
		{"scp style", "git@github.com:acme/kits.git", "git@github.com:acme/kits", "", ""},
		{"ssh url", "ssh://git@host/acme/kits.git", "ssh://git@host/acme/kits", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSource(tc.raw)
			if err != nil {
				t.Fatalf("parseSource(%q): %v", tc.raw, err)
			}
			if got.RemoteURL != tc.remote {
				t.Errorf("remote = %q, want %q", got.RemoteURL, tc.remote)
			}
			if got.Ref != tc.ref {
				t.Errorf("ref = %q, want %q", got.Ref, tc.ref)
			}
			if got.Subpath != tc.subpath {
				t.Errorf("subpath = %q, want %q", got.Subpath, tc.subpath)
			}
			if got.Raw != tc.raw {
				t.Errorf("raw = %q, want %q", got.Raw, tc.raw)
			}
		})
	}
}

func TestParseSourceLocalPath(t *testing.T) {
	got, err := parseSource("./my-kits")
	if err != nil {
		t.Fatalf("parseSource: %v", err)
	}
	if got.Subpath != "" {
		t.Errorf("subpath = %q, want empty (local sources use --path)", got.Subpath)
	}
	if got.RemoteURL == "" || got.RemoteURL[0] != '/' {
		t.Errorf("remote = %q, want an absolute path", got.RemoteURL)
	}
}

func TestParseSourceExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("os.UserHomeDir: %v", err)
	}

	got, err := parseSource("~/my-kits")
	if err != nil {
		t.Fatalf("parseSource: %v", err)
	}
	want := filepath.Join(home, "my-kits")
	if got.RemoteURL != want {
		t.Errorf("remote = %q, want %q", got.RemoteURL, want)
	}

	gotHome, err := parseSource("~")
	if err != nil {
		t.Fatalf("parseSource: %v", err)
	}
	if gotHome.RemoteURL != home {
		t.Errorf("remote = %q, want %q", gotHome.RemoteURL, home)
	}
}

func TestParseSourceRejects(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"empty", "   "},
		{"single segment", "acme"},
		{"traversal in subpath", "acme/kits/../../etc"},
		{"empty segment", "acme//kits"},
		{"leading dash", "--upload-pack=evil"},
		{"blob url", "https://github.com/acme/kits/blob/main/extensions/features/foo/spec.yaml"},
		{"no host in url", "https:///acme/kits"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSource(tc.raw); err == nil {
				t.Fatalf("parseSource(%q) succeeded, want error", tc.raw)
			}
		})
	}
}

func TestWithRefConflict(t *testing.T) {
	src, err := parseSource("https://github.com/acme/kits/tree/v1/extensions/features/foo")
	if err != nil {
		t.Fatalf("parseSource: %v", err)
	}
	if _, err := src.WithRef("v1"); err != nil {
		t.Fatalf("WithRef with matching ref: %v", err)
	}
	if _, err := src.WithRef("v2"); err == nil {
		t.Fatal("WithRef with conflicting ref succeeded, want error")
	}
}

func TestWithPathRejectsWhenSubpathPresent(t *testing.T) {
	src, err := parseSource("acme/kits/extensions/features/foo")
	if err != nil {
		t.Fatalf("parseSource: %v", err)
	}
	if _, err := src.WithPath("extensions/features/bar"); err == nil {
		t.Fatal("WithPath over an existing subpath succeeded, want error")
	}
}

func TestRedactRemote(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"https userinfo stripped", "https://user:token@github.com/acme/kits", "https://github.com/acme/kits"},
		{"https bare token stripped", "https://ghp_token@github.com/o/r", "https://github.com/o/r"},
		{"ssh url preserved verbatim", "ssh://git@host/acme/kits", "ssh://git@host/acme/kits"},
		{"scp style preserved verbatim", "git@github.com:acme/kits.git", "git@github.com:acme/kits.git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactRemote(tc.in); got != tc.want {
				t.Errorf("RedactRemote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactRemoteInsideText(t *testing.T) {
	got := RedactRemote("fatal: could not read from https://user:token@github.com/acme/kits")
	if strings.Contains(got, "token") {
		t.Fatalf("RedactRemote left a credential in free text: %q", got)
	}
}
