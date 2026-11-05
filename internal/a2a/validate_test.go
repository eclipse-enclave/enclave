// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCheckTextParts(t *testing.T) {
	text := "x"
	cases := map[string]struct {
		parts []Part
		code  int
	}{
		"text":           {[]Part{TextPart("a"), {Text: &text, MediaType: "text/plain"}}, 0},
		"empty text":     {[]Part{TextPart("")}, 0},
		"no parts":       {nil, CodeInvalidParams},
		"no member":      {[]Part{{MediaType: "text/plain"}}, CodeInvalidParams},
		"two members":    {[]Part{{Text: &text, URL: "https://x"}}, CodeInvalidParams},
		"raw":            {[]Part{TextPart("a"), {Raw: "aGk=", MediaType: "image/png"}}, CodeContentTypeNotSupported},
		"url":            {[]Part{{URL: "https://x"}}, CodeContentTypeNotSupported},
		"data":           {[]Part{{Data: json.RawMessage(`{"a":1}`)}}, CodeContentTypeNotSupported},
		"data JSON null": {[]Part{{Data: json.RawMessage(`null`)}}, CodeContentTypeNotSupported},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := CheckTextParts(tc.parts)
			switch {
			case tc.code == 0 && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tc.code != 0 && (err == nil || err.Code != tc.code):
				t.Fatalf("want %d, got %v", tc.code, err)
			}
		})
	}
}

// Spec 6.7 sends a raw file part next to text; v1 refuses it.
func TestSpecFileRequestRefused(t *testing.T) {
	var req SendMessageRequest
	if err := json.Unmarshal(readFixture(t, "spec-6.7-file-request.json"), &req); err != nil {
		t.Fatal(err)
	}
	if p := req.Message.Parts[1]; p.Raw == "" || p.Filename != "input_image.png" || p.MediaType != "image/png" {
		t.Fatalf("raw part decoded wrong: %+v", p)
	}
	if err := req.Message.Validate(); err == nil || err.Code != CodeContentTypeNotSupported {
		t.Fatalf("want -32005, got %v", err)
	}

	// Spec 6.8 asks for JSON output through part metadata but sends text: accepted.
	var structured SendMessageRequest
	if err := json.Unmarshal(readFixture(t, "spec-6.8-structured-request.json"), &structured); err != nil {
		t.Fatal(err)
	}
	if err := structured.Message.Validate(); err != nil {
		t.Fatalf("spec 6.8 request should validate: %v", err)
	}
}

func TestMessageValidate(t *testing.T) {
	ok := Message{MessageID: "m", Role: RoleUser, Parts: []Part{TextPart("hi")}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]Message{
		"no id":       {Role: RoleUser, Parts: []Part{TextPart("hi")}},
		"no role":     {MessageID: "m", Parts: []Part{TextPart("hi")}},
		"unspecified": {MessageID: "m", Role: "ROLE_UNSPECIFIED", Parts: []Part{TextPart("hi")}},
		"0.3 role":    {MessageID: "m", Role: "user", Parts: []Part{TextPart("hi")}},
		"no parts":    {MessageID: "m", Role: RoleAgent},
	} {
		if err := m.Validate(); err == nil || err.Code != CodeInvalidParams {
			t.Errorf("%s: want -32602, got %v", name, err)
		}
	}
}

func TestCheckVersion(t *testing.T) {
	for value, wantOK := range map[string]bool{"1.0": true, " 1.0 ": true, "": false, "0.3": false, "1.0.1": false, "1": false, "1.1": false} {
		h := http.Header{}
		if value != "" {
			h.Set(HeaderVersion, value)
		}
		err := CheckVersion(h)
		if wantOK != (err == nil) {
			t.Errorf("CheckVersion(%q) = %v", value, err)
		}
		if err != nil && err.Code != CodeVersionNotSupported {
			t.Errorf("CheckVersion(%q) code %d", value, err.Code)
		}
	}
	h := http.Header{}
	h.Set("a2a-version", "1.0")
	if err := CheckVersion(h); err != nil {
		t.Fatalf("header names are case-insensitive: %v", err)
	}
}

func TestCheckExtensions(t *testing.T) {
	cases := map[string]struct {
		values []string
		ok     bool
	}{
		"exact":         {[]string{ExtensionURI}, true},
		"listed":        {[]string{"https://example.com/x/v1, " + ExtensionURI}, true},
		"second header": {[]string{"https://example.com/x/v1", ExtensionURI}, true},
		"missing":       {nil, false},
		"other":         {[]string{"https://example.com/x/v1"}, false},
		"prefix only":   {[]string{ExtensionURI + "/extra"}, false},
	}
	for name, tc := range cases {
		h := http.Header{}
		for _, v := range tc.values {
			h.Add(HeaderExtensions, v)
		}
		err := CheckExtensions(h)
		if tc.ok != (err == nil) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && err.Code != CodeExtensionSupportRequired {
			t.Errorf("%s: code %d", name, err.Code)
		}
	}
}

func TestSetProtocolHeaders(t *testing.T) {
	h := http.Header{}
	SetProtocolHeaders(h)
	if CheckVersion(h) != nil || CheckExtensions(h) != nil {
		t.Fatalf("headers %v do not pass the checks", h)
	}
}

func TestEnclaveMeta(t *testing.T) {
	if _, ok, err := EnclaveMetaFrom(nil); ok || err != nil {
		t.Fatalf("absent: ok=%v err=%v", ok, err)
	}
	pos := 2
	md := WithEnclaveMeta(map[string]any{"other": 1}, EnclaveMeta{QueuePosition: &pos})
	if md["other"] != 1 {
		t.Fatal("WithEnclaveMeta must keep other keys")
	}
	got, ok, err := EnclaveMetaFrom(md)
	if err != nil || !ok || got.QueuePosition == nil || *got.QueuePosition != 2 {
		t.Fatalf("struct value: %+v ok=%v err=%v", got, ok, err)
	}
	if got, ok, err := EnclaveMetaFrom(map[string]any{ExtensionURI: &EnclaveMeta{Retry: true}}); err != nil || !ok || !got.Retry {
		t.Fatalf("pointer value: %+v ok=%v err=%v", got, ok, err)
	}

	// A JSON round trip turns the struct into a generic map.
	data, err := json.Marshal(WithEnclaveMeta(nil, EnclaveMeta{Termination: TerminationConfirmed, Reason: ReasonDeadline}))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"https://enclave.eclipse.dev/a2a/ext/v1":{"termination":"confirmed","reason":"deadline"}}` {
		t.Fatalf("encoded %s", data)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got, ok, err = EnclaveMetaFrom(decoded)
	if err != nil || !ok || got.Termination != TerminationConfirmed || got.Reason != ReasonDeadline {
		t.Fatalf("decoded: %+v ok=%v err=%v", got, ok, err)
	}

	for name, raw := range map[string]string{
		"not object":     `"x"`,
		"null":           `null`,
		"array":          `[]`,
		"bad type":       `{"retry":"yes"}`,
		"termination":    `{"termination":"maybe"}`,
		"reason":         `{"reason":"crash"}`,
		"queue position": `{"queuePosition":-1}`,
	} {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := EnclaveMetaFrom(map[string]any{ExtensionURI: v}); !ok || err == nil {
			t.Errorf("%s: want error, ok=%v err=%v", name, ok, err)
		}
	}
}
