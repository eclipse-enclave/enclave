// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"enclave/internal/extinstall"
	"enclave/internal/model"
)

func TestRenderExtensionListJSON(t *testing.T) {
	entries := extensionListJSONEntries(
		[]model.Extension{{Name: "foo", DisplayName: "Foo", Description: "A test feature"}},
		map[string]extinstall.Managed{"foo": {
			Name:   "foo",
			Source: "user",
			Origin: &extinstall.Origin{
				SchemaVersion: extinstall.OriginSchemaVersion,
				Remote:        "https://github.com/acme/kits",
				Source:        "acme/kits",
				Ref:           "main",
				RefType:       extinstall.RefTypeBranch,
				Commit:        "a1b2c3d4",
			},
		}},
		func(string) bool { return true },
	)

	var out bytes.Buffer
	if err := renderExtensionListJSON(&out, model.KindFeature, entries); err != nil {
		t.Fatalf("renderExtensionListJSON: %v", err)
	}

	var decoded struct {
		SchemaVersion string `json:"schemaVersion"`
		Kind          string `json:"kind"`
		Extensions    []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			Enabled     bool   `json:"enabled"`
			Managed     bool   `json:"managed"`
			Origin      *struct {
				Commit          string `json:"commit"`
				LocallyModified bool   `json:"locallyModified"`
			} `json:"origin"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if decoded.SchemaVersion != "1" || decoded.Kind != "feature" {
		t.Fatalf("envelope = %+v", decoded)
	}
	if len(decoded.Extensions) != 1 || !decoded.Extensions[0].Managed {
		t.Fatalf("extensions = %+v", decoded.Extensions)
	}
	if decoded.Extensions[0].DisplayName != "Foo" || !decoded.Extensions[0].Enabled {
		t.Errorf("extension = %+v, want displayName Foo and enabled", decoded.Extensions[0])
	}
	if decoded.Extensions[0].Origin == nil || decoded.Extensions[0].Origin.Commit != "a1b2c3d4" {
		t.Fatalf("origin = %+v", decoded.Extensions[0].Origin)
	}
}

func TestRenderExtensionListJSONOmitsOriginWhenUnmanaged(t *testing.T) {
	var out bytes.Buffer
	entries := extensionListJSONEntries(
		[]model.Extension{{Name: "builtin"}},
		map[string]extinstall.Managed{"builtin": {Name: "builtin", Source: "builtin"}},
		func(string) bool { return true },
	)
	if err := renderExtensionListJSON(&out, model.KindTool, entries); err != nil {
		t.Fatalf("renderExtensionListJSON: %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte(`"origin"`)) {
		t.Fatalf("unmanaged entry carries an origin key:\n%s", out.String())
	}
}

func TestRenderExtensionListJSONReportsOriginError(t *testing.T) {
	const problem = `feature "broken": parse .enclave-source.json: unexpected end of JSON input`
	entries := extensionListJSONEntries(
		[]model.Extension{{Name: "broken"}},
		map[string]extinstall.Managed{"broken": {Name: "broken", Source: "user", Problem: problem}},
		func(string) bool { return true },
	)

	var out bytes.Buffer
	if err := renderExtensionListJSON(&out, model.KindFeature, entries); err != nil {
		t.Fatalf("renderExtensionListJSON: %v", err)
	}

	var decoded struct {
		Extensions []struct {
			Name        string          `json:"name"`
			Managed     bool            `json:"managed"`
			Origin      json.RawMessage `json:"origin"`
			OriginError string          `json:"originError"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if len(decoded.Extensions) != 1 {
		t.Fatalf("extensions = %+v", decoded.Extensions)
	}
	got := decoded.Extensions[0]
	if got.Managed {
		t.Error("managed = true, want false for an entry with a provenance error")
	}
	if got.Origin != nil {
		t.Errorf("origin = %s, want absent for an entry with a provenance error", got.Origin)
	}
	if got.OriginError != problem {
		t.Errorf("originError = %q, want %q", got.OriginError, problem)
	}
}

func TestProvenanceSuffix(t *testing.T) {
	managed := extinstall.Managed{
		Name:   "foo",
		Source: "user",
		Origin: &extinstall.Origin{Source: "acme/kits", Remote: "https://github.com/acme/kits", Commit: "a1b2c3d4e5f6"},
	}
	cases := []struct {
		name  string
		entry extinstall.Managed
		want  string
	}{
		{name: "managed", entry: managed, want: " [acme/kits@a1b2c3d]"},
		{
			name:  "managed and edited by hand",
			entry: extinstall.Managed{Name: "foo", Source: "user", Origin: managed.Origin, Modified: true},
			want:  " [acme/kits@a1b2c3d, modified]",
		},
		{
			name:  "no raw source falls back to the redacted remote",
			entry: extinstall.Managed{Name: "foo", Origin: &extinstall.Origin{Remote: "https://github.com/acme/kits", Commit: "a1b2c3d4e5f6"}},
			want:  " [https://github.com/acme/kits@a1b2c3d]",
		},
		{
			name:  "provenance unreadable",
			entry: extinstall.Managed{Name: "broken", Source: "user", Problem: `feature "broken": corrupt sidecar`},
			want:  " [provenance unreadable]",
		},
		{name: "built-in", entry: extinstall.Managed{Name: "builtin", Source: "builtin"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provenanceSuffix(tc.entry); got != tc.want {
				t.Errorf("provenanceSuffix = %q, want %q", got, tc.want)
			}
		})
	}
}

// The add/update/remove envelope carries the same schemaVersion as
// `list --json`, and "results" is always an array.
func TestWriteExtensionResultsJSON(t *testing.T) {
	var out bytes.Buffer
	if err := writeExtensionResultsJSON(&out, model.KindFeature, nil); err != nil {
		t.Fatalf("writeExtensionResultsJSON: %v", err)
	}
	var decoded struct {
		SchemaVersion string                    `json:"schemaVersion"`
		Kind          string                    `json:"kind"`
		Results       []extinstall.ActionResult `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if decoded.SchemaVersion != extensionSchemaVersion || decoded.Kind != "feature" {
		t.Fatalf("envelope = %+v", decoded)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"results": []`)) {
		t.Fatalf("\"results\" must be an array, never null:\n%s", out.String())
	}

	out.Reset()
	results := []extinstall.ActionResult{{Name: "foo", Action: extinstall.ActionInstalled, Commit: "a1b2c3d4", Path: "/tmp/foo"}}
	if err := writeExtensionResultsJSON(&out, model.KindTool, results); err != nil {
		t.Fatalf("writeExtensionResultsJSON: %v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if len(decoded.Results) != 1 || !reflect.DeepEqual(decoded.Results[0], results[0]) {
		t.Fatalf("results = %+v", decoded.Results)
	}
}
