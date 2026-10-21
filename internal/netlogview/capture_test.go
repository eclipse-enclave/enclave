// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlogview

import (
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

	// A body encoded twice is described the same way.
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

// A body the capture decoded is shown as stored, even though the response
// headers still name the encoding it was relayed in.
func websocketFixture() CaptureView {
	view := captureFixture()
	view.Record.Request = netcapture.Request{Method: "GET", URL: "https://api.example.com/responses", Proto: "HTTP/1.1", Headers: http.Header{"Upgrade": {"websocket"}}}
	view.Record.Response = &netcapture.Response{Status: http.StatusSwitchingProtocols, Proto: "HTTP/1.1", Headers: http.Header{"Upgrade": {"websocket"}}}
	view.Record.WebSocket = &netcapture.WebSocket{File: "0123456789abcdef.messages.jsonl", Compression: "permessage-deflate", Messages: 5, StoredMessages: 4, Truncated: true, Error: "server: invalid WebSocket frame: reserved opcode 0x3"}
	view.Request, view.Response = CapturedBody{}, CapturedBody{}
	view.Messages = CapturedMessages{
		Path: "/state/capture/enclave-demo-1/0123456789abcdef.messages.jsonl",
		List: []netcapture.Message{
			{Timestamp: "2026-08-13T12:04:31.5Z", From: "client", Type: "text", Bytes: 20, StoredBytes: 26, DecodedFrom: "permessage-deflate", Text: `{"type":"response.create"}`},
			{Timestamp: "2026-08-13T12:04:32Z", From: "server", Type: "text", Bytes: 9, StoredBytes: 9, Text: "evil\x1b[2Jx"},
			{Timestamp: "2026-08-13T12:04:33Z", From: "server", Type: "binary", Bytes: 3, StoredBytes: 3, Base64: []byte{1, 2, 3}},
			{Timestamp: "2026-08-13T12:04:34Z", From: "client", Type: "close", Bytes: 6, StoredBytes: 6, Code: 1000, Text: "done"},
		},
	}
	return view
}

func TestWriteCaptureRendersWebSocketMessages(t *testing.T) {
	out := renderCapture(t, websocketFixture())
	for _, want := range []string{
		"(WebSocket, 5 messages, 4 stored, permessage-deflate decoded, truncated: /state/capture/enclave-demo-1/0123456789abcdef.messages.jsonl)",
		"(frames no longer followed: server: invalid WebSocket frame: reserved opcode 0x3)",
		"> client 12:04:31.500 text (26 B decoded from 20 B of permessage-deflate)\n{\"type\":\"response.create\"}\n",
		"< server 12:04:32.000 text (9 B)\nevil?[2Jx\n",
		"< server 12:04:33.000 binary (3 B, binary)\n",
		"> client 12:04:34.000 close 1000 (6 B)\ndone\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "not captured") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestWriteCaptureRendersAnOpenWebSocket(t *testing.T) {
	view := websocketFixture()
	view.Record.WebSocket = &netcapture.WebSocket{File: "0123456789abcdef.messages.jsonl", Open: true}
	view.Messages.List = view.Messages.List[:2]
	out := renderCapture(t, view)
	if !strings.Contains(out, "  open\n") || !strings.Contains(out, "(WebSocket still open, 2 messages so far: ") {
		t.Fatalf("open socket not described:\n%s", out)
	}

	view.Messages = CapturedMessages{Path: "/gone", Err: errors.New("open /gone: no such file or directory")}
	out = renderCapture(t, view)
	if !strings.Contains(out, "not shown: open /gone: no such file or directory") {
		t.Fatalf("unreadable messages not reported:\n%s", out)
	}
}

func TestWriteCaptureNotesAnOmittedResponseBody(t *testing.T) {
	view := captureFixture()
	view.Record.Response.Headers = http.Header{"Content-Encoding": {"br"}}
	view.Record.Response.Body = nil
	view.Record.Response.BodyOmitted = netcapture.OmittedEncoding
	view.Response = CapturedBody{}
	out := renderCapture(t, view)
	if !strings.Contains(out, "(response body not stored: unexpected content encoding)") {
		t.Fatalf("omitted body not noted:\n%s", out)
	}
}

func TestWriteCaptureNotesAnOmittedRequestBody(t *testing.T) {
	view := captureFixture()
	view.Record.Request.Headers = http.Header{"Content-Encoding": {"gzip"}}
	view.Record.Request.Body = nil
	view.Record.Request.BodyOmitted = netcapture.OmittedEncoding
	view.Request = CapturedBody{}
	out := renderCapture(t, view)
	if !strings.Contains(out, "(request body not stored: unexpected content encoding)") {
		t.Fatalf("omitted request body not noted:\n%s", out)
	}
}
