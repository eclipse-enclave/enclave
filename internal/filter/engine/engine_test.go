// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"enclave/internal/filter"
	"enclave/internal/filter/spec"
	"enclave/internal/filter/token"
)

const gmail = "https://gmail.googleapis.com"

func TestNewFailsClosed(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	pol := *h.engine.Policy()
	pol.Placeholder = "short"
	if _, err := New(Options{Policy: &pol, Tokens: &fakeTokens{}}); err == nil {
		t.Fatal("New accepted an invalid policy")
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("New accepted a missing policy")
	}
	good := *h.engine.Policy()
	if _, err := New(Options{Policy: &good}); err == nil {
		t.Fatal("New accepted a missing credential")
	}
	if _, err := New(Options{Policy: &good, Credential: filter.NewStaticCredential("gws", "x")}); err == nil {
		t.Fatal("New accepted a credential of the wrong type")
	}
	cred := filter.NewOAuth2Credential("gws", filter.OAuth2Credential{TokenURL: "https://oauth2.googleapis.com/token", ClientID: "c", RefreshToken: "r"})
	if _, err := New(Options{Policy: &good, Credential: cred, Tokens: &fakeTokens{}}); err == nil {
		t.Fatal("New accepted both Credential and Tokens")
	}
	if e, err := New(Options{Policy: &good, Credential: cred}); err != nil || e.Filter() != "gws" || e.Preset() != "readonly" {
		t.Fatalf("New with credential = %v, %v", e, err)
	}
}

// I5: ambiguous requests are denied before any route matches.
func TestEvaluateDeniesAmbiguousRequests(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	get := func(path string, opts ...reqOption) *http.Request { return newRequest("GET", gmail+path, "", opts...) }
	tests := []struct {
		name string
		req  *http.Request
	}{
		{"encoded slash", get("/gmail/v1/users/me/messages/a%2Fb")},
		{"encoded backslash", get("/gmail/v1/users/me/messages/a%5cb")},
		{"encoded nul", get("/gmail/v1/users/me/messages/a%00")},
		{"dot segment", get("/gmail/v1/users/me/../me/messages/x")},
		{"encoded dot segment", get("/gmail/v1/users/me/%2e%2e/me/messages/x")},
		{"empty segment", get("/gmail/v1/users//me/messages/x")},
		{"method override", get("/gmail/v1/users/me/messages/x", withHeader("X-HTTP-Method-Override", "DELETE"))},
		{"method header", get("/gmail/v1/users/me/messages/x", withHeader("X-HTTP-Method", "DELETE"))},
		{"method override alt", get("/gmail/v1/users/me/messages/x", withHeader("X-Method-Override", "DELETE"))},
		{"upgrade", get("/gmail/v1/users/me/messages/x", withHeader("Upgrade", "websocket"))},
		{"query semicolon", get("/gmail/v1/users/me/messages/x?format=full;alt=media")},
		{"query bad escape", get("/gmail/v1/users/me/messages/x?format=%zz")},
		{"two content types", newRequest("POST", gmail+"/gmail/v1/users/me/messages/send", "{}", withContentType("application/json"), withContentType("text/plain"))},
		{"two content encodings", newRequest("POST", gmail+"/gmail/v1/users/me/messages/send", "{}", withHeader("Content-Encoding", "identity"), withHeader("Content-Encoding", "gzip"))},
	}
	trailer := get("/gmail/v1/users/me/messages/x")
	trailer.Trailer = http.Header{"Authorization": nil}
	tests = append(tests, struct {
		name string
		req  *http.Request
	}{"trailer", trailer})
	sni := get("/gmail/v1/users/me/messages/x")
	sni.TLS = &tls.ConnectionState{ServerName: "www.googleapis.com"}
	tests = append(tests, struct {
		name string
		req  *http.Request
	}{"sni mismatch", sni})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustDeny(t, h.eval(t, tt.req), ReasonAmbiguous, "")
			if h.tokens.calls != 0 {
				t.Fatal("token minted for a denied request")
			}
		})
	}
}

func TestEvaluateDeniesPlainHTTPAndForeignHosts(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	d := h.engine.Evaluate(context.Background(), "http", newRequest("GET", "http://gmail.googleapis.com/gmail/v1/users/me/messages/x", ""))
	mustDeny(t, d, ReasonPlainHTTP, "plain HTTP")
	if d.Denial.Filter != "gws" {
		t.Fatalf("denial filter = %q, want gws", d.Denial.Filter)
	}
	d = h.eval(t, newRequest("GET", "https://evil.example/gmail/v1/users/me/messages/x", ""))
	mustDeny(t, d, ReasonNotGoverned, "not governed")
	d = h.eval(t, newRequest("GET", "https://googleapis.com/gmail/v1/users/me/messages/x", ""))
	mustDeny(t, d, ReasonNotGoverned, "not governed")
	if !h.engine.Governs("gmail.googleapis.com:443") || h.engine.Governs("googleapis.com") || h.engine.Governs("") {
		t.Fatal("Governs is wrong")
	}
}

// A filter governs every name below its hosts. Routes still match their
// hosts exactly, so a subdomain reaches the engine and finds no route.
func TestEvaluateGovernsSubdomainsWithoutRoutes(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	if !h.engine.Governs("sub.gmail.googleapis.com") || !h.engine.Governs("a.b.www.googleapis.com:443") {
		t.Fatal("a subdomain of a governed host is not governed")
	}
	d := h.eval(t, newRequest("GET", "https://sub.gmail.googleapis.com/gmail/v1/users/me/messages/x", ""))
	mustDeny(t, d, ReasonNoRoute, "no route for GET sub.gmail.googleapis.com/gmail/v1/users/me/messages/x")
}

// I4 and I11: foreign credentials and placeholder leaks on governed hosts.
func TestEvaluateDeniesForeignCredentials(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	const msg = "/gmail/v1/users/me/messages/m1"
	tests := []struct {
		name   string
		req    *http.Request
		reason string
	}{
		{"api key header", newRequest("GET", gmail+msg, "", withHeader("X-Goog-Api-Key", "AIza-attacker")), ReasonForeignCredential},
		{"cookie", newRequest("GET", gmail+msg, "", withHeader("Cookie", "SID=attacker")), ReasonForeignCredential},
		{"access_token param", newRequest("GET", gmail+msg+"?access_token=ya29.attacker", ""), ReasonForeignCredential},
		{"accessToken spelling", newRequest("GET", gmail+msg+"?accessToken=ya29.attacker", ""), ReasonForeignCredential},
		{"upper spelling", newRequest("GET", gmail+msg+"?ACCESS_TOKEN=x", ""), ReasonForeignCredential},
		{"mixed case spelling", newRequest("GET", gmail+msg+"?Access_Token=x", ""), ReasonForeignCredential},
		{"folded spelling", newRequest("GET", gmail+msg+"?accesstoken=x", ""), ReasonForeignCredential},
		{"extra underscore spelling", newRequest("GET", gmail+msg+"?oauth__token=x", ""), ReasonForeignCredential},
		{"upper key", newRequest("GET", gmail+msg+"?KEY=AIza", ""), ReasonForeignCredential},
		{"kelvin sign key", newRequest("GET", gmail+msg+"?%E2%84%AAey=AIza", ""), ReasonForeignCredential},
		{"key param", newRequest("GET", gmail+msg+"?key=AIza", ""), ReasonForeignCredential},
		{"foreign bearer", newRequest("GET", gmail+msg, "", withoutAuth(), withHeader("Authorization", "Bearer ya29.attacker")), ReasonForeignCredential},
		{"placeholder plus suffix", newRequest("GET", gmail+msg, "", withoutAuth(), withHeader("Authorization", "Bearer "+testPlaceholder+"x")), ReasonForeignCredential},
		{"lowercase scheme", newRequest("GET", gmail+msg, "", withoutAuth(), withHeader("Authorization", "bearer "+testPlaceholder)), ReasonForeignCredential},
		{"two authorization values", newRequest("GET", gmail+msg, "", withHeader("Authorization", "Bearer "+testPlaceholder)), ReasonForeignCredential},
		{"placeholder in other header", newRequest("GET", gmail+msg, "", withHeader("X-Debug", testPlaceholder)), ReasonPlaceholder},
		{"placeholder in proxy auth", newRequest("GET", gmail+msg, "", withHeader("Proxy-Authorization", "Bearer "+testPlaceholder)), ReasonPlaceholder},
		{"placeholder in header name", newRequest("GET", gmail+msg, "", withHeader("X-"+testPlaceholder, "1")), ReasonPlaceholder},
		{"placeholder in query", newRequest("GET", gmail+msg+"?q="+testPlaceholder, ""), ReasonPlaceholder},
		{"encoded placeholder in query", newRequest("GET", gmail+msg+"?q=%45NCLAVE_SECRET_0123456789abcdef0123456789abcdef", ""), ReasonPlaceholder},
		{"placeholder in path", newRequest("GET", gmail+"/gmail/v1/users/me/messages/"+testPlaceholder, ""), ReasonPlaceholder},
		{"encoded placeholder in path", newRequest("GET", gmail+"/gmail/v1/users/me/messages/%45NCLAVE_SECRET_0123456789abcdef0123456789abcdef", ""), ReasonPlaceholder},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustDeny(t, h.eval(t, tt.req), tt.reason, "")
			if h.tokens.calls != 0 {
				t.Fatal("token minted for a denied request")
			}
		})
	}
}

// I2 and I3: only a matched, enabled route gets the credential, which
// replaces the placeholder in the injection header.
func TestEvaluateInjectsCredentialForAllowedRoute(t *testing.T) {
	for _, auth := range []string{"Bearer " + testPlaceholder, testPlaceholder} {
		h := newHarness(t, "readonly", nil)
		req := newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1?format=full", "", withoutAuth(), withHeader("Authorization", auth))
		d := h.eval(t, req)
		mustAllow(t, d)
		if got := req.Header.Get("Authorization"); got != "Bearer "+realToken {
			t.Fatalf("Authorization = %q", got)
		}
		if d.Op != "gmail.messages.get" || d.Capability != "mail.read" || d.Filter != "gws" || !d.Allowed() {
			t.Fatalf("decision = %+v", d)
		}
		d.Finish(200)
	}
}

func TestEvaluateRequiresPlaceholderForCredentialRoutes(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	d := h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1", "", withoutAuth()))
	mustDeny(t, d, ReasonMissingCredential, "needs the filter placeholder")

	// Without the placeholder no credentialed resolver lookup happens.
	edit := newHarness(t, "folder-edit", nil)
	d = edit.eval(t, newRequest("POST", "https://docs.googleapis.com/v1/documents/FILE_IN:batchUpdate", "{}", withoutAuth()))
	mustDeny(t, d, ReasonMissingCredential, "needs the filter placeholder")
	if len(edit.fetcher.urls) != 0 || edit.tokens.calls != 0 {
		t.Fatalf("lookups %v, token calls %d", edit.fetcher.urls, edit.tokens.calls)
	}
}

func TestEvaluatePublicRouteStripsCredential(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	for _, opts := range [][]reqOption{nil, {withoutAuth()}} {
		req := newRequest("GET", "https://www.googleapis.com/discovery/v1/apis/drive/v3/rest", "", opts...)
		d := h.eval(t, req)
		mustAllow(t, d)
		if _, ok := req.Header["Authorization"]; ok {
			t.Fatalf("public route kept Authorization: %q", req.Header.Get("Authorization"))
		}
	}
	if h.tokens.calls != 0 {
		t.Fatal("public route minted a token")
	}
}

func TestEvaluateRouteMatching(t *testing.T) {
	tests := []struct {
		name    string
		preset  string
		req     *http.Request
		reason  string
		message string
	}{
		{"no route", "readonly", newRequest("GET", gmail+"/gmail/v1/users/me/drafts", ""), ReasonNoRoute, "no route for GET gmail.googleapis.com/gmail/v1/users/me/drafts"},
		{"wrong method", "readonly", newRequest("DELETE", gmail+"/gmail/v1/users/me/messages/m1", ""), ReasonNoRoute, "no route for DELETE"},
		{"wrong host", "readonly", newRequest("GET", "https://www.googleapis.com/gmail/v1/users/me/messages/m1", ""), ReasonNoRoute, "no route"},
		{"query value not listed", "readonly", newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1?format=metadata", ""), ReasonNoRoute, "no route"},
		{"repeated query value", "readonly", newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1?format=full&format=metadata", ""), ReasonNoRoute, "no route"},
		{"capability not granted", "readonly", newRequest("POST", gmail+"/gmail/v1/users/me/messages/send", sendBody("To: a@eclipsesource.com")),
			ReasonCapability, "gmail.messages.send needs capability mail.send, not granted by preset readonly"},
		{"deny route", "edit-all", newRequest("POST", "https://www.googleapis.com/drive/v3/files/F1/permissions", "{}"), ReasonDenyRoute, "drive.permissions.create is never allowed"},
		{"deny route reason", "edit-all", newRequest("DELETE", "https://www.googleapis.com/drive/v3/files/F1/permissions/p1", ""), ReasonDenyRoute, "never allowed: sharing changes are never allowed"},
		{"snake case query alias", "edit-all", newRequest("PATCH", "https://www.googleapis.com/drive/v3/files/F1?add_parents=X", "{}"), ReasonNoRoute, "no route"},
		{"camel case query", "edit-all", newRequest("PATCH", "https://www.googleapis.com/drive/v3/files/F1?addParents=X", "{}"), ReasonNoRoute, "no route"},
		{"form body", "send-eclipsesource", newRequest("POST", gmail+"/gmail/v1/users/me/messages/send", "access_token=x", withContentType("application/x-www-form-urlencoded")),
			ReasonFormBody, "form-encoded"},
		{"multipart form body", "send-eclipsesource", newRequest("POST", gmail+"/gmail/v1/users/me/messages/send", "x", withContentType("multipart/form-data; boundary=b")),
			ReasonFormBody, "form-encoded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.preset, nil)
			mustDeny(t, h.eval(t, tt.req), tt.reason, tt.message)
		})
	}
}

// I5: a request parameter that folds like a parameter the route constrains
// or binds must be that parameter or, with jsonKeys protobuf, its snake_case
// alias. Upstream may read any other spelling as the parameter or ignore it
// and fall back to a default the route does not allow.
func TestEvaluateDeniesAmbiguousQueryParams(t *testing.T) {
	tests := []struct {
		name   string
		preset string
		req    *http.Request
	}{
		{"Format", "readonly", newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1?Format=full", "")},
		{"for_mat", "readonly", newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1?for_mat=metadata", "")},
		{"FORMAT with exact format", "readonly", newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1?format=full&FORMAT=raw", "")},
		{"ALT", "readonly", newRequest("GET", "https://www.googleapis.com/drive/v3/files/F1?ALT=media", "")},
		{"Add_Parents", "edit-all", newRequest("PATCH", "https://www.googleapis.com/drive/v3/files/F1?Add_Parents=X", "{}")},
		{"addparents", "edit-all", newRequest("PATCH", "https://www.googleapis.com/drive/v3/files/F1?addparents=X", "{}")},
		{"UploadType", "folder-edit", newRequest("POST", "https://www.googleapis.com/upload/drive/v3/files?UploadType=multipart", "x", withContentType("multipart/related; boundary=b"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.preset, nil)
			mustDeny(t, h.eval(t, tt.req), ReasonAmbiguous, "is ambiguous with")
			if h.tokens.calls != 0 {
				t.Fatal("token minted for a denied request")
			}
		})
	}
	h := newHarness(t, "readonly", nil)
	mustDeny(t, h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1?Format=full", "")), ReasonAmbiguous,
		`gmail.messages.get: query parameter "Format" is ambiguous with format`)
	// Parameters the route neither constrains nor binds keep any spelling.
	mustAllow(t, h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1?format=full&Fields=id&fields=id", "")))
}

const querySpec = `
schemaVersion: "1"
kind: filter
name: query
%s
hosts: [api.example.com]
credential:
  static: { env: [QUERY_TOKEN] }
  inject: { header: authorization, format: "Bearer %%s" }
  env: [QUERY_TOKEN]
capabilities:
  read: {}
  move:
    constraints:
      parents: { kind: value, var: parents }
routes:
  - op: items.get
    capability: read
    method: GET
    host: api.example.com
    path: /items/{id}
    query: { viewMode: [basic, ~], requestMask.includeField: [~] }
  - op: items.move
    capability: move
    method: PATCH
    host: api.example.com
    path: /items/{id}
    bind:
      parents: [{ query: addParents }, { split: "," }]
`

func TestEvaluateQueryParamNames(t *testing.T) {
	preset := func() *spec.Preset {
		return &spec.Preset{Capabilities: map[string]map[string]filter.ConstraintValue{
			"read": {},
			"move": {"parents": {Values: []string{"P1", "P2"}}},
		}}
	}
	type check struct {
		method, query, reason, message string
	}
	run := func(t *testing.T, h *harness, checks []check) {
		t.Helper()
		for _, c := range checks {
			d := h.eval(t, newRequest(c.method, "https://api.example.com/items/1?"+c.query, ""))
			if c.reason == "" {
				if d.Denial != nil {
					t.Errorf("%s ?%s denied: %s", c.method, c.query, d.Denial.Message)
				}
				continue
			}
			if d.Denial == nil || d.Denial.Reason != c.reason || !strings.Contains(d.Denial.Message, c.message) {
				t.Errorf("%s ?%s = %+v, want %s %q", c.method, c.query, d.Denial, c.reason, c.message)
			}
		}
	}
	t.Run("protobuf", func(t *testing.T) {
		h := newHarnessForPolicy(t, parseSpecPolicy(t, fmt.Sprintf(querySpec, "jsonKeys: protobuf"), preset()))
		run(t, h, []check{
			{"GET", "viewMode=basic", "", ""},
			{"GET", "view_mode=basic", "", ""},
			{"GET", "view_mode=full", ReasonNoRoute, "no route"},
			{"GET", "viewMode=basic&view_mode=full", ReasonNoRoute, "no route"},
			{"GET", "ViewMode=full", ReasonAmbiguous, `"ViewMode" is ambiguous with viewMode`},
			{"GET", "viewmode=full", ReasonAmbiguous, "ambiguous"},
			{"GET", "VIEW_MODE=basic", ReasonAmbiguous, "ambiguous"},
			{"GET", "view_Mode=basic", ReasonAmbiguous, "ambiguous"},
			{"GET", "viewMode=basic&viewMode_=full", ReasonAmbiguous, "ambiguous"},
			{"GET", "request_mask.include_field=x", ReasonNoRoute, "no route"},
			{"GET", "requestMask.include_field=x", ReasonAmbiguous, "ambiguous with requestMask.includeField"},
			{"GET", "other=1&Other=2", "", ""},
			{"PATCH", "addParents=P1&add_parents=P2", "", ""},
			{"PATCH", "addParents=P1&add_parents=X", ReasonConstraint, `"X"`},
			{"PATCH", "add_parents=X", ReasonConstraint, `"X"`},
			{"PATCH", "AddParents=X", ReasonAmbiguous, "items.move: query parameter \"AddParents\" is ambiguous with addParents"},
		})
	})
	t.Run("exact", func(t *testing.T) {
		h := newHarnessForPolicy(t, parseSpecPolicy(t, fmt.Sprintf(querySpec, ""), preset()))
		run(t, h, []check{
			{"GET", "viewMode=basic", "", ""},
			{"GET", "view_mode=basic", ReasonAmbiguous, "ambiguous with viewMode"},
			{"GET", "view_mode=full", ReasonAmbiguous, "ambiguous with viewMode"},
			{"PATCH", "addParents=P1", "", ""},
			{"PATCH", "add_parents=P1", ReasonAmbiguous, "ambiguous with addParents"},
		})
	})
}

func TestEvaluateJSONConditions(t *testing.T) {
	h := newHarness(t, "edit-all", nil)
	drive := "https://www.googleapis.com/drive/v3/files/F1"
	tests := []struct {
		name string
		body string
		op   string
	}{
		{"trash", `{"trashed":true}`, "drive.files.trash"},
		{"untrash", `{"trashed":false}`, "drive.files.update"},
		{"absent", `{"name":"x"}`, "drive.files.update"},
		{"null is absent", `{"trashed":null}`, "drive.files.update"},
		{"empty body is absent", ``, "drive.files.update"},
		{"whitespace body is absent", "  \n", "drive.files.update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := h.eval(t, newRequest("PATCH", drive, tt.body, withContentType("application/json")))
			mustAllow(t, d)
			if d.Op != tt.op {
				t.Fatalf("op = %s, want %s", d.Op, tt.op)
			}
			d.Finish(200)
		})
	}
	t.Run("string not boolean", func(t *testing.T) {
		mustDeny(t, h.eval(t, newRequest("PATCH", drive, `{"trashed":"true"}`, withContentType("application/json"))), ReasonNoRoute, "no route")
	})
	// A body the first candidate's conditions cannot read denies, whatever
	// its declared type: it must not fall through to a later route.
	for name, tc := range map[string]struct{ body, contentType, want string }{
		"duplicate keys":    {`{"trashed":false,"trashed":true}`, "application/json", "drive.files.trash: the request body is not valid JSON or repeats a key"},
		"not json":          {`trashed=true`, "application/json", "is not valid JSON"},
		"declared as text":  {`trashed`, "text/plain", "is not declared as JSON"},
		"array body":        {`[{"trashed":true}]`, "application/json", "is not a JSON object"},
		"duplicate as text": {`{"trashed":true,"trashed":false}`, "text/plain", "is not declared as JSON"},
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("PATCH", drive, tc.body, withContentType(tc.contentType))), ReasonBody, tc.want)
		})
	}
	// I12: the message never quotes the body.
	d := mustDeny(t, h.eval(t, newRequest("PATCH", drive, `{"bodyKey":1,"bodyKey":2}`)), ReasonBody, "repeats a key")
	if strings.Contains(d.Message, "bodyKey") {
		t.Fatalf("message quotes the body: %s", d.Message)
	}
}

const protobufSpec = `
schemaVersion: "1"
kind: filter
name: proto
jsonKeys: protobuf
hosts: [api.example.com]
credential:
  static: { env: [PROTO_TOKEN] }
  inject: { header: x-api-key }
  env: [PROTO_TOKEN]
capabilities:
  edit: {}
routes:
  - op: items.update
    capability: edit
    method: PATCH
    host: api.example.com
    path: /items/{id}
    json: { "$.copyRequiresWriterPermission": [true, ~] }
  - op: items.form
    capability: edit
    method: POST
    host: api.example.com
    path: /form
    allowFormBody: true
`

func TestEvaluateProtobufKeysAndStaticInjection(t *testing.T) {
	h := newHarnessForPolicy(t, parseSpecPolicy(t, protobufSpec, &spec.Preset{Capabilities: map[string]map[string]filter.ConstraintValue{"edit": {}}}))
	ok := []string{`{"copyRequiresWriterPermission":true}`, `{"copy_requires_writer_permission":true}`, `{}`}
	for _, body := range ok {
		req := newRequest("PATCH", "https://api.example.com/items/1", body, withoutAuth(), withHeader("X-Api-Key", testPlaceholder))
		d := h.eval(t, req)
		mustAllow(t, d)
		if got := req.Header.Get("X-Api-Key"); got != realToken {
			t.Fatalf("X-Api-Key = %q", got)
		}
	}
	req := newRequest("PATCH", "https://api.example.com/items/1", `{"copy_requires_writer_permission":false}`, withoutAuth(), withHeader("X-Api-Key", testPlaceholder))
	mustDeny(t, h.eval(t, req), ReasonNoRoute, "no route")
	// Two keys that name the same field are a body the route cannot read.
	req = newRequest("PATCH", "https://api.example.com/items/1", `{"copyRequiresWriterPermission":true,"copy_requires_writer_permission":false}`,
		withoutAuth(), withHeader("X-Api-Key", testPlaceholder))
	mustDeny(t, h.eval(t, req), ReasonBody, "repeats a key")
	// With a non-Authorization injection header, any Authorization is foreign.
	req = newRequest("PATCH", "https://api.example.com/items/1", "{}", withHeader("X-Api-Key", testPlaceholder))
	mustDeny(t, h.eval(t, req), ReasonForeignCredential, "Authorization header is not allowed")
	// allowFormBody opts a route in.
	req = newRequest("POST", "https://api.example.com/form", "a=b", withoutAuth(), withHeader("X-Api-Key", testPlaceholder), withContentType("application/x-www-form-urlencoded"))
	mustAllow(t, h.eval(t, req))
}

func TestEvaluateBodyLimitsAndEncoding(t *testing.T) {
	h := newHarness(t, "folder-edit", nil)
	upload := "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart"
	big := strings.Repeat("a", 1<<20+1)
	mustDeny(t, h.eval(t, newRequest("POST", upload, big, withContentType("multipart/related; boundary=b"))), ReasonBody, "inspection limit")
	mustDeny(t, h.eval(t, newRequest("POST", upload, "x", withContentType("multipart/related; boundary=b"), withHeader("Content-Encoding", "gzip"))), ReasonBody, "compressed")
	// A body nobody inspects may stay compressed.
	d := h.eval(t, newRequest("POST", "https://docs.googleapis.com/v1/documents/DOC1:batchUpdate", "x", withHeader("Content-Encoding", "gzip")))
	mustAllow(t, d)
}

func TestEvaluateBuffersInspectedBody(t *testing.T) {
	h := newHarness(t, "send-eclipsesource", nil)
	body := sendBody("To: alice@eclipsesource.com")
	req := newRequest("POST", gmail+"/gmail/v1/users/me/messages/send", body, withContentType("application/json"))
	req.TransferEncoding = []string{"chunked"}
	mustAllow(t, h.eval(t, req))
	got, _ := io.ReadAll(req.Body)
	if string(got) != body || req.ContentLength != int64(len(body)) || req.TransferEncoding != nil {
		t.Fatalf("forwarded body = %q, length %d, te %v", got, req.ContentLength, req.TransferEncoding)
	}
	again, _ := req.GetBody()
	if replay, _ := io.ReadAll(again); string(replay) != body {
		t.Fatal("GetBody does not replay the body")
	}
}

func TestDenialRendering(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	d := mustDeny(t, h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/drafts", "")), ReasonNoRoute, "")
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Errors  []struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal(d.Body, &body); err != nil {
		t.Fatalf("deny body is not JSON: %v: %s", err, d.Body)
	}
	if body.Error.Code != 403 || body.Error.Status != "PERMISSION_DENIED" || body.Error.Message != d.Message || body.Error.Errors[0].Reason != "enclaveFilter" {
		t.Fatalf("deny body = %+v", body)
	}
	if !strings.HasPrefix(d.Message, "filter gws: ") || d.Code != 403 || d.Status != filter.StatusPermissionDenied || !strings.HasPrefix(d.ContentType, "application/json") {
		t.Fatalf("denial = %+v", d)
	}
	rec := httptest.NewRecorder()
	d.Write(rec)
	if rec.Code != 403 || rec.Body.String() != string(d.Body) || rec.Header().Get("Content-Type") != d.ContentType {
		t.Fatalf("Write = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}

	plain := newHarnessForPolicy(t, parseSpecPolicy(t, protobufSpec, &spec.Preset{Capabilities: map[string]map[string]filter.ConstraintValue{"edit": {}}}))
	pd := mustDeny(t, plain.eval(t, newRequest("GET", "https://api.example.com/x", "", withoutAuth())), ReasonNoRoute, "")
	if pd.ContentType != "text/plain; charset=utf-8" || string(pd.Body) != pd.Message+"\n" {
		t.Fatalf("plain denial = %q %q", pd.ContentType, pd.Body)
	}
}

// I6: a failed token refresh denies with 502 and never forwards.
func TestEvaluateTokenFailureDenies(t *testing.T) {
	h := newHarness(t, "readonly", map[string]string{"tight": "extends: readonly\nquotas:\n  mail.read: 1\n"})
	h.tokens.err = errors.New("refresh failed: " + realToken)
	req := newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1", "")
	d := mustDeny(t, h.eval(t, req), "token:error", "the token endpoint is unreachable or failed (error)")
	if d.Code != http.StatusBadGateway || d.Status != filter.StatusUnavailable || strings.Contains(string(d.Body), realToken) {
		t.Fatalf("denial = %+v", d)
	}
	if req.Header.Get("Authorization") != "Bearer "+testPlaceholder {
		t.Fatal("request was rewritten despite the failure")
	}
	// Only a rejected grant or client asks for a new login.
	for _, tc := range []struct {
		err     error
		reason  string
		message string
	}{
		{&token.RefreshError{Status: 400, Code: "invalid_grant"}, "token:invalid_grant", "(invalid_grant), run enclave filters login gws"},
		{&token.RefreshError{Status: 401, Code: "invalid_client"}, "token:invalid_client", "run enclave filters login gws"},
		{&token.RefreshError{Status: 400, Code: "unauthorized_client"}, "token:unauthorized_client", "run enclave filters login gws"},
		{&token.RefreshError{Err: token.ErrUnreachable}, "token:unreachable", "the token endpoint is unreachable or failed (unreachable)"},
		{&token.RefreshError{Status: 503}, "token:http-503", "(http-503)"},
		{&token.RefreshError{Status: 400, Code: "invalid_scope"}, "token:invalid_scope", "unreachable or failed (invalid_scope)"},
		{&token.RefreshError{Status: 200, Err: errors.New("no JSON")}, "token:bad-response", "(bad-response)"},
	} {
		h.tokens.err = tc.err
		d := mustDeny(t, h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1", "")), tc.reason, tc.message)
		if loginHint := strings.Contains(d.Message, "filters login"); loginHint != token.IsGrantError(tc.err) {
			t.Fatalf("%s: message %q", tc.reason, d.Message)
		}
	}

	tight := newHarness(t, "tight", map[string]string{"tight": "extends: readonly\nquotas:\n  mail.read: 1\n"})
	tight.tokens.err = errors.New("down")
	tight.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1", ""))
	tight.tokens.err = nil
	// The failed request released its reservation.
	d2 := tight.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/m2", ""))
	mustAllow(t, d2)
}

func TestFinishInvalidatesTokenOn401(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	d := h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1", ""))
	mustAllow(t, d)
	d.Finish(http.StatusUnauthorized)
	d.Finish(http.StatusUnauthorized)
	if len(h.tokens.invalidated) != 1 || h.tokens.invalidated[0] != realToken {
		t.Fatalf("invalidated = %v", h.tokens.invalidated)
	}
	pub := h.eval(t, newRequest("GET", "https://www.googleapis.com/discovery/v1/apis/drive/v3/rest", ""))
	pub.Finish(http.StatusUnauthorized)
	if len(h.tokens.invalidated) != 1 {
		t.Fatal("a public route invalidated the token")
	}
}

// I1 and I12: no denial carries the real token, the placeholder, header
// values, or bodies.
func TestDenialsNeverLeakSecretsOrContent(t *testing.T) {
	const headerSecret = "header-value-should-not-appear"
	const bodySecret = "body-content-should-not-appear"
	h := newHarness(t, "send-eclipsesource", nil)
	requests := []*http.Request{
		newRequest("GET", gmail+"/gmail/v1/users/me/drafts", "", withHeader("X-Note", headerSecret)),
		newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1", "", withHeader("X-Goog-Api-Key", headerSecret)),
		newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1", "", withoutAuth(), withHeader("Authorization", "Bearer "+headerSecret)),
		newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1", "", withHeader("X-Debug", testPlaceholder)),
		newRequest("POST", gmail+"/gmail/v1/users/me/messages/send", `{"raw":"`+bodySecret+`!"}`),
		newRequest("POST", gmail+"/gmail/v1/users/me/messages/send", sendBody("To: x@evil.com\r\nSubject: "+bodySecret)),
		newRequest("POST", gmail+"/gmail/v1/users/me/messages/modify", bodySecret),
	}
	for i, req := range requests {
		d := h.eval(t, req)
		if d.Denial == nil {
			t.Fatalf("request %d allowed", i)
		}
		for _, secret := range []string{realToken, testPlaceholder, headerSecret, bodySecret} {
			if strings.Contains(d.Denial.Message, secret) || strings.Contains(string(d.Denial.Body), secret) {
				t.Fatalf("request %d: denial leaks %q: %s", i, secret, d.Denial.Message)
			}
		}
	}
}

func TestDecisionExposesNoToken(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	d := h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/m1", ""))
	mustAllow(t, d)
	exported, _ := json.Marshal(d)
	if strings.Contains(string(exported), realToken) {
		t.Fatalf("decision exposes the token: %s", exported)
	}
}

const multipartConditionSpec = `
schemaVersion: "1"
kind: filter
name: mp
hosts: [api.example.com]
credential:
  static: { env: [MP_TOKEN] }
  inject: { header: authorization, format: "Bearer %s" }
  env: [MP_TOKEN]
capabilities:
  upload: {}
routes:
  - op: upload.private
    capability: upload
    method: POST
    host: api.example.com
    path: /upload
    json: { "$.shared": [false, ~] }
`

func TestEvaluateJSONConditionOnMultipartMetadata(t *testing.T) {
	h := newHarnessForPolicy(t, parseSpecPolicy(t, multipartConditionSpec, &spec.Preset{Capabilities: map[string]map[string]filter.ConstraintValue{"upload": {}}}))
	ct := withContentType("multipart/related; boundary=q")
	media := [2]string{"Content-Type: application/json", `{"shared":true}`}
	tests := []struct {
		name    string
		body    string
		allowed bool
	}{
		{"private", multipartBody("q", [2]string{"Content-Type: application/json", `{"shared":false}`}, media), true},
		{"absent", multipartBody("q", [2]string{"Content-Type: application/json", `{"name":"x"}`}, media), true},
		{"shared", multipartBody("q", [2]string{"Content-Type: application/json", `{"shared":true}`}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := h.eval(t, newRequest("POST", "https://api.example.com/upload", tt.body, ct))
			if tt.allowed {
				mustAllow(t, d)
				return
			}
			mustDeny(t, d, ReasonNoRoute, "no route")
		})
	}
	for name, body := range map[string]string{
		"broken multipart":         "--q\r\nContent-Type: application/json\r\n\r\n{}",
		"first part not json":      multipartBody("q", [2]string{"Content-Type: application/json", `shared`}, media),
		"first part declared text": multipartBody("q", [2]string{"Content-Type: text/plain", `x`}, [2]string{"Content-Type: application/json", `{}`}),
		// The metadata a server reads is the first part, so it must be JSON,
		// even if a later part is.
		"first part text with JSON": multipartBody("q", [2]string{"Content-Type: text/plain", `{"shared":true}`}, [2]string{"Content-Type: application/json", `{}`}),
		"first part without type":   multipartBody("q", [2]string{"Content-Length: 15", `{"shared":true}`}, media),
		"first part JSPB":           multipartBody("q", [2]string{"Content-Type: application/json+protobuf", `[true]`}, media),
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("POST", "https://api.example.com/upload", body, ct)), ReasonBody, "")
		})
	}
	// An empty first part has every field absent, like an empty body.
	mustAllow(t, h.eval(t, newRequest("POST", "https://api.example.com/upload", multipartBody("q", [2]string{"Content-Type: application/json", ""}, media), ct)))
}

func TestDenyMessagesBoundRequestValues(t *testing.T) {
	h := newHarness(t, "send-eclipsesource", nil)
	long := strings.Repeat("x", 500)
	d := mustDeny(t, h.eval(t, newRequest("GET", gmail+"/"+long, "")), ReasonNoRoute, "...")
	if len(d.Message) > 300 {
		t.Fatalf("message length %d", len(d.Message))
	}
	d = mustDeny(t, h.eval(t, newRequest("POST", sendURL, sendBody("To: "+long+"@evil.com"))), ReasonConstraint, "...")
	if len(d.Message) > 300 {
		t.Fatalf("message length %d", len(d.Message))
	}
}
