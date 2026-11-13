// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"testing"

	"enclave/internal/filter"
	"enclave/internal/filter/spec"
)

// matchSpec shadows a broad allow route with deny routes on request content,
// the way a filter keeps batch requests that reach other files and full text
// searches away from an editing preset.
const matchSpec = `
schemaVersion: "1"
kind: filter
name: match
jsonKeys: protobuf
hosts: [slides.example.com, drive.example.com]
credential:
  static: { env: [MATCH_TOKEN] }
  inject: { header: authorization, format: "Bearer %s" }
  env: [MATCH_TOKEN]
capabilities:
  files.edit:
    constraints:
      resources: { kind: value, var: fileId }
  files.list: {}
routes:
  - op: slides.batchUpdate.crossFile
    action: deny
    reason: requests that read other files cannot be checked
    method: POST
    host: slides.example.com
    path: /v1/presentations/{fileId}:batchUpdate
    jsonPresent: ["$..createSheetsChart", "$.requests[*].replaceAllShapesWithSheetsChart"]
  - op: slides.batchUpdate.secret
    action: deny
    method: POST
    host: slides.example.com
    path: /v1/presentations/{fileId}:batchUpdate
    jsonMatch: { "$..text": "(?i)password" }
  - op: slides.batchUpdate.big
    action: deny
    method: POST
    host: slides.example.com
    path: /v1/presentations/{fileId}:batchUpdate
    jsonMatch: { "$..magnitude": "^[0-9]{3,}$", "$..bold": "^true$" }
  - op: slides.batchUpdate
    capability: files.edit
    method: POST
    host: slides.example.com
    path: /v1/presentations/{fileId}:batchUpdate
  - op: drive.files.list.fullText
    action: deny
    reason: full text search reads every file
    method: GET
    host: drive.example.com
    path: /files
    queryMatch: { q: "fullText" }
  - op: drive.files.list
    capability: files.list
    method: GET
    host: drive.example.com
    path: /files
`

func newMatchHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessForPolicy(t, parseSpecPolicy(t, matchSpec, &spec.Preset{Capabilities: map[string]map[string]filter.ConstraintValue{
		"files.edit": {"resources": {Values: []string{"PRES1"}}},
		"files.list": {},
	}}))
}

// Regression for a batch request that embeds a chart of a spreadsheet
// outside the preset into an allowed presentation: a deny route on request
// content must catch it at any depth.
func TestEvaluateJSONPresentDeniesNestedRequests(t *testing.T) {
	h := newMatchHarness(t)
	batch := "https://slides.example.com/v1/presentations/PRES1:batchUpdate"
	for name, body := range map[string]string{
		"chart":           `{"requests":[{"createSheetsChart":{"spreadsheetId":"OUTSIDE","chartId":1,"linkingMode":"LINKED"}}]}`,
		"chart later":     `{"requests":[{"insertText":{"text":"a"}},{"createSheetsChart":{"spreadsheetId":"OUTSIDE"}}]}`,
		"null counts":     `{"requests":[{"createSheetsChart":null}]}`,
		"empty object":    `{"requests":[{"createSheetsChart":{}}]}`,
		"snake_case key":  `{"requests":[{"create_sheets_chart":{"spreadsheet_id":"OUTSIDE"}}]}`,
		"second path":     `{"requests":[{"replaceAllShapesWithSheetsChart":{"spreadsheetId":"OUTSIDE"}}]}`,
		"deeply embedded": `{"x":{"y":[{"z":{"createSheetsChart":1}}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("POST", batch, body)), ReasonDenyRoute,
				"slides.batchUpdate.crossFile is never allowed: requests that read other files cannot be checked")
		})
	}
	d := h.eval(t, newRequest("POST", batch, `{"requests":[{"insertText":{"objectId":"o","text":"hello"}}]}`))
	mustAllow(t, d)
	if d.Op != "slides.batchUpdate" {
		t.Fatalf("op = %s, want slides.batchUpdate", d.Op)
	}
	// An empty body has nothing present, so it reaches the allow route.
	mustAllow(t, h.eval(t, newRequest("POST", batch, "")))
	// The constraint of the allow route still applies.
	mustDeny(t, h.eval(t, newRequest("POST", "https://slides.example.com/v1/presentations/OTHER:batchUpdate", `{"requests":[]}`)),
		ReasonConstraint, `"OTHER"`)
}

// A body the deny route cannot read never falls through to the allow route.
func TestEvaluateUnreadableBodyNeverPassesADenyRoute(t *testing.T) {
	h := newMatchHarness(t)
	batch := "https://slides.example.com/v1/presentations/PRES1:batchUpdate"
	for name, tc := range map[string]struct{ body, contentType, want string }{
		"duplicate key hides the chart": {`{"requests":[{"insertText":{}}],"requests":[{"createSheetsChart":{}}]}`, "application/json", "repeats a key"},
		"snake and camel collide":       {`{"requests":[],"requests_":[]}`, "application/json", ""},
		"declared as text":              {`requests`, "text/plain", "is not declared as JSON"},
		"JSPB":                          {`[[["S1"]]]`, "application/json+protobuf", "is not declared as JSON"},
		"array as JSON":                 {`[{"createSheetsChart":{}}]`, "application/json", "is not a JSON object"},
		"scalar":                        {`"createSheetsChart"`, "application/json", "is not a JSON object"},
		"trailing data":                 {`{"requests":[]} {"requests":[{"createSheetsChart":{}}]}`, "application/json", "is not valid JSON"},
		"wrong shape":                   {`{"requests":{"replaceAllShapesWithSheetsChart":{}}}`, "application/json", "does not have the shape"},
	} {
		t.Run(name, func(t *testing.T) {
			d := h.eval(t, newRequest("POST", batch, tc.body, withContentType(tc.contentType)))
			if d.Denial == nil {
				t.Fatalf("allowed as %s", d.Op)
			}
			if tc.want != "" {
				mustDeny(t, d, ReasonBody, tc.want)
			}
		})
	}
}

func TestEvaluateJSONMatch(t *testing.T) {
	h := newMatchHarness(t)
	batch := "https://slides.example.com/v1/presentations/PRES1:batchUpdate"
	tests := []struct {
		name string
		body string
		op   string
	}{
		{"unanchored, case-insensitive by the expression", `{"requests":[{"insertText":{"text":"the PassWord is x"}}]}`, "slides.batchUpdate.secret"},
		{"no match", `{"requests":[{"insertText":{"text":"hello"}}]}`, "slides.batchUpdate"},
		{"numbers and booleans in their JSON spelling", `{"a":{"magnitude":1200,"bold":true}}`, "slides.batchUpdate.big"},
		{"every entry must match", `{"a":{"magnitude":1200,"bold":false}}`, "slides.batchUpdate"},
		{"objects are not scalars", `{"a":{"magnitude":{"v":1200},"bold":true}}`, "slides.batchUpdate"},
		{"any value may match", `{"a":[{"magnitude":5},{"magnitude":500}],"bold":true}`, "slides.batchUpdate.big"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := h.eval(t, newRequest("POST", batch, tt.body))
			if d.Op != tt.op {
				t.Fatalf("op = %s (%v), want %s", d.Op, d.Denial, tt.op)
			}
		})
	}
}

func TestEvaluateQueryMatch(t *testing.T) {
	h := newMatchHarness(t)
	files := "https://drive.example.com/files"
	for name, query := range map[string]string{
		"full text":            "?q=fullText+contains+%27secret%27",
		"any occurrence":       "?q=name%3D%27a%27&q=fullText+contains+%27b%27",
		"unanchored":           "?q=name%3D%27a%27+or+fullText+contains+%27b%27",
		"percent-decoded data": "?q=%66ullText+contains+%27b%27",
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("GET", files+query, "")), ReasonDenyRoute, "drive.files.list.fullText is never allowed")
		})
	}
	for _, query := range []string{"", "?q=name%3D%27a%27", "?pageSize=10"} {
		d := h.eval(t, newRequest("GET", files+query, ""))
		mustAllow(t, d)
		if d.Op != "drive.files.list" {
			t.Fatalf("%q: op = %s", query, d.Op)
		}
	}
	// A spelling a server may read as q is ambiguous for the deny route.
	mustDeny(t, h.eval(t, newRequest("GET", files+"?Q=fullText", "")), ReasonAmbiguous, `query parameter "Q" is ambiguous with q`)
}
