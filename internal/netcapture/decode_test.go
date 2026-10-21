// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func compress(t *testing.T, encoding string, plain []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var writer io.WriteCloser
	switch encoding {
	case "gzip":
		writer = gzip.NewWriter(&buffer)
	case "deflate":
		writer = zlib.NewWriter(&buffer)
	case "raw-deflate":
		var err error
		if writer, err = flate.NewWriter(&buffer, flate.DefaultCompression); err != nil {
			t.Fatalf("flate writer: %v", err)
		}
	}
	if _, err := writer.Write(plain); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close compressor: %v", err)
	}
	return buffer.Bytes()
}

// captureEncodedResponse relays an encoded response body in small writes, the
// way io.Copy hands chunks to the capture.
func captureEncodedResponse(t *testing.T, store *Store, encoding string, encoded []byte) Record {
	t.Helper()
	capture := store.Begin(httptest.NewRequest(http.MethodGet, "http://api.example.com/echo", nil), "https")
	capture.Response(&http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Encoding": {encoding}}})
	sink := capture.ResponseBody()
	for rest := encoded; len(rest) > 0; {
		n := min(len(rest), 7)
		if _, err := sink.Write(rest[:n]); err != nil {
			t.Fatalf("write: %v", err)
		}
		rest = rest[n:]
	}
	id := capture.Finish(nil)
	record, err := Load(RecordPath(store.dir, id))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return record
}

// A released value echoed inside a compressed response is redacted like one in
// plain text, because the body is decoded before it is redacted and stored.
func TestStoreDecodesCompressedBodiesBeforeRedacting(t *testing.T) {
	plain := []byte(`{"echo":"Bearer sk-live-do-not-store","items":[1,2,3]}`)
	want := `{"echo":"Bearer ENCLAVE_PH","items":[1,2,3]}`
	for _, tc := range []struct{ header, compressor string }{
		{"gzip", "gzip"},
		{"x-gzip", "gzip"},
		{"deflate", "deflate"},
		{"deflate", "raw-deflate"},
	} {
		t.Run(tc.header+"/"+tc.compressor, func(t *testing.T) {
			store := openTestStore(t, Options{Secrets: []Secret{{Value: "sk-live-do-not-store", Placeholder: "ENCLAVE_PH"}}})
			encoded := compress(t, tc.compressor, plain)
			record := captureEncodedResponse(t, store, tc.header, encoded)
			body := record.Response.Body
			if body == nil || body.DecodedFrom != tc.header || body.Bytes != int64(len(encoded)) || body.Truncated {
				t.Fatalf("body = %+v, want decoded from %s, %d bytes relayed", body, tc.header, len(encoded))
			}
			if got := readBody(t, RecordPath(store.dir, record.ID), body); got != want {
				t.Fatalf("stored body = %q, want %q", got, want)
			}
			if record.Response.Headers.Get("Content-Encoding") != tc.header {
				t.Fatalf("headers = %v, want them as relayed", record.Response.Headers)
			}
		})
	}
}

func TestStoreMarksBodiesThatDoNotDecodeToTheEnd(t *testing.T) {
	store := openTestStore(t, Options{})
	encoded := compress(t, "gzip", []byte(strings.Repeat("stream ", 1000)))
	record := captureEncodedResponse(t, store, "gzip", encoded[:len(encoded)/2])
	if body := record.Response.Body; body == nil || !body.Truncated || body.DecodedFrom != "gzip" {
		t.Fatalf("cut-off gzip body = %+v, want it marked truncated", body)
	}

	record = captureEncodedResponse(t, store, "gzip", []byte("not gzip at all"))
	if body := record.Response.Body; body == nil || !body.Truncated || body.StoredBytes != 0 || body.Bytes != 15 {
		t.Fatalf("undecodable body = %+v, want 15 bytes relayed, nothing stored, truncated", body)
	}
}

// Decoding stops once the capture is full, so a small body cannot keep the
// gateway expanding it.
func TestStoreStopsDecodingAtTheCap(t *testing.T) {
	store := openTestStore(t, Options{MaxBodyBytes: 1024})
	encoded := compress(t, "gzip", make([]byte, 16<<20))
	started := time.Now()
	record := captureEncodedResponse(t, store, "gzip", encoded)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("capturing a 16 MiB expansion took %s", elapsed)
	}
	if body := record.Response.Body; body == nil || !body.Truncated || body.StoredBytes != 1024 || body.Bytes != int64(len(encoded)) {
		t.Fatalf("body = %+v, want 1 KiB stored of the expansion", body)
	}
}

func TestStoreDecodesCompressedRequestBodies(t *testing.T) {
	store := openTestStore(t, Options{})
	plain := `{"prompt":"hello"}`
	encoded := compress(t, "gzip", []byte(plain))
	req := httptest.NewRequest(http.MethodPost, "http://api.example.com/upload", bytes.NewReader(encoded))
	req.Header.Set("Content-Encoding", "gzip")
	capture := store.Begin(req, "https")
	if _, err := io.Copy(io.Discard, capture.RequestBody(req.Body)); err != nil {
		t.Fatalf("read request body: %v", err)
	}
	id := capture.Finish(nil)
	record, err := Load(RecordPath(store.dir, id))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	body := record.Request.Body
	if body == nil || body.DecodedFrom != "gzip" || body.Bytes != int64(len(encoded)) || body.Truncated {
		t.Fatalf("request body = %+v", body)
	}
	if got := readBody(t, RecordPath(store.dir, id), body); got != plain {
		t.Fatalf("stored request body = %q", got)
	}
}

func TestStoreKeepsOtherEncodingsAsRelayed(t *testing.T) {
	for _, header := range []http.Header{
		{"Content-Encoding": {"br"}},
		{"Content-Encoding": {"gzip, br"}},
		{"Content-Encoding": {"gzip", "gzip"}},
	} {
		store := openTestStore(t, Options{})
		capture := store.Begin(httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil), "https")
		capture.Response(&http.Response{StatusCode: http.StatusOK, Header: header})
		_, _ = capture.ResponseBody().Write([]byte("\x8b\x02\x80raw"))
		id := capture.Finish(nil)
		record, err := Load(RecordPath(store.dir, id))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if body := record.Response.Body; body == nil || body.DecodedFrom != "" || body.StoredBytes != 6 {
			t.Fatalf("%v body = %+v, want it stored as relayed", header, body)
		}
	}
}
