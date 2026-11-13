// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"enclave/internal/gateway/tlsstore"
)

// Run applies an allowlist reload in place: the new list takes effect, the
// connections the proxy held are closed as a restart would close them, and
// the proxy reports ready again so the entrypoint knows the reload is done.
func TestRunReloadsAllowlistInPlace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "upstream")
	}))
	t.Cleanup(upstream.Close)

	reloads := make(chan Allowlist)
	ready := make(chan struct{}, 4)
	httpPort := freeLoopbackPort(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			HTTPPort:       httpPort,
			HTTPSPort:      freeLoopbackPort(t),
			AllowedDomains: []string{"127.0.0.1"},
			TLSStore:       tlsstore.New(t.TempDir()),
			Reloads:        reloads,
			OnReady:        func() { ready <- struct{}{} },
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})
	awaitReady := func() {
		t.Helper()
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("Run() ended: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("proxy never reported ready")
		}
	}
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)
	get := func() (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, "http://"+proxyAddr+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = upstream.Listener.Addr().String()
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	awaitReady()

	if status, body := get(); status != http.StatusOK || body != "upstream" {
		t.Fatalf("before reload = %d %q, want the upstream response", status, body)
	}
	held, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	// A request on the held connection makes sure the server tracks it.
	if _, err := fmt.Fprintf(held, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.Listener.Addr()); err != nil {
		t.Fatal(err)
	}
	heldReader := bufio.NewReader(held)
	resp, err := http.ReadResponse(heldReader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	reloads <- Allowlist{Allowed: []string{"other.example"}}
	awaitReady()

	_ = held.SetReadDeadline(time.Now().Add(5 * time.Second))
	var netErr net.Error
	if _, err := heldReader.ReadByte(); err == nil || errors.As(err, &netErr) && netErr.Timeout() {
		t.Errorf("a connection held across the reload stayed open (%v)", err)
	}
	if status, body := get(); status != http.StatusForbidden || !strings.Contains(body, "Domain not in allowlist") {
		t.Fatalf("after reload = %d %q, want the allowlist denial", status, body)
	}
}
