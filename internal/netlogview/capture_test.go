// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlogview

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"enclave/internal/netcapture"
)

func captureFixture() CaptureView {
	return CaptureView{
		Path: "/state/capture/enclave-demo-1/0123456789abcdef.json",
		Record: netcapture.Record{
			SchemaVersion: netcapture.SchemaVersion,
			Kind:          netcapture.Kind,
			ID:            "0123456789abcdef",
			Session:       "enclave-demo-1",
			Timestamp:     "2026-08-13T12:04:31.412Z",
			DurationMS:    1250,
			Request: netcapture.Request{
				Method:  "POST",
				URL:     "https://api.example.com/v1/messages?beta=true",
				Proto:   "HTTP/1.1",
				Headers: http.Header{"X-Api-Key": {"ENCLAVE_SECRET_abc"}, "Content-Type": {"application/json"}},
				Body:    &netcapture.Body{File: "0123456789abcdef.request.body", Bytes: 15, StoredBytes: 15},
			},
			Response: &netcapture.Response{
				Status:  200,
				Proto:   "HTTP/1.1",
				Headers: http.Header{"Content-Type": {"text/event-stream"}},
				Body:    &netcapture.Body{File: "0123456789abcdef.response.body", Bytes: 64, StoredBytes: 13, Truncated: true},
			},
		},
		Request:  CapturedBody{Data: []byte(`{"prompt":"hi"}`), Path: "/state/capture/enclave-demo-1/0123456789abcdef.request.body"},
		Response: CapturedBody{Data: []byte("data: first\n\n"), Path: "/state/capture/enclave-demo-1/0123456789abcdef.response.body"},
	}
}

func renderCapture(t *testing.T, view CaptureView) string {
	t.Helper()
	var out strings.Builder
	if err := WriteCapture(&out, view, humanOptions()); err != nil {
		t.Fatalf("WriteCapture() error = %v", err)
	}
	return out.String()
}

func TestWriteCaptureRendersBothMessages(t *testing.T) {
	out := renderCapture(t, captureFixture())
	for _, want := range []string{
		"CAPTURE  0123456789abcdef  enclave-demo-1  2026-08-13 12:04:31  1.25s",
		"POST https://api.example.com/v1/messages?beta=true HTTP/1.1\nContent-Type: application/json\nX-Api-Key: ENCLAVE_SECRET_abc\n\n{\"prompt\":\"hi\"}\n",
		"HTTP/1.1 200 OK\nContent-Type: text/event-stream\n\ndata: first\n",
		"(request body, 15 B: /state/capture/enclave-demo-1/0123456789abcdef.request.body)",
		"(response body, 13 B of 64 B, truncated: /state/capture/enclave-demo-1/0123456789abcdef.response.body)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "POST ") > strings.Index(out, "HTTP/1.1 200") {
		t.Fatalf("response printed before request:\n%s", out)
	}
}

func TestWriteCaptureNeutralizesTerminalControl(t *testing.T) {
	view := captureFixture()
	view.Record.Request.Headers = http.Header{"X-Evil": {"\x1b]0;title\x07"}}
	view.Request.Data = []byte("line\x1b[2Jcleared\r\nnext\tcol\n")
	view.Request.Path = "/state/\x1b[31mred.request.body"
	view.Path = "/state/\x1b[2J.json"
	out := renderCapture(t, view)
	if strings.ContainsAny(out, "\x1b\x07\r") {
		t.Fatalf("output carries control characters: %q", out)
	}
	if !strings.Contains(out, "line?[2Jcleared\nnext\tcol\n") {
		t.Fatalf("body lost its line structure: %q", out)
	}
}

func TestWriteCaptureDescribesBodiesItCannotShow(t *testing.T) {
	view := captureFixture()
	view.Record.Response.Headers = http.Header{"Content-Encoding": {"br"}}
	view.Response.Data = []byte{0x8b, 0x02, 0x80}
	out := renderCapture(t, view)
	if !strings.Contains(out, "response body br-encoded, not shown") {
		t.Fatalf("encoded body not described:\n%s", out)
	}
	if !strings.Contains(out, "(decode with: brotli -dc '/state/capture/enclave-demo-1/0123456789abcdef.response.body')") {
		t.Fatalf("no decode hint for br:\n%s", out)
	}

	// The capture leaves a body encoded twice as relayed, so it is described
	// rather than decoded once into something unreadable.
	view = captureFixture()
	view.Record.Response.Headers = http.Header{"Content-Encoding": {"gzip", "gzip"}}
	out = renderCapture(t, view)
	if !strings.Contains(out, "response body gzip, gzip-encoded, not shown") || strings.Contains(out, "data: first") {
		t.Fatalf("repeated encoding not described:\n%s", out)
	}

	view = captureFixture()
	view.Request.Data = []byte{0x00, 0xff, 0x10}
	out = renderCapture(t, view)
	if !strings.Contains(out, "request body binary") {
		t.Fatalf("binary body not described:\n%s", out)
	}

	view = captureFixture()
	view.Response = CapturedBody{Path: "/gone", Err: errors.New("open /gone: no such file or directory")}
	out = renderCapture(t, view)
	if !strings.Contains(out, "response body not shown, 13 B of 64 B, truncated: open /gone: no such file or directory") {
		t.Fatalf("missing body not reported:\n%s", out)
	}
}

func TestWriteCaptureReportsRelayFailure(t *testing.T) {
	view := captureFixture()
	view.Record.Response = nil
	view.Record.Error = "dial tcp: connection refused"
	out := renderCapture(t, view)
	if !strings.Contains(out, "Relay failed: dial tcp: connection refused") {
		t.Fatalf("relay failure not reported:\n%s", out)
	}
}

// The size cap cuts bytes, not characters: a text body cut inside a multibyte
// character still shows everything before it.
func TestWriteCaptureShowsTextCutInsideACharacter(t *testing.T) {
	view := captureFixture()
	text := strings.Repeat("a", 20) + "é"
	cut := []byte(text)[:21]
	view.Record.Response.Body = &netcapture.Body{File: "0123456789abcdef.response.body", Bytes: int64(len(text)), StoredBytes: int64(len(cut)), Truncated: true}
	view.Response.Data = cut
	out := renderCapture(t, view)
	if !strings.Contains(out, strings.Repeat("a", 20)+"\n") || strings.Contains(out, "binary") {
		t.Fatalf("truncated text rendered as binary:\n%s", out)
	}

	// An untruncated body ending in an invalid sequence is still binary.
	view.Record.Response.Body.Truncated = false
	out = renderCapture(t, view)
	if !strings.Contains(out, "response body binary") {
		t.Fatalf("invalid complete body not reported as binary:\n%s", out)
	}
}

func TestTrimPartialRune(t *testing.T) {
	euro := []byte("€") // three bytes
	for _, tc := range []struct {
		in, want []byte
	}{
		{[]byte("abc"), []byte("abc")},
		{append([]byte("ab"), euro...), append([]byte("ab"), euro...)},
		{append([]byte("ab"), euro[:1]...), []byte("ab")},
		{append([]byte("ab"), euro[:2]...), []byte("ab")},
		{[]byte{}, []byte{}},
	} {
		if got := trimPartialRune(tc.in); !bytes.Equal(got, tc.want) {
			t.Fatalf("trimPartialRune(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A body the capture decoded is shown as stored, even though the response
// headers still name the encoding it was relayed in.
func TestWriteCaptureShowsBodiesDecodedAtCapture(t *testing.T) {
	view := captureFixture()
	view.Record.Response.Headers = http.Header{"Content-Encoding": {"gzip"}}
	view.Record.Response.Body = &netcapture.Body{File: "0123456789abcdef.response.body", Bytes: 31, StoredBytes: 16, DecodedFrom: "gzip"}
	view.Response.Data = []byte(`{"decoded":true}`)
	out := renderCapture(t, view)
	if !strings.Contains(out, `{"decoded":true}`) {
		t.Fatalf("decoded body not shown:\n%s", out)
	}
	if !strings.Contains(out, "(response body, 16 B decoded from 31 B of gzip: /state/capture/enclave-demo-1/0123456789abcdef.response.body)") {
		t.Fatalf("decoding not described:\n%s", out)
	}
	if strings.Contains(out, "not decodable") {
		t.Fatalf("stored body decoded a second time:\n%s", out)
	}
}
