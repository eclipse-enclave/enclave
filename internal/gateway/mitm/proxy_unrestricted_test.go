// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"enclave/internal/model"
	"enclave/internal/netlog"
)

func newUnrestrictedTestProxy(t *testing.T) (*proxy, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatalf("netlog.NewAppender() error = %v", err)
	}
	t.Cleanup(func() { _ = audit.Close() })
	p := newProxy(nil, nil, nil, false, audit, testSession)
	p.setCapabilities(capabilitiesFor(model.NetworkModeUnrestricted))
	return p, logPath
}

// An unrestricted proxy has no allowlist: a host no policy names is forwarded
// and logged as an http pass event stamped with the session.
func TestUnrestrictedProxyForwardsAndLogsPlaintextHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	p, logPath := newUnrestrictedTestProxy(t)
	dst, err := netip.ParseAddrPort(upstreamURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/path?q=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), originalDstKey{}, dst))
	req.Host = upstreamURL.Host
	recorder := httptest.NewRecorder()

	p.handler("http")(recorder, req)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
	events := readAuditEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Type != netlog.TypeHTTP || event.Verdict != netlog.VerdictPass || event.Rule != netlog.RuleUnrestricted ||
		event.Session != testSession || event.Domain != "127.0.0.1" || event.Path != "/path" || event.Status != http.StatusAccepted {
		t.Fatalf("event = %+v, want http pass rule=unrestricted for 127.0.0.1/path", event)
	}
}

// An unrestricted proxy passes TLS for any SNI and logs a tcp pass event.
func TestUnrestrictedProxyPassesAndLogsTLS(t *testing.T) {
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, startTLSUpstream(t))
	server := &httpsDispatchServer{proxy: p, conns: map[net.Conn]struct{}{}}
	clientConn, serverConn := net.Pipe()

	done := make(chan struct{})
	go func() {
		server.serveConn(serverConn)
		close(done)
	}()
	go func() {
		// Closing the client ends the tunnel, so serveConn returns.
		defer func() { _ = clientConn.Close() }()
		_ = tls.Client(clientConn, &tls.Config{ServerName: "localhost", InsecureSkipVerify: true}).Handshake() // #nosec G402 -- test-only.
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("serveConn did not return")
	}

	events := readAuditEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Type != netlog.TypeTCP || event.Verdict != netlog.VerdictPass || event.Rule != netlog.RuleUnrestricted ||
		event.Session != testSession || event.Domain != "localhost" || event.Port != 443 {
		t.Fatalf("event = %+v, want tcp pass rule=unrestricted for localhost:443", event)
	}
}

func TestRestrictedCapabilitiesEnforceAllowlist(t *testing.T) {
	if !capabilitiesFor(model.NetworkModeRestricted).enforceAllowlist || !capabilitiesFor("").enforceAllowlist {
		t.Fatal("restricted and unknown modes must enforce the allowlist")
	}
	if capabilitiesFor(model.NetworkModeUnrestricted).enforceAllowlist {
		t.Fatal("unrestricted mode must not enforce the allowlist")
	}
}
