// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func readAll(t *testing.T, r *SSEReader) ([]Event, error) {
	t.Helper()
	var events []Event
	for {
		ev, err := r.Next()
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
}

// Spec 9.4.2: one data line per event holding a JSON-RPC response with a StreamResponse result.
func TestSSESpecStreamFixture(t *testing.T) {
	r := NewSSEReader(bytes.NewReader(readFixture(t, "spec-9.4.2-stream.sse")), 1<<16)
	events, err := readAll(t, r)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	var kinds []string
	for _, ev := range events {
		if ev.Event != "" || ev.ID != "" {
			t.Fatalf("A2A events carry no event name or id: %+v", ev)
		}
		var resp Response
		if err := json.Unmarshal(ev.Data, &resp); err != nil {
			t.Fatal(err)
		}
		if string(resp.ID) != "1" || resp.Error != nil {
			t.Fatalf("response %+v", resp)
		}
		result, err := DecodeResult(resp.Result)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case result.Task != nil:
			kinds = append(kinds, "task")
		case result.ArtifactUpdate != nil:
			if got := *result.ArtifactUpdate.Artifact.Parts[0].Text; got != "# Climate Change Report\n\n" {
				t.Fatalf("artifact text %q", got)
			}
			kinds = append(kinds, "artifactUpdate")
		case result.StatusUpdate != nil:
			if !result.StatusUpdate.Status.State.Terminal() {
				t.Fatal("last event should be terminal")
			}
			kinds = append(kinds, "statusUpdate")
		}
	}
	if want := []string{"task", "artifactUpdate", "statusUpdate"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("order %v", kinds)
	}
}

func TestSSEWriterOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewSSEWriter(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Type"); got != SSEContentType {
		t.Fatalf("Content-Type %q", got)
	}
	if err := w.WriteEvent(Event{Event: ControlEventHello, Data: []byte(`{"epoch":"e"}`)}); err != nil {
		t.Fatal(err)
	}
	if !rec.Flushed {
		t.Fatal("WriteEvent must flush")
	}
	if err := w.WriteComment("keepalive"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteEvent(Event{ID: "7", Retry: 1500, Data: []byte("a\nb\r\nc\rd")}); err != nil {
		t.Fatal(err)
	}
	want := "event: hello\ndata: {\"epoch\":\"e\"}\n\n" +
		": keepalive\n\n" +
		"id: 7\nretry: 1500\ndata: a\ndata: b\ndata: c\ndata: d\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body:\n%q\nwant:\n%q", got, want)
	}
}

func TestSSEWriterRejects(t *testing.T) {
	w, err := NewSSEWriter(httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	for name, ev := range map[string]Event{
		"id newline":      {ID: "a\nb", Data: []byte("x")},
		"id NUL":          {ID: "a\x00", Data: []byte("x")},
		"event CR":        {Event: "a\rb", Data: []byte("x")},
		"negative retry":  {Retry: -1, Data: []byte("x")},
		"event injection": {Event: "x\ndata: forged", Data: []byte("x")},
	} {
		if err := w.WriteEvent(ev); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if err := w.WriteComment("a\nb"); err == nil {
		t.Error("comment with newline: want error")
	}
}

type noFlushWriter struct{ h http.Header }

func (w *noFlushWriter) Header() http.Header         { return w.h }
func (w *noFlushWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *noFlushWriter) WriteHeader(int)             {}

type wrappedWriter struct{ http.ResponseWriter }

func (w wrappedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestNewSSEWriterNeedsFlusher(t *testing.T) {
	if _, err := NewSSEWriter(&noFlushWriter{h: http.Header{}}); err == nil {
		t.Fatal("want error for a writer that cannot flush")
	}
	if _, err := NewSSEWriter(wrappedWriter{&noFlushWriter{h: http.Header{}}}); err == nil {
		t.Fatal("want error for a wrapped writer that cannot flush")
	}
	rec := httptest.NewRecorder()
	w, err := NewSSEWriter(wrappedWriter{rec})
	if err != nil {
		t.Fatalf("wrapped flusher: %v", err)
	}
	if err := w.WriteEvent(Event{Data: []byte("x")}); err != nil || !rec.Flushed {
		t.Fatalf("wrapped flush: %v flushed=%v", err, rec.Flushed)
	}
}

// Cases follow the parsing rules of the WHATWG HTML event-stream section 9.2.6.
func TestSSEReaderParsing(t *testing.T) {
	cases := []struct {
		name   string
		stream string
		want   []Event
	}{
		{"LF", "data: a\n\n", []Event{{Data: []byte("a")}}},
		{"CRLF", "data: a\r\n\r\n", []Event{{Data: []byte("a")}}},
		{"CR", "data: a\r\r", []Event{{Data: []byte("a")}}},
		{"mixed terminators", "data: a\rdata: b\r\ndata: c\n\n", []Event{{Data: []byte("a\nb\nc")}}},
		{"BOM", "\xEF\xBB\xBFdata: a\n\n", []Event{{Data: []byte("a")}}},
		{"one space stripped", "data:  a\n\n", []Event{{Data: []byte(" a")}}},
		{"no space", "data:a\n\n", []Event{{Data: []byte("a")}}},
		{"field without colon", "data\ndata\n\n", []Event{{Data: []byte("\n")}}},
		{"comments ignored", ": ping\n:\ndata: a\n: mid\n\n", []Event{{Data: []byte("a")}}},
		{"unknown field", "foo: bar\ndata: a\n\n", []Event{{Data: []byte("a")}}},
		{"empty data dropped", "event: x\n\ndata: a\n\n", []Event{{Data: []byte("a")}}},
		{"event type resets", "event: x\ndata: 1\n\ndata: 2\n\n", []Event{
			{Event: "x", Data: []byte("1")}, {Data: []byte("2")},
		}},
		{"id persists", "id: 1\ndata: a\n\ndata: b\n\nid\ndata: c\n\n", []Event{
			{ID: "1", Data: []byte("a")}, {ID: "1", Data: []byte("b")}, {Data: []byte("c")},
		}},
		{"id with NUL ignored", "id: 1\ndata: a\n\nid: 2\x00\ndata: b\n\n", []Event{
			{ID: "1", Data: []byte("a")}, {ID: "1", Data: []byte("b")},
		}},
		{"retry", "retry: 3000\ndata: a\n\nretry: 3x\ndata: b\n\n", []Event{
			{Retry: 3000, Data: []byte("a")}, {Data: []byte("b")},
		}},
		{"trailing blank lines", "\n\n\ndata: a\n\n\n\n", []Event{{Data: []byte("a")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events, err := readAll(t, NewSSEReader(strings.NewReader(tc.stream), 1024))
			if !errors.Is(err, io.EOF) {
				t.Fatalf("want io.EOF, got %v", err)
			}
			if !reflect.DeepEqual(events, tc.want) {
				t.Fatalf("events %+v, want %+v", events, tc.want)
			}
		})
	}
}

func TestSSEReaderTruncation(t *testing.T) {
	for name, stream := range map[string]string{
		"no blank line":  "data: a\n",
		"partial line":   "data: a\n\ndata: b",
		"event pending":  "event: x\n",
		"partial event:": "data: a\n\nev",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readAll(t, NewSSEReader(strings.NewReader(stream), 1024))
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("want io.ErrUnexpectedEOF, got %v", err)
			}
		})
	}
	for name, stream := range map[string]string{
		"empty":            "",
		"comment only":     ": ping\n",
		"trailing comment": "data: a\n\n: ping",
		"id only":          "id: 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readAll(t, NewSSEReader(strings.NewReader(stream), 1024))
			if !errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("want io.EOF, got %v", err)
			}
		})
	}
}

func TestSSEReaderLimit(t *testing.T) {
	r := NewSSEReader(strings.NewReader("data: 12345\n\ndata: "+strings.Repeat("x", 64)+"\n\ndata: ok\n\n"), 32)
	ev, err := r.Next()
	if err != nil || string(ev.Data) != "12345" {
		t.Fatalf("first event %+v %v", ev, err)
	}
	if _, err := r.Next(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("want ErrEventTooLarge, got %v", err)
	}
	if _, err := r.Next(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatal("the error must be sticky")
	}

	// Many short lines add up to one oversized event.
	r = NewSSEReader(strings.NewReader(strings.Repeat("data: x\n", 10)+"\n"), 32)
	if _, err := r.Next(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("want ErrEventTooLarge for many lines, got %v", err)
	}

	// The bound is per event, not per stream.
	r = NewSSEReader(strings.NewReader(strings.Repeat("data: 0123456789\n\n", 100)), 32)
	events, err := readAll(t, r)
	if !errors.Is(err, io.EOF) || len(events) != 100 {
		t.Fatalf("got %d events, %v", len(events), err)
	}
}

func TestSSEOverHTTP(t *testing.T) {
	sent := []Event{
		{Event: ControlEventHello, Data: []byte(`{"epoch":"0123456789abcdef","revision":3,"groupGeneration":"g"}`)},
		{Event: ControlEventOrchestrator, Data: []byte(`{"member":"o","generation":"x","connected":true,"since":"t"}`)},
		{Data: []byte("multi\nline")},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		w, err := NewSSEWriter(rw)
		if err != nil {
			t.Error(err)
			return
		}
		for i, ev := range sent {
			if err := w.WriteEvent(ev); err != nil {
				t.Error(err)
			}
			if i == 0 {
				_ = w.WriteComment("keepalive")
			}
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Content-Type"); got != SSEContentType {
		t.Fatalf("Content-Type %q", got)
	}
	got, err := readAll(t, NewSSEReader(resp.Body, 1024))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
	if !reflect.DeepEqual(got, sent) {
		t.Fatalf("got %+v", got)
	}
}

func FuzzSSEReader(f *testing.F) {
	f.Add([]byte("data: a\n\n"))
	f.Add([]byte("\xEF\xBB\xBFid: 1\r\nevent: x\r\ndata: a\rdata: b\r\n\r\n"))
	f.Add([]byte(": c\nretry: 10\ndata\n\n"))
	f.Add(readFixture(f, "spec-9.4.2-stream.sse"))
	f.Fuzz(func(t *testing.T, stream []byte) {
		const limit = 256
		r := NewSSEReader(bytes.NewReader(stream), limit)
		for i := 0; i <= len(stream); i++ {
			ev, err := r.Next()
			if err != nil {
				return
			}
			if len(ev.Data) > limit {
				t.Fatalf("event data %d bytes exceeds limit", len(ev.Data))
			}
			// Writing a parsed event and parsing it again must reproduce it.
			rec := httptest.NewRecorder()
			w, err := NewSSEWriter(rec)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.WriteEvent(ev); err != nil {
				t.Fatalf("rewrite %+v: %v", ev, err)
			}
			again, err := NewSSEReader(rec.Body, 4*limit).Next()
			if err != nil {
				t.Fatalf("reparse %q: %v", rec.Body.String(), err)
			}
			if again.Event != ev.Event || again.ID != ev.ID || again.Retry != ev.Retry || !bytes.Equal(again.Data, ev.Data) {
				t.Fatalf("rewrite changed the event: %+v -> %+v", ev, again)
			}
		}
		t.Fatal("reader produced more events than input bytes")
	})
}
