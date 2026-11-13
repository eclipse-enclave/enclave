// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsAmbiguousDocuments(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{"duplicate key", `{"trashed":false,"trashed":true}`, "duplicate key"},
		{"nested duplicate key", `{"a":{"b":1,"b":2}}`, "duplicate key"},
		{"trailing data", `{"a":1} {"a":2}`, "trailing data"},
		{"empty", ``, "empty document"},
		{"syntax", `{"a":}`, "invalid JSON"},
		{"too deep", strings.Repeat("[", maxJSONDepth+2) + strings.Repeat("]", maxJSONDepth+2), "nested deeper"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeJSON([]byte(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeJSON error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseJSONPathRejects(t *testing.T) {
	for _, path := range []string{"", "messages", "$.", "$..", "$...a", "$..[0]", "$[", "$[x]", "$[-1]", "$[01]", "$.a b", "$a", "$." + strings.Repeat("a", maxJSONPathLength)} {
		t.Run(path, func(t *testing.T) {
			if _, err := ParseJSONPath(path); err == nil {
				t.Fatalf("ParseJSONPath(%q) error = nil", path)
			}
		})
	}
}

func TestJSONPathSelectStrings(t *testing.T) {
	doc := `{"messages":[{"id":"m1"},{"id":"m2"},{"other":1}],"n":3.50,"ok":true,"none":null,
		"parents":["p1"],"add_parents":"p2","nested":{"list":[[1,2],[3]]},"obj":{"x":1}}`
	tests := []struct {
		path    string
		want    []string
		wantErr bool
	}{
		{"$.messages[*].id", []string{"m1", "m2"}, false},
		{"$.messages[1].id", []string{"m2"}, false},
		{"$.messages[9].id", nil, false},
		{"$.n", []string{"3.50"}, false},
		{"$.ok", []string{"true"}, false},
		{"$.none", nil, false},
		{"$.missing", nil, false},
		{"$.none.deeper", nil, false},
		{"$.parents[*]", []string{"p1"}, false},
		{"$.add_parents", []string{"p2"}, false},
		// Keys that fold like a name the path reads are ambiguous.
		{"$.addParents", nil, true},
		{"$.MESSAGES[0].ID", nil, true},
		{"$.nested.list[*][*]", []string{"1", "2", "3"}, false},
		{"$.obj", nil, true},
		{"$.messages", nil, true},
		{"$.ok.x", nil, true},
		{"$.obj[0]", nil, true},
	}
	parsed, err := DecodeJSON([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			p, err := ParseJSONPath(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.SelectStrings(parsed)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("SelectStrings = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SelectStrings = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJSONPathRecursiveDescent(t *testing.T) {
	doc, err := DecodeJSON([]byte(`{"requests":[{"insertText":{"text":"a"}},{"createSheetsChart":{"spreadsheetId":"S1"}},
		{"nested":{"deeper":[{"createSheetsChart":null}]}}],"createSheetsChart":"top","scalar":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseJSONPath("$..createSheetsChart")
	if err != nil {
		t.Fatal(err)
	}
	present, err := p.SelectPresent(doc)
	if err != nil || len(present) != 3 {
		t.Fatalf("SelectPresent = %v, %v, want the top value, the object, and null", present, err)
	}
	selected, err := p.Select(doc)
	if err != nil || len(selected) != 2 {
		t.Fatalf("Select = %v, %v, want null dropped", selected, err)
	}
	ids, err := ParseJSONPath("$.requests..spreadsheetId")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ids.SelectStrings(doc)
	if err != nil || !reflect.DeepEqual(got, []string{"S1"}) {
		t.Fatalf("SelectStrings = %v, %v, want [S1]", got, err)
	}
	// Descent never fails on the shape, also below a scalar.
	scalar, _ := ParseJSONPath("$.scalar..x")
	if got, err := scalar.Select(doc); err != nil || len(got) != 0 {
		t.Fatalf("descent below a scalar = %v, %v", got, err)
	}
	snake, _ := ParseJSONPath("$..spreadsheet_id")
	normalized, err := DecodeJSONKeys([]byte(`{"a":{"spreadsheet_id":"S2"}}`), JSONKeysProtobuf)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := snake.WithProtobufNames().SelectStrings(normalized); err != nil || !reflect.DeepEqual(got, []string{"S2"}) {
		t.Fatalf("protobuf descent = %v, %v, want [S2]", got, err)
	}
}

func TestJSONPathSelectPresentKeepsNull(t *testing.T) {
	doc, _ := DecodeJSON([]byte(`{"a":null,"b":{"c":null},"list":[null,1]}`))
	for path, want := range map[string]int{"$.a": 1, "$.b.c": 1, "$.a.x": 0, "$.list[*]": 2, "$.missing": 0, "$.b": 1} {
		p, _ := ParseJSONPath(path)
		got, err := p.SelectPresent(doc)
		if err != nil || len(got) != want {
			t.Errorf("SelectPresent(%s) = %v, %v, want %d values", path, got, err, want)
		}
	}
	root, _ := ParseJSONPath("$.a")
	if got, err := root.SelectPresent(nil); err != nil || len(got) != 0 {
		t.Fatalf("SelectPresent on an absent document = %v, %v", got, err)
	}
}

func TestDecodeJSONKeysProtobuf(t *testing.T) {
	doc, err := DecodeJSONKeys([]byte(`{"add_parents":"a","nested":{"file_id":"f"},"list":[{"mime_type":"x"}],"keep":"k"}`), JSONKeysProtobuf)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"$.addParents": "a", "$.add_parents": "a", "$.nested.fileId": "f", "$.list[0].mimeType": "x", "$.keep": "k",
	} {
		p, _ := ParseJSONPath(path)
		got, err := p.WithProtobufNames().SelectStrings(doc)
		if err != nil || !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("%s = %v, %v, want %q", path, got, err, want)
		}
	}
	for _, body := range []string{
		`{"addParents":"a","add_parents":"b"}`,
		`{"x":[{"file_id":1,"fileId":2}]}`,
	} {
		if _, err := DecodeJSONKeys([]byte(body), JSONKeysProtobuf); err == nil || !strings.Contains(err.Error(), "both mean") {
			t.Errorf("DecodeJSONKeys(%s) = %v, want collision error", body, err)
		}
	}
	if _, err := DecodeJSONKeys([]byte(`{"addParents":"a","add_parents":"b"}`), ""); err != nil {
		t.Errorf("exact mode rejected distinct keys: %v", err)
	}
	if got := ProtobufJSONName("upload_type"); got != "uploadType" {
		t.Errorf("ProtobufJSONName = %q", got)
	}
}

func TestJSONScalarEqual(t *testing.T) {
	tests := []struct {
		a, b any
		want bool
	}{
		{"x", "x", true},
		{"x", "y", false},
		{true, true, true},
		{false, "false", false},
		{json.Number("1"), json.Number("1.0"), true},
		{json.Number("1"), json.Number("2"), false},
		{json.Number("1"), "1", false},
		{nil, nil, false},
		{map[string]any{}, map[string]any{}, false},
	}
	for _, tt := range tests {
		if got := JSONScalarEqual(tt.a, tt.b); got != tt.want {
			t.Errorf("JSONScalarEqual(%#v, %#v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestFoldParam(t *testing.T) {
	for _, pair := range [][2]string{
		{"uploadType", "upload_type"}, {"addParents", "ADD_PARENTS"}, {"access_token", "accessToken"},
		{"format", "Format"}, {"format", "for_mat"}, {"key", "\u212Aey"}, {"spaces", "\u017Fpaces"},
		{"format", "format "}, {"format", "\tfor\x00mat"},
	} {
		if FoldParam(pair[0]) != FoldParam(pair[1]) {
			t.Errorf("FoldParam(%q) != FoldParam(%q)", pair[0], pair[1])
		}
	}
	if FoldParam("format") == FoldParam("fields") || FoldParam("alt") == FoldParam("$alt") {
		t.Fatal("distinct names fold together")
	}
}

func TestProtobufParamAlias(t *testing.T) {
	tests := []struct {
		name, alias string
		ok          bool
	}{
		{"addParents", "add_parents", true},
		{"includeItemsFromAllDrives", "include_items_from_all_drives", true},
		{"requestMask.includeField", "request_mask.include_field", true},
		{"v2Field", "v2_field", true},
		{"format", "format", false},
		{"upload_type", "upload_type", false},
		{"$fields", "$fields", false},
		{"Format", "Format", false},
	}
	for _, tt := range tests {
		alias, ok := ProtobufParamAlias(tt.name)
		if alias != tt.alias || ok != tt.ok {
			t.Errorf("ProtobufParamAlias(%q) = %q, %v, want %q, %v", tt.name, alias, ok, tt.alias, tt.ok)
		}
		if ok && ProtobufJSONName(alias) != tt.name {
			t.Errorf("ProtobufJSONName(%q) = %q, want %q", alias, ProtobufJSONName(alias), tt.name)
		}
	}
	if got := QueryParamNames("addParents", JSONKeysProtobuf); !reflect.DeepEqual(got, []string{"addParents", "add_parents"}) {
		t.Errorf("QueryParamNames protobuf = %q", got)
	}
	if got := QueryParamNames("addParents", ""); !reflect.DeepEqual(got, []string{"addParents"}) {
		t.Errorf("QueryParamNames exact = %q", got)
	}
}

// A key that folds like a name the path reads is ambiguous where the path
// reads it: at a name step, and in every object a recursive descent passes.
func TestJSONPathAmbiguousKeys(t *testing.T) {
	tests := []struct {
		name, path, doc string
		ambiguous       string
	}{
		{"case", "$.trashed", `{"Trashed":true}`, "Trashed"},
		{"underscore", "$.addParents", `{"add_parents":"x"}`, "add_parents"},
		{"whitespace", "$.trashed", `{"trashed ":true}`, "trashed "},
		{"exact and folded", "$.trashed", `{"trashed":false,"TRASHED":true}`, "TRASHED"},
		{"smallest of several", "$.trashed", `{"Trashed":1,"TRASHED":2}`, "TRASHED"},
		{"later step", "$.file.mimeType", `{"file":{"MimeType":"x"}}`, "MimeType"},
		{"descent nested", "$..insertInlineImage", `{"requests":[{"InsertInlineImage":{}}]}`, "InsertInlineImage"},
		{"descent root", "$..createChart", `{"create_chart":{}}`, "create_chart"},
		{"unicode fold", "$.ks", "{\"\u212as\":1}", "\u212as"},
		{"exact only", "$.trashed", `{"trashed":true,"name":"Trashed"}`, ""},
		{"other object", "$.trashed", `{"trashed":true,"properties":{"Trashed":"x"}}`, ""},
		{"different word", "$.value", `{"values":[],"userEnteredValue":{}}`, ""},
		{"index steps", "$.list[0]", `{"list":[{"LIST":1}]}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := DecodeJSON([]byte(tt.doc))
			if err != nil {
				t.Fatal(err)
			}
			p, err := ParseJSONPath(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.SelectPresent(doc)
			var ambiguous *AmbiguousKeyError
			if tt.ambiguous == "" {
				if err != nil {
					t.Fatalf("SelectPresent = %v", err)
				}
				return
			}
			if !errors.As(err, &ambiguous) || ambiguous.Key != tt.ambiguous {
				t.Fatalf("SelectPresent error = %v, want key %q", err, tt.ambiguous)
			}
		})
	}
}

// Protobuf renaming keeps the fold of each name step, so a key renamed to the
// exact name passes and one in another spelling does not.
func TestJSONPathAmbiguousKeysProtobuf(t *testing.T) {
	p, err := ParseJSONPath("$.writers_can_share")
	if err != nil {
		t.Fatal(err)
	}
	p = p.WithProtobufNames()
	for body, ambiguous := range map[string]bool{
		`{"writers_can_share":true}`: false,
		`{"writersCanShare":true}`:   false,
		`{"WritersCanShare":true}`:   true,
		`{"writers_Can_Share":true}`: false,
	} {
		doc, err := DecodeJSONKeys([]byte(body), JSONKeysProtobuf)
		if err != nil {
			t.Fatal(err)
		}
		nodes, err := p.Select(doc)
		var keyErr *AmbiguousKeyError
		if errors.As(err, &keyErr) != ambiguous {
			t.Fatalf("%s: Select = %v, %v", body, nodes, err)
		}
		if !ambiguous && len(nodes) != 1 {
			t.Fatalf("%s: Select = %v", body, nodes)
		}
	}
}

func TestAmbiguousKeyErrorBoundsTheKey(t *testing.T) {
	err := &AmbiguousKeyError{Key: strings.Repeat("T", 500), Name: "t"}
	if len(err.Error()) > 200 {
		t.Fatalf("message length %d", len(err.Error()))
	}
}
