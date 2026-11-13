// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"net/http"
	"strings"
	"testing"
)

// I5: a request target outside the RFC 3986 grammar may be cut or decoded
// differently upstream. With a raw "#", a server that reads it as the start
// of a fragment drops format=raw and returns what the route did not allow.
func TestEvaluateDeniesTargetsOutsideRFC3986(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	messages := gmail + "/gmail/v1/users/me/messages/"
	tests := map[string]*http.Request{
		"hash in query":       newRequest("GET", messages+"abc?x=#&format=raw", ""),
		"hash before param":   newRequest("GET", messages+"abc?format=raw#", ""),
		"brackets in path":    newRequest("GET", messages+"a[b]", ""),
		"pipe in query":       newRequest("GET", messages+"abc?format=raw|full", ""),
		"space in query":      newRequest("GET", messages+"abc?format=raw%20&x=a%zz", ""),
		"quote in query":      newRequest("GET", messages+`abc?format="raw"`, ""),
		"backtick in query":   newRequest("GET", messages+"abc?x=`", ""),
		"curly brace in path": newRequest("GET", messages+"a%7Bb", ""),
	}
	// The target as received counts, not only the URL Go rebuilt from it.
	raw := newRequest("GET", messages+"abc?format=raw", "")
	raw.RequestURI = "/gmail/v1/users/me/messages/a<b>?format=raw"
	tests["raw target path"] = raw
	rawQuery := newRequest("GET", messages+"abc?format=raw", "")
	rawQuery.RequestURI = "/gmail/v1/users/me/messages/abc?format=raw#x"
	tests["raw target query"] = rawQuery
	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			if name == "curly brace in path" {
				// Encoded, it is an ordinary character. No route captures it.
				mustDeny(t, h.eval(t, req), ReasonNoRoute, "")
				return
			}
			mustDeny(t, h.eval(t, req), ReasonAmbiguous, "")
			if h.tokens.calls != 0 {
				t.Fatal("token minted for a denied request")
			}
		})
	}
	// Every RFC 3986 character passes the target checks.
	ok := newRequest("GET", messages+"a-b._~!$&'()*+,;=:@?format=raw&x=a/b?c:d@e!$'()*+,", "")
	mustDeny(t, h.eval(t, ok), ReasonNoRoute, "")
	mustAllow(t, h.eval(t, newRequest("GET", messages+"m1?format=raw&x=a/b?c:d@e!$'()*,", "")))
}

// Google APIs read "$alt" like alt and "$fields" like fields, so a name
// outside the plain charset could carry a parameter past its condition.
func TestEvaluateDeniesQueryParamNamesOutsideTheCharset(t *testing.T) {
	h := newHarness(t, "readonly", nil)
	messages := gmail + "/gmail/v1/users/me/messages/m1"
	for name, query := range map[string]string{
		"dollar alt":       "?$alt=media",
		"encoded dollar":   "?%24fields=raw",
		"content type":     "?$ct=text%2Fplain",
		"trailing space":   "?format+=metadata",
		"nul":              "?format%00=metadata",
		"empty name":       "?=raw",
		"non-ascii":        "?f%C3%B6rmat=raw",
		"bracketed array":  "?ids%5B%5D=a",
		"plus in the name": "?a%2Bb=1",
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("GET", messages+query, "")), ReasonAmbiguous, "is not allowed")
		})
	}
	mustAllow(t, h.eval(t, newRequest("GET", messages+"?format=raw&requestMask.includeField=a&page_token=x&x~y-z=1", "")))
}

// A body without a Content-Type, or with one that does not parse, may be
// read as anything upstream.
func TestEvaluateRequiresAContentTypeForBodies(t *testing.T) {
	h := newHarness(t, "edit-all", nil)
	doc := "https://docs.googleapis.com/v1/documents/DOC1:batchUpdate"
	for name, req := range map[string]*http.Request{
		"missing":      newRequest("POST", doc, `{}`, withoutContentType()),
		"empty":        newRequest("POST", doc, `{}`, withContentType("")),
		"no subtype":   newRequest("POST", doc, `{}`, withContentType("application")),
		"bad param":    newRequest("POST", doc, `{}`, withContentType("application/json; charset")),
		"repeated":     newRequest("POST", doc, `{}`, withContentType("application/json"), withContentType("application/json")),
		"chunked body": chunked(newRequest("POST", doc, `{}`, withoutContentType())),
	} {
		t.Run(name, func(t *testing.T) {
			d := mustDeny(t, h.eval(t, req), ReasonAmbiguous, "")
			if !strings.Contains(d.Message, "Content-Type") {
				t.Fatalf("message = %q, want it to name Content-Type", d.Message)
			}
		})
	}
	// Without a body, no Content-Type is needed.
	d := h.eval(t, newRequest("POST", doc, ""))
	mustAllow(t, d)
	d.Finish(200)
	d = h.eval(t, newRequest("POST", doc, `{"requests":[]}`, withContentType("application/json; charset=UTF-8")))
	mustAllow(t, d)
	d.Finish(200)
}

// chunked turns req into a request with a body of unknown length.
func chunked(req *http.Request) *http.Request {
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	return req
}
