// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"net/http"
	"testing"

	"enclave/internal/filter"
	"enclave/internal/filter/spec"
)

// I5: JSON conditions read only bodies a server reads the same way. Google
// parses application/json+protobuf as JSPB, an array encoding in which no
// key a deny route looks for exists.
func TestEvaluateJSONConditionsNeedAJSONObject(t *testing.T) {
	h := newMatchHarness(t)
	batch := "https://slides.example.com/v1/presentations/PRES1:batchUpdate"
	for name, tc := range map[string]struct{ body, contentType, want string }{
		"JSPB":             {`[[[null,["S1"]]]]`, "application/json+protobuf", "is not declared as JSON"},
		"protobuf":         {"\x0a\x02S1", "application/x-protobuf", "is not declared as JSON"},
		"text":             {`{"requests":[]}`, "text/plain", "is not declared as JSON"},
		"no media subtype": {`{"requests":[]}`, "application/jsonx", "is not declared as JSON"},
		"latin1":           {`{"requests":[]}`, "application/json; charset=ISO-8859-1", "is not declared as JSON"},
		"array":            {`[{"requests":[]}]`, "application/json", "is not a JSON object"},
		"null":             {`null`, "application/json", "is not a JSON object"},
		"number":           {`1`, "application/json", "is not a JSON object"},
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("POST", batch, tc.body, withContentType(tc.contentType))), ReasonBody, tc.want)
		})
	}
	for _, contentType := range []string{"application/json", "application/json; charset=UTF-8", "Application/JSON; charset=utf8", "application/vnd.example+json"} {
		t.Run(contentType, func(t *testing.T) {
			mustAllow(t, h.eval(t, newRequest("POST", batch, `{"requests":[]}`, withContentType(contentType))))
		})
	}
	// Without a body there is nothing to declare.
	mustAllow(t, h.eval(t, newRequest("POST", batch, "")))
}

// I5: a key a server may read as the one a condition checks denies the
// request, wherever the condition looks for it.
func TestEvaluateJSONKeysSpelledLikeACheckedKey(t *testing.T) {
	h := newMatchHarness(t)
	batch := "https://slides.example.com/v1/presentations/PRES1:batchUpdate"
	for name, body := range map[string]string{
		"pascal case":       `{"requests":[{"CreateSheetsChart":{"spreadsheetId":"OUTSIDE"}}]}`,
		"upper case":        `{"requests":[{"CREATESHEETSCHART":{}}]}`,
		"odd underscores":   `{"requests":[{"create_sheetschart":{}}]}`,
		"in the shape path": `{"Requests":[{"replaceAllShapesWithSheetsChart":{}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("POST", batch, body)), ReasonAmbiguous, "is ambiguous with")
		})
	}
	// The protobuf spelling is renamed and caught by the deny route.
	mustDeny(t, h.eval(t, newRequest("POST", batch, `{"requests":[{"create_sheets_chart":{}}]}`)), ReasonDenyRoute, "never allowed")

	f := newHarness(t, "edit-all", nil)
	drive := "https://www.googleapis.com/drive/v3/files/F1"
	d := mustDeny(t, f.eval(t, newRequest("PATCH", drive, `{"Trashed":true}`)), ReasonAmbiguous, `"Trashed" is ambiguous with "trashed"`)
	if d.Code != http.StatusForbidden {
		t.Fatalf("code = %d", d.Code)
	}
	// A key spelled alike in an object the condition does not read passes.
	mustAllow(t, f.eval(t, newRequest("PATCH", drive, `{"name":"x","properties":{"Trashed":"yes"}}`)))
}

const allowedKeysSpec = `
schemaVersion: "1"
kind: filter
name: keys
jsonKeys: protobuf
hosts: [docs.example.com]
credential:
  static: { env: [KEYS_TOKEN] }
  inject: { header: authorization, format: "Bearer %s" }
  env: [KEYS_TOKEN]
capabilities:
  files.edit: {}
routes:
  - op: docs.batchUpdate
    capability: files.edit
    method: POST
    host: docs.example.com
    path: /v1/documents/{documentId}:batchUpdate
    jsonAllowedKeys:
      "$": [requests, write_control]
      "$.requests[*]": [insertText, delete_content_range]
      "$.writeControl": []
  - op: docs.batchUpdate.unsupported
    action: deny
    reason: only text edits are allowed
    method: POST
    host: docs.example.com
    path: /v1/documents/{documentId}:batchUpdate
`

// jsonAllowedKeys turns a deny list into an allow list: a request kind the
// filter does not list, including one the API adds later, matches no allow
// route.
func TestEvaluateJSONAllowedKeys(t *testing.T) {
	h := newHarnessForPolicy(t, parseSpecPolicy(t, allowedKeysSpec, &spec.Preset{Capabilities: map[string]map[string]filter.ConstraintValue{"files.edit": {}}}))
	batch := "https://docs.example.com/v1/documents/D1:batchUpdate"
	for name, body := range map[string]string{
		"listed":           `{"requests":[{"insertText":{"text":"a"}}]}`,
		"protobuf names":   `{"requests":[{"insert_text":{}},{"deleteContentRange":{}}],"write_control":{}}`,
		"nested unchecked": `{"requests":[{"insertText":{"anything":{"goes":1}}}]}`,
		"null request":     `{"requests":[null]}`,
		"no requests":      `{}`,
		"empty body":       ``,
	} {
		t.Run(name, func(t *testing.T) {
			d := h.eval(t, newRequest("POST", batch, body))
			mustAllow(t, d)
			if d.Op != "docs.batchUpdate" {
				t.Fatalf("op = %s", d.Op)
			}
		})
	}
	for name, body := range map[string]string{
		"unlisted request":     `{"requests":[{"insertInlineImage":{"uri":"https://evil.example"}}]}`,
		"unlisted beside":      `{"requests":[{"insertText":{},"insertInlineImage":{}}]}`,
		"unlisted top level":   `{"requests":[],"tabsCriteria":{}}`,
		"other spelling":       `{"requests":[{"InsertText":{}}]}`,
		"empty object listed":  `{"writeControl":{"requiredRevisionId":"r"}}`,
		"unlisted after valid": `{"requests":[{"insertText":{}},{"createFooter":{}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("POST", batch, body)), ReasonDenyRoute, "only text edits are allowed")
		})
	}
	for name, body := range map[string]string{
		"request not an object": `{"requests":["insertText"]}`,
		"requests not a list":   `{"requests":{"insertText":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("POST", batch, body)), ReasonBody, "")
		})
	}
}

// A json step that reads a multipart part needs the part to declare JSON,
// as route conditions do.
func TestEvaluateJSONBindOnMultipartPartNeedsJSON(t *testing.T) {
	h := newHarness(t, "folder-edit", nil)
	upload := "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart"
	ct := withContentType("multipart/related; boundary=xyz")
	media := [2]string{"Content-Type: text/plain", "file content"}
	ok := multipartBody("xyz", [2]string{"Content-Type: application/json; charset=UTF-8", `{"parents":["FOLDER1"]}`}, media)
	mustAllow(t, h.eval(t, newRequest("POST", upload, ok, ct)))
	jspb := multipartBody("xyz", [2]string{"Content-Type: application/json+protobuf", `{"parents":["FOLDER1"]}`}, media)
	mustDeny(t, h.eval(t, newRequest("POST", upload, jspb, ct)), ReasonBind, "not declared as JSON")
}
