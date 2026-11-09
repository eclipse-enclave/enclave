// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyNativeOwnershipAndResetResidue(t *testing.T) {
	profile := claudeProfile()
	profile.Providers[0].AuthSecrets = profile.Providers[0].AuthSecrets[:1]
	real, views := t.TempDir(), t.TempDir()
	nativeFile(t, real, ".credentials.json", nativeOAuth(accessToken, refreshToken))
	view, err := Project(real, views, "original", profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	sanitized, err := os.ReadFile(filepath.Join(view.Dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, content, link string
		current, other      Bindings
		idle                bool
		want                string
	}{
		{name: "absent", current: view.Bindings, want: "create"},
		{name: "expected link", link: "/home/agent/.enclave-auth/.credentials.json", current: view.Bindings, want: "keep"},
		{name: "foreign link", link: "/home/agent/.elsewhere/.credentials.json", current: view.Bindings},
		{name: "bound sibling", content: string(sanitized), current: view.Bindings, want: "keep"},
		{name: "bound idle", content: string(sanitized), current: view.Bindings, idle: true, want: "link"},
		{name: "raw equal real", content: nativeOAuth(accessToken, refreshToken), current: view.Bindings, idle: true},
		{name: "raw real-shaped after reset", content: nativeOAuth("sk-ant-oat01-"+strings.Repeat("A", 86), "sk-ant-ort01-"+strings.Repeat("B", 86)), idle: true},
		{name: "unbound residue idle", content: string(sanitized), idle: true, want: "link"},
		{name: "unbound while live", content: string(sanitized)},
		{name: "foreign-bound residue", content: string(sanitized), other: view.Bindings, idle: true},
		{name: "undecodable", content: "not json", idle: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.content != "" {
				nativeFile(t, dir, ".credentials.json", tc.content)
			}
			if tc.link != "" {
				if err := os.Symlink(tc.link, filepath.Join(dir, ".credentials.json")); err != nil {
					t.Fatal(err)
				}
			}
			ops, err := ClassifyNative(dir, "/home/agent/.enclave-auth", profile, tc.current, []Bindings{tc.other}, tc.idle)
			if tc.want == "" {
				if err == nil {
					t.Fatal("accepted foreign or raw native path")
				}
				return
			}
			if err != nil || len(ops) != 1 || ops[0].Verb != tc.want {
				t.Fatalf("classification = %+v, %v; want %s", ops, err, tc.want)
			}
			if tc.content != "" {
				data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
				if err != nil || string(data) != tc.content || ops[0].Digest == "" {
					t.Fatal("classification changed native path or lost its fingerprint")
				}
			}
		})
	}
}

func TestClassifyNativeRefusesUnknownKeysInOriginalRegularFile(t *testing.T) {
	real, views := t.TempDir(), t.TempDir()
	profile := claudeProfile()
	profile.Providers[0].AuthSecrets = profile.Providers[0].AuthSecrets[:1]
	nativeFile(t, real, ".credentials.json", nativeOAuth(accessToken, refreshToken))
	view, err := Project(real, views, "original", profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	sanitized, err := os.ReadFile(filepath.Join(view.Dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, nested := range []bool{false, true} {
		var doc map[string]any
		if err := json.Unmarshal(sanitized, &doc); err != nil {
			t.Fatal(err)
		}
		target := doc
		if nested {
			target = doc["claudeAiOauth"].(map[string]any)
		}
		target["unknownCredential"] = "synthetic-unknown-raw-looking-credential"
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		config := t.TempDir()
		nativeFile(t, config, ".credentials.json", string(raw))
		if _, err := ClassifyNative(config, "/home/agent/.enclave-auth", profile, view.Bindings, nil, false); err == nil {
			t.Fatal("classifier kept an unsanitized regular file after stripping unknown keys")
		}
		unchanged, err := os.ReadFile(filepath.Join(config, ".credentials.json"))
		if err != nil || string(unchanged) != string(raw) {
			t.Fatal("classification changed the original file")
		}
	}
}

func TestClassifyNativeRefusesAnInRootSymlinkedParent(t *testing.T) {
	real, views, config := t.TempDir(), t.TempDir(), t.TempDir()
	profile := claudeProfile()
	profile.Providers[0].AuthSecrets = profile.Providers[0].AuthSecrets[:1]
	nativeFile(t, real, ".credentials.json", nativeOAuth(accessToken, refreshToken))
	view, err := Project(real, views, "original", profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	sanitized, err := os.ReadFile(filepath.Join(view.Dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	nativeFile(t, config, "inside/.credentials.json", string(sanitized))
	if err := os.Symlink("inside", filepath.Join(config, "parent")); err != nil {
		t.Fatal(err)
	}
	nested := "parent/.credentials.json"
	profile.Providers[0].AuthSecrets[0].File = nested
	for i := range view.Bindings.Bindings {
		view.Bindings.Bindings[i].File = nested
	}
	if _, err := ClassifyNative(config, "/home/agent/.enclave-auth", profile, view.Bindings, nil, false); err == nil {
		t.Fatal("classifier followed a parent symlink that os.Root confines but does not reject")
	}
}
