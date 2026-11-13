// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/filter"
)

func step(op, arg string) filter.BindStep { return filter.BindStep{Op: op, Arg: arg} }

func indexStep(i int) filter.BindStep { return filter.BindStep{Op: filter.StepMultipart, Index: &i} }

func TestRunBindSteps(t *testing.T) {
	header := http.Header{}
	header.Add("X-Ids", "a, b")
	header.Add("X-Ids", "c")
	related := multipartBody("b1",
		[2]string{"Content-Type: application/json; charset=UTF-8", `{"parents":["P1","P2"],"name":"n"}`},
		[2]string{"Content-Type: text/plain", "media"})
	in := bindInput{
		captures:        map[string]string{"id": "m1"},
		query:           url.Values{"addParents": {"F1,F2"}, "add_parents": {"F3"}, "AddParents": {"X"}},
		header:          header,
		body:            []byte(related),
		bodyContentType: "multipart/related; boundary=b1",
		response:        []byte(`{"messages":[{"id":"x"},{"id":"y"}]}`),
		responseType:    "application/json",
		jsonKeys:        filter.JSONKeysProtobuf,
	}
	tests := []struct {
		name  string
		steps []filter.BindStep
		want  []string
	}{
		{"path", []filter.BindStep{step(filter.StepPath, "id")}, []string{"m1"}},
		{"query name and protobuf alias", []filter.BindStep{step(filter.StepQuery, "addParents")}, []string{"F1,F2", "F3"}},
		{"query split", []filter.BindStep{step(filter.StepQuery, "addParents"), step(filter.StepSplit, ",")}, []string{"F1", "F2", "F3"}},
		{"query absent", []filter.BindStep{step(filter.StepQuery, "removeParents")}, nil},
		{"header split", []filter.BindStep{step(filter.StepHeader, "x-ids"), step(filter.StepSplit, ",")}, []string{"a", "b", "c"}},
		{"body", []filter.BindStep{step(filter.StepBody, "")}, []string{related}},
		{"multipart prefix", []filter.BindStep{step(filter.StepBody, ""), step(filter.StepMultipart, "application/json"), step(filter.StepJSON, "$.parents[*]")}, []string{"P1", "P2"}},
		{"multipart index", []filter.BindStep{step(filter.StepBody, ""), indexStep(1)}, []string{"media"}},
		{"response json", []filter.BindStep{step(filter.StepResponse, ""), step(filter.StepJSON, "$.messages[*].id")}, []string{"x", "y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runBind(tt.steps, in)
			if err != nil {
				t.Fatalf("runBind: %v", err)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("runBind = %q, want %q", got, tt.want)
			}
		})
	}
}

// Without jsonKeys protobuf a query bind reads the exact name only.
func TestRunBindQueryExactName(t *testing.T) {
	in := bindInput{query: url.Values{"addParents": {"A"}, "add_parents": {"B"}}}
	got, err := runBind([]filter.BindStep{step(filter.StepQuery, "addParents")}, in)
	if err != nil || !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("runBind = %v, %v", got, err)
	}
}

func TestRunBindJSONProtobufKeys(t *testing.T) {
	in := bindInput{body: []byte(`{"add_label_ids":["A"],"x":{"file_id":"F"}}`), bodyContentType: "application/json", jsonKeys: filter.JSONKeysProtobuf}
	got, err := runBind([]filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.addLabelIds[*]")}, in)
	if err != nil || !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("runBind = %v, %v", got, err)
	}
	in.body = []byte(`{"addLabelIds":["A"],"add_label_ids":["B"]}`)
	if _, err := runBind([]filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.addLabelIds[*]")}, in); err == nil {
		t.Fatal("colliding keys accepted")
	}
	// Without protobuf keys, add_label_ids is not renamed, but it folds like
	// addLabelIds, which a server may read it as.
	in.jsonKeys = ""
	_, err = runBind([]filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.addLabelIds[*]")}, in)
	var ambiguous *filter.AmbiguousKeyError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("exact mode error = %v, want an ambiguous key", err)
	}
	in.body = []byte(`{"addLabelIds":["A"]}`)
	got, err = runBind([]filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.addLabelIds[*]")}, in)
	if err != nil || !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("exact mode = %v, %v", got, err)
	}
}

func TestRunBindErrors(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		steps []filter.BindStep
	}{
		{"json syntax", `{"raw":`, []filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.raw")}},
		{"json empty body", ``, []filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.raw")}},
		{"json duplicate", `{"raw":"a","raw":"b"}`, []filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.raw")}},
		{"json object leaf", `{"raw":{}}`, []filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.raw")}},
		{"base64 invalid", `{"raw":"!!!"}`, []filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.raw"), step(filter.StepBase64URL, "")}},
		{"base64 whitespace", `{"raw":"YW Jj"}`, []filter.BindStep{step(filter.StepBody, ""), step(filter.StepJSON, "$.raw"), step(filter.StepBase64URL, "")}},
		{"not multipart", `{}`, []filter.BindStep{step(filter.StepBody, ""), step(filter.StepMultipart, "application/json")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := runBind(tt.steps, bindInput{body: []byte(tt.body), bodyContentType: "application/json"}); err == nil {
				t.Fatalf("runBind = %q, want error", got)
			}
		})
	}
	if _, err := runBind([]filter.BindStep{step(filter.StepPath, "missing")}, bindInput{}); err == nil {
		t.Fatal("missing capture accepted")
	}
	if _, err := runBind([]filter.BindStep{step("xml", "")}, bindInput{}); err == nil {
		t.Fatal("unknown step accepted")
	}
}

func TestDecodeBase64URL(t *testing.T) {
	plain := "To: a@b.com\n\xff\xfe?>"
	for name, encoded := range map[string]string{
		"url raw":      base64.RawURLEncoding.EncodeToString([]byte(plain)),
		"url padded":   base64.URLEncoding.EncodeToString([]byte(plain)),
		"std padded":   base64.StdEncoding.EncodeToString([]byte(plain)),
		"std unpadded": base64.RawStdEncoding.EncodeToString([]byte(plain)),
	} {
		got, err := decodeBase64URL(encoded)
		if err != nil || string(got) != plain {
			t.Errorf("%s: decodeBase64URL = %q, %v", name, got, err)
		}
	}
	for _, bad := range []string{"a", "ab$c", "YW\nJj"} {
		if _, err := decodeBase64URL(bad); err == nil {
			t.Errorf("decodeBase64URL(%q) accepted", bad)
		}
	}
}

func TestRFC822Recipients(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    []string
	}{
		{"all headers", "From: me@x.com\r\nTo: A <Alice@Example.com>, bob@example.com\r\nCc: carol@example.com\r\nBcc: dave@example.com\r\n" +
			"Resent-To: erin@example.com\r\nResent-Cc: frank@example.com\r\nResent-Bcc: grace@example.com\r\nSubject: hi\r\n\r\nTo: body@evil.com\r\n",
			[]string{"alice@example.com", "bob@example.com", "carol@example.com", "dave@example.com", "erin@example.com", "frank@example.com", "grace@example.com"}},
		{"repeated and lowercase header names", "to: a@example.com\nTO: b@example.com\n\nbody", []string{"a@example.com", "b@example.com"}},
		{"mixed case names", "bCC: a@example.com\r\nresent-TO: b@example.com\r\n\r\n", []string{"a@example.com", "b@example.com"}},
		{"multiple bcc headers", "To: a@example.com\r\nBcc: b@example.com\r\nBcc: c@evil.com, d@evil.com\r\n\r\n", []string{"a@example.com", "b@example.com", "c@evil.com", "d@evil.com"}},
		{"folded header", "To: a@example.com,\r\n b@evil.com\r\n\r\n", []string{"a@example.com", "b@evil.com"}},
		{"folded with tab", "Bcc:\r\n\ta@example.com,\r\n\t b@evil.com\r\n\r\n", []string{"a@example.com", "b@evil.com"}},
		{"folded lf", "Cc: a@example.com,\n b@evil.com\n\n", []string{"a@example.com", "b@evil.com"}},
		{"obsolete space before colon", "To: a@example.com\r\nBcc : x@evil.com\r\n\r\n", []string{"a@example.com", "x@evil.com"}},
		{"obsolete tab before colon", "To: a@example.com\r\nBcc\t: x@evil.com\r\n\r\n", []string{"a@example.com", "x@evil.com"}},
		{"obsolete whitespace before colon", "Resent-Cc \t : x@evil.com\r\n\r\n", []string{"x@evil.com"}},
		{"tab after colon", "To:\ta@example.com\r\n\r\n", []string{"a@example.com"}},
		{"encoded name", "To: =?utf-8?q?J=C3=B6rg?= <joerg@example.com>\r\n\r\n", []string{"joerg@example.com"}},
		{"trailing comment", "To: a@example.com (x@evil.com)\r\n\r\n", []string{"a@example.com"}},
		{"quoted local part", "To: \"x@example.com\"@evil.com\r\n\r\n", []string{"x@example.com@evil.com"}},
		{"quoted local part with space", "To: \"a b\"@example.com\r\n\r\n", []string{"a b@example.com"}},
		{"idn domain", "To: user@B\u00dcCHER.de\r\n\r\n", []string{"user@b\u00dccher.de"}},
		{"no unicode folding", "To: \u212Aate@example.com\r\n\r\n", []string{"\u212Aate@example.com"}},
		{"group", "To: team: a@example.com, b@evil.com;\r\n\r\n", []string{"a@example.com", "b@evil.com"}},
		{"empty group", "To: undisclosed-recipients:;\r\n\r\n", nil},
		{"delivered-to ignored", "Delivered-To: x@evil.com\r\nX-Original-To: y@evil.com\r\nTo: a@example.com\r\n\r\n", []string{"a@example.com"}},
		{"headers only", "To: a@example.com\r\n", []string{"a@example.com"}},
		{"no final line ending", "To: a@example.com", []string{"a@example.com"}},
		{"empty header ignored", "To: \r\nCc: a@example.com\r\n\r\n", []string{"a@example.com"}},
		{"no recipients", "Subject: x\r\n\r\n", nil},
		{"body not parsed", "To: a@example.com\r\n\r\nBcc : x\r\n\x00\n", []string{"a@example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rfc822Addresses(tt.message, recipientHeaders)
			if err != nil {
				t.Fatalf("rfc822Addresses: %v", err)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rfc822Addresses = %q, want %q", got, tt.want)
			}
		})
	}
}

// The destinations mode adds the fields that send replies, receipts, and
// bounces to an address, so a constraint can check them like recipients.
func TestRFC822Destinations(t *testing.T) {
	message := "From: me@example.com\r\nSender: me@example.com\r\nTo: a@example.com\r\n" +
		"reply-to : Evil <r@evil.example>\r\nMail-Reply-To: mr@evil.example\r\nMail-Followup-To: f@evil.example, g@evil.example\r\n" +
		"Disposition-Notification-To: <d@evil.example>\r\nReturn-Receipt-To: rr@evil.example\r\nErrors-To: e@evil.example\r\n\r\nReply-To: body@evil.example\r\n"
	got, err := runBind([]filter.BindStep{step(filter.StepBody, ""), step(filter.StepRFC822, filter.RFC822Destinations)}, bindInput{body: []byte(message)})
	want := []string{"a@example.com", "r@evil.example", "mr@evil.example", "f@evil.example", "g@evil.example", "d@evil.example", "rr@evil.example", "e@evil.example"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("destinations = %q, %v, want %q", got, err, want)
	}
	got, err = runBind([]filter.BindStep{step(filter.StepBody, ""), step(filter.StepRFC822, filter.RFC822Recipients)}, bindInput{body: []byte(message)})
	if err != nil || !reflect.DeepEqual(got, []string{"a@example.com"}) {
		t.Fatalf("recipients = %q, %v", got, err)
	}
	if _, err := rfc822Addresses("To: a@example.com\r\nReply-To: not an address\r\n\r\n", append(recipientHeaders, replyHeaders...)); err == nil ||
		!strings.Contains(err.Error(), "invalid Reply-To header") {
		t.Fatalf("invalid Reply-To = %v", err)
	}
}

func TestRFC822RecipientsRejects(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{"invalid address", "To: not an address\r\n\r\n", "invalid To header"},
		{"leading comment", "To: (x@evil.com) a@example.com\r\n\r\n", "invalid To header"},
		{"comment in address", "To: a(x)@example.com\r\n\r\n", "invalid To header"},
		{"source route", "To: <@relay.evil.com:a@example.com>\r\n\r\n", "invalid To header"},
		{"unterminated group", "Bcc: team: a@example.com, b@evil.com\r\n\r\n", "invalid Bcc header"},
		{"nested group", "Cc: outer: inner: a@example.com;;\r\n\r\n", "invalid Cc header"},
		{"bare cr smuggling", "To: a@example.com\rBcc: evil@evil.com\r\n\r\n", "line endings"},
		{"bare cr line ending", "To: a@example.com\r\n\rBcc: evil@evil.com\r\n\r\n", "line endings"},
		{"lf after crlf", "To: a@example.com\r\nBcc: evil@evil.com\n\r\n", "line endings"},
		{"lf blank line after crlf", "To: a@example.com\r\n\nBcc: evil@evil.com\r\n\r\n", "line endings"},
		{"crlf after lf", "To: a@example.com\nBcc: evil@evil.com\r\n\n", "line endings"},
		{"nul", "To: a@example.com\r\nX: \x00\r\nBcc: evil@evil.com\r\n\r\n", "NUL"},
		{"malformed header line", "To a@example.com\r\n\r\n", "malformed"},
		{"space inside name", "B cc: evil@evil.com\r\n\r\n", "header name contains whitespace"},
		{"tab inside name", "B\tcc: evil@evil.com\r\n\r\n", "header name contains whitespace"},
		{"control in name", "\vBcc: evil@evil.com\r\n\r\n", "header name contains whitespace"},
		{"non-ascii name", "B\u0441\u0441: evil@evil.com\r\n\r\n", "header name contains whitespace"},
		{"bom", "\ufeffTo: a@example.com\r\n\r\n", "header name contains whitespace"},
		{"empty name", "To: a@example.com\r\n: evil@evil.com\r\n\r\n", "empty message header name"},
		{"encoded word name", "=?utf-8?q?Bcc?=: evil@evil.com\r\n\r\n", "encoded word"},
		{"leading fold", " Bcc: evil@evil.com\r\nTo: a@example.com\r\n\r\n", "start with a folded line"},
		{"whitespace-only fold", "To: a@example.com\r\n \r\nBcc: evil@evil.com\r\n\r\n", "whitespace-only"},
		{"tab-only fold", "To: a@example.com,\n\t\n b@evil.com\n\n", "whitespace-only"},
		{"folded into next header", "To: a@example.com\r\n Bcc: evil@evil.com\r\n\r\n", "invalid To header"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rfc822Addresses(tt.message, recipientHeaders)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("rfc822Addresses = %q, %v, want error %q", got, err, tt.want)
			}
			if strings.Contains(err.Error(), "evil") {
				t.Fatalf("error leaks message content: %v", err)
			}
		})
	}
}

func TestParseMultipartRejects(t *testing.T) {
	jsonPart := [2]string{"Content-Type: application/json", `{"parents":["P"]}`}
	media := [2]string{"Content-Type: text/plain", "x"}
	many := make([][2]string, filter.MaxMultipartParts+1)
	for i := range many {
		many[i] = media
	}
	tests := []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{"not multipart", "application/json", "{}", "not multipart"},
		{"no boundary", "multipart/related", multipartBody("b", jsonPart), "no boundary"},
		{"duplicate boundary", "multipart/related; boundary=b; boundary=c", multipartBody("b", jsonPart), "not multipart"},
		{"quoted printable", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: message/rfc822\r\nContent-Transfer-Encoding: quoted-printable", "=54o: evil@evil.com"}), "transfer encoding"},
		{"base64 part", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: application/json\r\nContent-Transfer-Encoding: base64", "e30="}), "transfer encoding"},
		{"repeated content type", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: application/json\r\nContent-Type: text/plain", "{}"}), "repeats a header"},
		{"nested", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: multipart/mixed; boundary=c", multipartBody("c", jsonPart)}), "nested"},
		{"too many parts", "multipart/related; boundary=b", multipartBody("b", many...), "more than"},
		{"no parts", "multipart/related; boundary=b", "--b--\r\n", "no parts"},
		{"start", `multipart/related; boundary=b; start="<root@x>"`, multipartBody("b", jsonPart), "start part"},
		{"start uppercase", "multipart/related; boundary=b; START=x", multipartBody("b", jsonPart), "start part"},
		{"start-info", "multipart/related; boundary=b; start-info=x", multipartBody("b", jsonPart), "start part"},
		{"start continuation", "multipart/related; boundary=b; start*0=x", multipartBody("b", jsonPart), "RFC 2231"},
		{"start undecodable", "multipart/related; boundary=b; start*=x", multipartBody("b", jsonPart), "RFC 2231"},
		{"boundary override", "multipart/related; boundary=b; boundary*=us-ascii''c", multipartBody("b", jsonPart), "RFC 2231"},
		{"repeated equal boundary", "multipart/related; boundary=b; boundary=b", multipartBody("b", jsonPart), "repeats a parameter"},
		{"escaped quoted value", `multipart/related; boundary="b\\c"`, multipartBody(`b\c`, jsonPart), "invalid multipart Content-Type"},
		{"content id", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: application/json\r\nContent-ID: <meta@x>", "{}"}), "other than Content-Type"},
		{"content id lowercase", "multipart/related; boundary=b",
			multipartBody("b", jsonPart, [2]string{"content-id: <media@x>\r\nContent-Type: text/plain", "x"}), "other than Content-Type"},
		{"content id before colon space", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: application/json\r\nContent-ID : <meta@x>", "{}"}), "malformed multipart part header"},
		{"transfer encoding before colon space", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: message/rfc822\r\nContent-Transfer-Encoding : quoted-printable", "=54o: evil@evil.com"}), "malformed multipart part header"},
		{"truncated", "multipart/related; boundary=b", "--b\r\nContent-Type: application/json\r\n\r\n{}", "invalid multipart"},
		// A message header in the part header block: a parser that ends
		// the part headers elsewhere reads it as the first message header.
		{"to header in a part", "multipart/related; boundary=b",
			multipartBody("b", jsonPart, [2]string{"Content-Type: message/rfc822\r\nTo: evil@evil.example", "To: ok@example.com\r\n\r\nx"}), "other than Content-Type"},
		{"whitespace-only folded line", "multipart/related; boundary=b",
			"--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: message/rfc822\r\n \r\nTo: evil@evil.example\r\n\r\nTo: ok@example.com\r\n\r\nx\r\n--b--\r\n",
			"must not be folded"},
		{"folded header", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: application/json;\r\n charset=UTF-8", "{}"}), "must not be folded"},
		{"bare LF blank line", "multipart/related; boundary=b",
			"--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: message/rfc822\r\n\nTo: ok@example.com\r\n\r\nTo: evil@evil.example\r\n\r\nx\r\n--b--\r\n",
			"CRLF line endings"},
		{"LF only", "multipart/related; boundary=b", "--b\nContent-Type: application/json\n\n{}\n--b--\n", "invalid multipart delimiter line"},
		{"bare CR", "multipart/related; boundary=b", multipartBody("b", [2]string{"Content-Type: application/json\rX: y", "{}"}), "CRLF line endings"},
		{"preamble", "multipart/related; boundary=b", "To: evil@evil.example\r\n" + multipartBody("b", jsonPart), "preamble"},
		{"preamble without CRLF", "multipart/related; boundary=b", "  " + multipartBody("b", jsonPart), "preamble"},
		{"epilogue", "multipart/related; boundary=b", multipartBody("b", jsonPart) + "--b\r\nContent-Type: text/plain\r\n\r\nx", "epilogue"},
		{"boundary inside a part", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: text/plain", "x\n--b\r\nContent-Type: application/json\r\n\r\n{}"}), "contains its boundary"},
		{"delimiter with trailing text", "multipart/related; boundary=b",
			"--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--bX\r\n\r\nx\r\n--b--\r\n", "invalid multipart delimiter line"},
		{"other header", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: application/json\r\nContent-Disposition: inline", "{}"}), "other than Content-Type"},
		{"content length mismatch", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: application/json\r\nContent-Length: 1", "{}"}), "Content-Length does not match"},
		{"content length sign", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: application/json\r\nContent-Length: +2", "{}"}), "Content-Length does not match"},
		{"part content type does not parse", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: application/json; charset", "{}"}), "Content-Type does not parse"},
		{"non-ascii header value", "multipart/related; boundary=b",
			multipartBody("b", [2]string{"Content-Type: text/plain; name=\"\xc3\xa4\"", "{}"}), "non-ASCII"},
		{"invalid boundary", "multipart/related; boundary=\"a b \"", multipartBody("a b ", jsonPart), "boundary is not valid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseMultipart([]byte(tt.body), tt.contentType)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseMultipart = %v, want %q", err, tt.want)
			}
		})
	}
}

// gwsSendBody is the body gws 0.22.5 sent for `gmail +send`, captured in the
// end-to-end run: JSON metadata, then the RFC 822 message.
const gwsSendBody = "--gws_boundary_798bf456d0cb0b59\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n{}\r\n" +
	"--gws_boundary_798bf456d0cb0b59\r\nContent-Type: message/rfc822\r\n\r\nTo: <alice@eclipsesource.com>\r\nSubject: hi\r\n" +
	"Bcc: <someone@gmail.com>\r\nMessage-ID: <18e7a14889bd5681.39a188cab2da06fe.c5d980e7dda9c486@78d9ed478501>\r\n" +
	"Date: Fri, 13 Nov 2026 14:31:49 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"utf-8\"\r\n" +
	"Content-Transfer-Encoding: 7bit\r\n\r\nhello\r\n--gws_boundary_798bf456d0cb0b59--\r\n"

const gwsSendContentType = "multipart/related; boundary=gws_boundary_798bf456d0cb0b59"

func TestParseMultipartAcceptsTheGWSClient(t *testing.T) {
	parts, err := parseMultipart([]byte(gwsSendBody), gwsSendContentType)
	if err != nil || len(parts) != 2 {
		t.Fatalf("parseMultipart = %d parts, %v", len(parts), err)
	}
	if string(parts[0].data) != "{}" || parts[1].contentType != "message/rfc822" {
		t.Fatalf("parts = %q, %q", parts[0].data, parts[1].contentType)
	}
	recipients, err := runBind([]filter.BindStep{{Op: filter.StepBody}, indexStep(1), {Op: filter.StepRFC822, Arg: filter.RFC822Recipients}},
		bindInput{body: []byte(gwsSendBody), bodyContentType: gwsSendContentType})
	if err != nil || strings.Join(recipients, ",") != "alice@eclipsesource.com,someone@gmail.com" {
		t.Fatalf("recipients = %v, %v", recipients, err)
	}
}

func TestParseMultipartAcceptsRFC2046Variants(t *testing.T) {
	for name, body := range map[string]string{
		"blank preamble":         "\r\n \r\n--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b--",
		"padding after boundary": "--b \t\r\nContent-Type: application/json\r\n\r\n{}\r\n--b--  \r\n\r\n",
		"no part headers":        "--b\r\n\r\n{}\r\n--b--\r\n",
		"matching length":        "--b\r\nContent-Length: 2\r\ncontent-type: application/json\r\n\r\n{}\r\n--b--\r\n",
		"identity encoding":      "--b\r\nContent-Transfer-Encoding: BINARY\r\n\r\n{}\r\n--b--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			parts, err := parseMultipart([]byte(body), "multipart/related; boundary=b")
			if err != nil || len(parts) != 1 || string(parts[0].data) != "{}" {
				t.Fatalf("parseMultipart = %v, %v", parts, err)
			}
		})
	}
}

func TestParseMultipartAcceptsPlainParams(t *testing.T) {
	body := multipartBody("b1", [2]string{"Content-Type: application/json; charset=UTF-8", "{}"}, [2]string{"Content-Type: text/plain", "x"})
	for _, ct := range []string{
		"multipart/related; boundary=b1",
		`Multipart/Related; type="application/json"; Boundary="b1";`,
		"multipart/related ;boundary = b1",
	} {
		if parts, err := parseMultipart([]byte(body), ct); err != nil || len(parts) != 2 {
			t.Errorf("parseMultipart(%q) = %d parts, %v", ct, len(parts), err)
		}
	}
}

func TestSelectPart(t *testing.T) {
	parts := []mimePart{{contentType: "text/plain", data: []byte("first")}, {contentType: "application/json", data: []byte("{}")}}
	if _, err := selectPart(parts, step(filter.StepMultipart, "application/json")); err == nil {
		t.Fatal("prefix form skipped a non-matching first part")
	}
	if got, err := selectPart(parts, indexStep(1)); err != nil || string(got.data) != "{}" {
		t.Fatalf("index 1 = %q, %v", got.data, err)
	}
	if _, err := selectPart(parts, indexStep(2)); err == nil {
		t.Fatal("index out of range accepted")
	}
	if got, err := selectPart(parts, step(filter.StepMultipart, "TEXT/")); err != nil || string(got.data) != "first" {
		t.Fatalf("case-insensitive prefix = %q, %v", got.data, err)
	}
}

func TestIsFormBody(t *testing.T) {
	for ct, want := range map[string]bool{
		"application/x-www-form-urlencoded":                true,
		"Application/X-WWW-Form-Urlencoded; charset=UTF-8": true,
		"multipart/form-data; boundary=x":                  true,
		"multipart/form-data; boundary=x; boundary=y":      true,
		"application/json":                                 false,
		"multipart/related; boundary=x":                    false,
		"":                                                 false,
	} {
		if got := isFormBody(ct); got != want {
			t.Errorf("isFormBody(%q) = %v, want %v", ct, got, want)
		}
	}
}

// The send route of the gws client: the captured body passes the multipart
// checks and is judged on its recipients, while part header blocks that a
// stricter parser would end elsewhere are denied before any recipient is
// read.
func TestEvaluateGWSSendUpload(t *testing.T) {
	send := gmail + "/upload/gmail/v1/users/me/messages/send?uploadType=multipart"
	ct := withContentType(gwsSendContentType)
	h := newHarness(t, "send-eclipsesource", nil)

	allowed := strings.Replace(gwsSendBody, "Bcc: <someone@gmail.com>\r\n", "", 1)
	d := h.eval(t, newRequest("POST", send, allowed, ct))
	mustAllow(t, d)
	if d.Op != "gmail.messages.send-upload" {
		t.Fatalf("op = %s", d.Op)
	}
	d.Finish(200)
	mustDeny(t, h.eval(t, newRequest("POST", send, gwsSendBody, ct)), ReasonConstraint,
		`gmail.messages.send-upload: recipients "someone@gmail.com" is not allowed by preset send-eclipsesource`)

	for name, body := range map[string]string{
		"whitespace-only line": "--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: message/rfc822\r\n \r\n" +
			"To: evil@evil.example\r\n\r\nTo: ok@eclipsesource.com\r\nSubject: hi\r\n\r\nbody\r\n--b--\r\n",
		"bare LF blank line": "--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: message/rfc822\r\n\n" +
			"To: ok@eclipsesource.com\r\n\r\nTo: evil@evil.example\r\n\r\nbody\r\n--b--\r\n",
		"recipient in the part headers": "--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: message/rfc822\r\n" +
			"To: evil@evil.example\r\n\r\nTo: ok@eclipsesource.com\r\n\r\nbody\r\n--b--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			mustDeny(t, h.eval(t, newRequest("POST", send, body, withContentType("multipart/related; boundary=b"))), ReasonBody, "multipart part")
			if h.tokens.calls != 1 {
				t.Fatalf("token calls = %d, want only the allowed send", h.tokens.calls)
			}
		})
	}
}
