// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"bufio"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// rawTLS writes raw bytes over a TLS connection with server name sni to the
// HTTPS dispatch listener and returns the response, so a test can send a
// request target no Go client would produce.
func (h *filterHarness) rawTLS(t *testing.T, sni string, raw string) response {
	t.Helper()
	conn, err := tls.Dial("tcp", h.httpsAddr, &tls.Config{ServerName: sni, RootCAs: h.gatewayCA, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: string(body)}
}

// Regression: the proxy forwards the raw query, so with "x=#&format=raw" a
// server that reads "#" as the start of a fragment drops format=raw, while
// the filter matched the route on it.
func TestFilterDeniesRawFragmentInRequestTarget(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	for i, target := range []string{
		"/gmail/v1/users/me/messages/abc?x=#&format=raw",
		"/gmail/v1/users/me/messages/abc#?format=raw",
		"/gmail/v1/users/me/messages/abc?$alt=media",
	} {
		raw := "GET " + target + " HTTP/1.1\r\nHost: " + gmailHost + "\r\nAuthorization: Bearer " + filterPlaceholder + "\r\nConnection: close\r\n\r\n"
		got := h.rawTLS(t, gmailHost, raw)
		assertFilterDenial(t, got, h.lastHTTPEvent(t, i+1), http.StatusForbidden, "ambiguous")
	}
	if reqs := h.google.apiRequests(); len(reqs) != 0 {
		t.Fatalf("upstream saw %+v", reqs)
	}
	if h.google.refreshCount() != 0 {
		t.Fatal("a token was minted for a denied request")
	}
}

// Regression: a part header block that Go reads differently from a strict
// CRLF parser hid a recipient from the send check. The proxy now denies it
// before anything reaches the upstream, and the captured gws body passes.
func TestFilterDeniesAmbiguousMultipartSend(t *testing.T) {
	h := newFilterHarness(t, "send-eclipsesource")
	send := "https://" + gmailHost + "/upload/gmail/v1/users/me/messages/send?uploadType=multipart"
	for i, body := range []string{
		"--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: message/rfc822\r\n \r\nTo: evil@evil.example\r\n\r\n" +
			"To: ok@eclipsesource.com\r\nSubject: hi\r\n\r\nbody\r\n--b--\r\n",
		"--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: message/rfc822\r\n\n" +
			"To: ok@eclipsesource.com\r\n\r\nTo: evil@evil.example\r\n\r\nbody\r\n--b--\r\n",
	} {
		got := h.do(t, http.MethodPost, send, body, withPlaceholder(), header("Content-Type", "multipart/related; boundary=b"))
		assertFilterDenial(t, got, h.lastHTTPEvent(t, i+1), http.StatusForbidden, "body")
	}
	if reqs := h.google.apiRequests(); len(reqs) != 0 {
		t.Fatalf("upstream saw %+v", reqs)
	}

	const boundary = "gws_boundary_798bf456d0cb0b59"
	gwsBody := "--" + boundary + "\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n{}\r\n--" + boundary +
		"\r\nContent-Type: message/rfc822\r\n\r\nTo: <alice@eclipsesource.com>\r\nSubject: hi\r\nMessage-ID: <e2e@gws>\r\n" +
		"Date: Fri, 13 Nov 2026 14:31:49 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"utf-8\"\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n\r\nhello\r\n--" + boundary + "--\r\n"
	got := h.do(t, http.MethodPost, send, gwsBody, withPlaceholder(), header("Content-Type", "multipart/related; boundary="+boundary))
	if got.status != http.StatusOK {
		t.Fatalf("gws send = %d %s", got.status, got.body)
	}
	reqs := h.google.apiRequests()
	if len(reqs) != 1 || reqs[0].Body != gwsBody || !strings.HasPrefix(reqs[0].Authorization, "Bearer "+filterAccessToken) {
		t.Fatalf("upstream saw %+v", reqs)
	}
}
