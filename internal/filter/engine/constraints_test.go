// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"fmt"
	"strings"
	"testing"

	"enclave/internal/filter"
	"enclave/internal/filter/spec"
)

const sendURL = gmail + "/gmail/v1/users/me/messages/send"

func TestEmailConstraint(t *testing.T) {
	h := newHarness(t, "send-eclipsesource", nil)
	allowed := []string{
		"To: alice@eclipsesource.com",
		"To: Alice <ALICE@EclipseSource.com>\r\nCc: bob@eclipsesource.com",
	}
	for _, headers := range allowed {
		d := h.eval(t, newRequest("POST", sendURL, sendBody(headers), withContentType("application/json")))
		mustAllow(t, d)
		d.Finish(200)
	}
	tests := []struct {
		name    string
		body    string
		reason  string
		message string
	}{
		{"foreign to", sendBody("To: x@evil.com"), ReasonConstraint, `recipients "x@evil.com" is not allowed by preset send-eclipsesource`},
		{"hidden bcc", sendBody("To: alice@eclipsesource.com\r\nBcc: x@evil.com"), ReasonConstraint, `"x@evil.com"`},
		{"resent", sendBody("To: alice@eclipsesource.com\r\nResent-To: x@evil.com"), ReasonConstraint, `"x@evil.com"`},
		{"lookalike domain", sendBody("To: a@eclipsesource.com.evil.com"), ReasonConstraint, "not allowed"},
		{"quoted local part", sendBody(`To: "a@eclipsesource.com"@evil.com`), ReasonConstraint, "not allowed"},
		{"no recipients", sendBody("Subject: hi"), ReasonConstraint, "recipients is empty"},
		{"raw missing", `{"threadId":"t"}`, ReasonConstraint, "recipients is empty"},
		{"invalid address", sendBody("To: not an address"), ReasonBind, "cannot read recipients"},
		{"raw not base64", `{"raw":"***"}`, ReasonBind, "cannot read recipients"},
		{"body not json", `raw=abc`, ReasonBind, "cannot read recipients"},
		{"duplicate raw", `{"raw":"` + rawMessage("To: a@eclipsesource.com") + `","raw":"` + rawMessage("To: x@evil.com") + `"}`, ReasonBind, "cannot read recipients"},
		{"obsolete bcc", sendBody("To: alice@eclipsesource.com\r\nBcc : x@evil.com"), ReasonConstraint, `"x@evil.com"`},
		{"obsolete bcc tab", sendBody("To: alice@eclipsesource.com\r\nBcc\t: x@evil.com"), ReasonConstraint, `"x@evil.com"`},
		{"obsolete resent-to", sendBody("To: alice@eclipsesource.com\r\nResent-To : x@evil.com"), ReasonConstraint, `"x@evil.com"`},
		{"folded bcc", sendBody("To: alice@eclipsesource.com\r\nBcc: bob@eclipsesource.com,\r\n x@evil.com"), ReasonConstraint, `"x@evil.com"`},
		{"group", sendBody("To: team: alice@eclipsesource.com, x@evil.com;"), ReasonConstraint, `"x@evil.com"`},
		{"idn domain", sendBody("To: a@\u00e9clipsesource.com"), ReasonConstraint, "not allowed"},
		{"space in header name", sendBody("To: alice@eclipsesource.com\r\nB cc: x@evil.com"), ReasonBind, "cannot read recipients"},
		{"whitespace-only fold", sendBody("To: alice@eclipsesource.com\r\n \r\nBcc: x@evil.com"), ReasonBind, "cannot read recipients"},
		{"mixed line endings", sendBody("To: alice@eclipsesource.com\nBcc: x@evil.com\r"), ReasonBind, "cannot read recipients"},
		{"unterminated group", sendBody("To: team: alice@eclipsesource.com"), ReasonBind, "cannot read recipients"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("POST", sendURL, tt.body, withContentType("application/json"))), tt.reason, tt.message)
		})
	}
}

func TestValueConstraint(t *testing.T) {
	h := newHarness(t, "folder-edit", nil)
	modify := gmail + "/gmail/v1/users/me/messages/m1/modify"
	for _, body := range []string{`{"addLabelIds":["STARRED"]}`, `{"add_label_ids":["STARRED","IMPORTANT"]}`} {
		mustAllow(t, h.eval(t, newRequest("POST", modify, body, withContentType("application/json"))))
	}
	mustDeny(t, h.eval(t, newRequest("POST", modify, `{"addLabelIds":["STARRED","TRASH"]}`)), ReasonConstraint, `labels "TRASH" is not allowed`)
	mustDeny(t, h.eval(t, newRequest("POST", modify, `{"removeLabelIds":["INBOX"]}`)), ReasonConstraint, "labels is empty")

	any := newHarness(t, "edit-all", nil)
	mustAllow(t, any.eval(t, newRequest("POST", modify, `{"removeLabelIds":["INBOX"]}`)))
}

func TestResourceConstraintWithResolver(t *testing.T) {
	tests := []struct {
		file    string
		allowed bool
		reason  string
		message string
	}{
		{"DOC1", true, "", ""},
		{"FOLDER1", true, "", ""},
		{"FILE_IN", true, "", ""},
		{"FILE_DEEP", true, "", ""},
		{"FILE_OUT", false, ReasonConstraint, `resources "FILE_OUT" is not inside a location allowed by preset folder-edit`},
		{"FILE_ERR", false, "resolver:unreachable", `resources "FILE_ERR" could not be resolved (unreachable)`},
		{"FILE_UP", false, ReasonConstraint, "is not inside a location"},
		{"CYCLE_A", false, ReasonConstraint, "is not inside a location"},
		{"UNKNOWN", false, "resolver:http-404", "could not be resolved (http-404)"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			h := newHarness(t, "folder-edit", nil)
			d := h.eval(t, newRequest("POST", "https://docs.googleapis.com/v1/documents/"+tt.file+":batchUpdate", "{}"))
			if tt.allowed {
				mustAllow(t, d)
				return
			}
			mustDeny(t, d, tt.reason, tt.message)
		})
	}
}

func TestResolverURLUsesGovernedHostAndEscaping(t *testing.T) {
	h := newHarness(t, "folder-edit", nil)
	mustAllow(t, h.eval(t, newRequest("POST", "https://docs.googleapis.com/v1/documents/FILE_DEEP:batchUpdate", "{}")))
	want := []string{
		"https://www.googleapis.com/drive/v3/files/FILE_DEEP?fields=parents&supportsAllDrives=true",
		"https://www.googleapis.com/drive/v3/files/SUB?fields=parents&supportsAllDrives=true",
	}
	if fmt.Sprint(h.fetcher.urls) != fmt.Sprint(want) {
		t.Fatalf("lookups = %v", h.fetcher.urls)
	}
}

func TestResolverWalkHonorsMaxDepth(t *testing.T) {
	h := newHarness(t, "folder-edit", nil)
	// Build a chain deeper than the fixture's maxDepth of 8.
	prev := "FOLDER1"
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("L%d", i)
		h.fetcher.parents[id] = []string{prev}
		prev = id
	}
	mustAllow(t, h.eval(t, newRequest("POST", "https://docs.googleapis.com/v1/documents/L7:batchUpdate", "{}")))
	mustDeny(t, h.eval(t, newRequest("POST", "https://docs.googleapis.com/v1/documents/L8:batchUpdate", "{}")), ReasonConstraint, "not inside")
}

func TestUploadParentsFromMultipartMetadata(t *testing.T) {
	upload := "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart"
	ct := withContentType("multipart/related; boundary=xyz")
	part := func(metadata string) string {
		return multipartBody("xyz", [2]string{"Content-Type: application/json; charset=UTF-8", metadata}, [2]string{"Content-Type: text/plain", "file content"})
	}
	tests := []struct {
		name    string
		body    string
		reason  string
		message string
	}{
		{"inside folder", part(`{"name":"a.txt","parents":["FOLDER1"]}`), "", ""},
		{"inside subfolder", part(`{"name":"a.txt","parents":["SUB"]}`), "", ""},
		{"snake case parents", part(`{"name":"a.txt","parents":["SUB"],"mime_type":"text/plain"}`), "", ""},
		{"outside", part(`{"name":"a.txt","parents":["OTHER"]}`), ReasonConstraint, `parents "OTHER" is not inside`},
		{"one parent outside", part(`{"parents":["FOLDER1","OTHER"]}`), ReasonConstraint, `"OTHER"`},
		{"no parents means root", part(`{"name":"a.txt"}`), ReasonConstraint, "parents is empty"},
		{"invalid resolver id", part(`{"parents":["../x"]}`), "resolver:invalid-id", "could not be resolved"},
		{"metadata not json", multipartBody("xyz", [2]string{"Content-Type: application/json", "nope"}, [2]string{"Content-Type: text/plain", "x"}), ReasonBind, "cannot read parents"},
		{"three parts", multipartBody("xyz", [2]string{"Content-Type: application/json", `{"parents":["FOLDER1"]}`}, [2]string{"", "a"}, [2]string{"", "b"}),
			ReasonBody, "must have 2 parts (got 3)"},
		{"quoted printable metadata", multipartBody("xyz", [2]string{"Content-Type: application/json\r\nContent-Transfer-Encoding: quoted-printable", `{"parents":["FOLDER1"]}`}, [2]string{"", "a"}),
			ReasonBody, "transfer encoding"},
		// A Content-ID could name the second part as the root that a server
		// reads as metadata, while the bind checks the first.
		{"content id", multipartBody("xyz", [2]string{"Content-Type: application/json", `{"parents":["FOLDER1"]}`}, [2]string{"Content-Type: application/json\r\nContent-ID: <root>", `{"parents":["OTHER"]}`}),
			ReasonBody, "other than Content-Type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, "folder-edit", nil)
			d := h.eval(t, newRequest("POST", upload, tt.body, ct))
			if tt.reason == "" {
				mustAllow(t, d)
				return
			}
			mustDeny(t, d, tt.reason, tt.message)
		})
	}
	h := newHarness(t, "folder-edit", nil)
	mustDeny(t, h.eval(t, newRequest("POST", upload, `{"parents":["FOLDER1"]}`, withContentType("application/json"))), ReasonBody, "not multipart")
	for _, start := range []string{`start="<root>"`, "start-info=x", "start*0=x"} {
		body := part(`{"parents":["FOLDER1"]}`)
		mustDeny(t, h.eval(t, newRequest("POST", upload, body, withContentType("multipart/related; boundary=xyz; "+start))), ReasonBody, "multipart Content-Type")
	}
	// The protobuf alias of uploadType selects the route like uploadType.
	mustAllow(t, h.eval(t, newRequest("POST", strings.Replace(upload, "uploadType", "upload_type", 1), part(`{"parents":["FOLDER1"]}`), ct)))
}

const unboundSpec = `
schemaVersion: "1"
kind: filter
name: unbound
hosts: [api.example.com]
credential:
  static: { env: [UNBOUND_TOKEN] }
  inject: { header: authorization, format: "Bearer %s" }
  env: [UNBOUND_TOKEN]
capabilities:
  share:
    constraints:
      people: { kind: email, var: people }
routes:
  - op: items.share
    capability: share
    method: POST
    host: api.example.com
    path: /items/{id}/share
`

// A route that does not bind a constraint's variable passes only with "*".
func TestUnboundConstraintVar(t *testing.T) {
	restricted := parseSpecPolicy(t, unboundSpec, &spec.Preset{Capabilities: map[string]map[string]filter.ConstraintValue{
		"share": {"people": {Values: []string{"@example.com"}}},
	}})
	h := newHarnessForPolicy(t, restricted)
	mustDeny(t, h.eval(t, newRequest("POST", "https://api.example.com/items/1/share", "{}")), ReasonConstraint, "operation items.share cannot be checked against people")

	open := parseSpecPolicy(t, unboundSpec, &spec.Preset{Capabilities: map[string]map[string]filter.ConstraintValue{
		"share": {"people": {Any: true}},
	}})
	mustAllow(t, newHarnessForPolicy(t, open).eval(t, newRequest("POST", "https://api.example.com/items/1/share", "{}")))
}

func TestDefaultStarSkipsBindsAndResolvers(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	d := h.eval(t, newRequest("GET", "https://www.googleapis.com/drive/v3/files/ANY?alt=media", ""))
	mustAllow(t, d)
	if len(h.fetcher.urls) != 0 {
		t.Fatalf("resolver called for a \"*\" constraint: %v", h.fetcher.urls)
	}
}
