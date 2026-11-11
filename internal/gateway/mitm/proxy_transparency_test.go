// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"enclave/internal/netlog"
)

// The test hosts below are unresolvable on purpose: a proxy that re-resolves
// SNI or Host instead of dialling the original destination cannot reach them.
const unresolvableHost = "upstream.enclave-test.invalid"

func fixedOriginalDestination(t *testing.T, addr string) func(net.Conn) (netip.AddrPort, error) {
	t.Helper()
	dst, err := netip.ParseAddrPort(addr)
	if err != nil {
		t.Fatalf("ParseAddrPort(%q) error = %v", addr, err)
	}
	return func(net.Conn) (netip.AddrPort, error) { return dst, nil }
}

// startEchoUpstream accepts connections and sends back every byte it reads.
func startEchoUpstream(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func startTLSUpstream(t *testing.T) string {
	t.Helper()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(upstream.Close)
	return upstream.Listener.Addr().String()
}

// serveHTTPS runs the HTTPS dispatch for one client connection and returns the
// client end.
func serveHTTPS(t *testing.T, p *proxy) net.Conn {
	t.Helper()
	server := &httpsDispatchServer{proxy: p, conns: map[net.Conn]struct{}{}}
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	go server.serveConn(serverConn)
	return clientConn
}

// serveHTTP runs the plaintext HTTP frontend and returns its address.
func serveHTTP(t *testing.T, p *proxy) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveHTTPOn(t, p, listener)
	return listener.Addr().String()
}

func serveHTTPOn(t *testing.T, p *proxy, listener net.Listener) {
	t.Helper()
	server := p.newHTTPServer()
	go func() { _ = p.serveHTTP(server, listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

func assertEcho(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
}

func assertSingleTCPPass(t *testing.T, logPath string, domain string, port int) {
	t.Helper()
	events := waitForAuditEvents(t, logPath, 1)
	if len(events) != 1 {
		t.Fatalf("audit events = %+v, want exactly 1", events)
	}
	event := events[0]
	if event.Type != netlog.TypeTCP || event.Verdict != netlog.VerdictPass || event.Rule != netlog.RuleUnrestricted ||
		event.Session != testSession || event.Domain != domain || event.Port != port {
		t.Fatalf("event = %+v, want tcp pass rule=unrestricted domain=%q port=%d", event, domain, port)
	}
}

func waitForAuditEvents(t *testing.T, logPath string, want int) []netlog.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		events := readAuditEvents(t, logPath)
		if len(events) >= want || time.Now().After(deadline) {
			return events
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnrestrictedProxyTunnelsTLSToOriginalDestination(t *testing.T) {
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, startTLSUpstream(t))
	client := tls.Client(serveHTTPS(t, p), &tls.Config{ServerName: unresolvableHost, InsecureSkipVerify: true}) // #nosec G402 -- test-only.

	if err := client.Handshake(); err != nil {
		t.Fatalf("handshake through proxy: %v", err)
	}
	assertSingleTCPPass(t, logPath, unresolvableHost, 443)
}

func TestUnrestrictedProxyTunnelsTLSWithoutSNI(t *testing.T) {
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, startTLSUpstream(t))
	client := tls.Client(serveHTTPS(t, p), &tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- test-only.

	if err := client.Handshake(); err != nil {
		t.Fatalf("handshake without SNI through proxy: %v", err)
	}
	assertSingleTCPPass(t, logPath, "", 443)
}

func TestUnrestrictedProxyTunnelsNonTLSOnHTTPSPort(t *testing.T) {
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, startEchoUpstream(t))

	assertEcho(t, serveHTTPS(t, p), []byte("PING not tls\r\n"))
	assertSingleTCPPass(t, logPath, "", 443)
}

func TestUnrestrictedProxyForwardsHTTPToOriginalDestination(t *testing.T) {
	var gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, upstream.Listener.Addr().String())
	proxyAddr := serveHTTP(t, p)

	req, err := http.NewRequest(http.MethodGet, "http://"+proxyAddr+"/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = unresolvableHost
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if gotHost != unresolvableHost {
		t.Fatalf("upstream Host = %q, want %q", gotHost, unresolvableHost)
	}
	events := waitForAuditEvents(t, logPath, 1)
	if len(events) != 1 {
		t.Fatalf("audit events = %+v, want exactly 1", events)
	}
	event := events[0]
	if event.Type != netlog.TypeHTTP || event.Verdict != netlog.VerdictPass || event.Rule != netlog.RuleUnrestricted ||
		event.Session != testSession || event.Domain != unresolvableHost || event.Path != "/path" || event.Status != http.StatusAccepted {
		t.Fatalf("event = %+v, want http pass rule=unrestricted for %s/path", event, unresolvableHost)
	}
}

// HTTP/1.0 allows a request without Host; the original destination still
// tells the proxy where it goes.
func TestUnrestrictedProxyForwardsHTTPWithoutHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, upstream.Listener.Addr().String())
	conn, err := net.Dial("tcp", serveHTTP(t, p))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	if _, err := conn.Write([]byte("GET /path HTTP/1.0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	events := waitForAuditEvents(t, logPath, 1)
	if len(events) != 1 || events[0].Type != netlog.TypeHTTP || events[0].Verdict != netlog.VerdictPass || events[0].Domain != "" {
		t.Fatalf("audit events = %+v, want one http pass without domain", events)
	}
}

func TestUnrestrictedProxyTunnelsNonHTTPOnHTTPPort(t *testing.T) {
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, startEchoUpstream(t))
	conn, err := net.Dial("tcp", serveHTTP(t, p))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	assertEcho(t, conn, []byte("\x00\x01binary protocol"))
	assertSingleTCPPass(t, logPath, "", 80)
}

// A connection that was not redirected reports the proxy's own address as its
// original destination; dialling it would loop back into the proxy. The
// gateway's proxy listens dual-stack, where IPv4 addresses appear mapped.
func TestUnrestrictedProxyRefusesOriginalDestinationOfItself(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	client, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	for name, lookup := range map[string]func(net.Conn) (netip.AddrPort, error){
		"fixed":  fixedOriginalDestination(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))),
		"kernel": kernelOriginalDestination,
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := newUnrestrictedTestProxy(t)
			p.originalDst = lookup
			if dst := p.originalDestination(server); dst.IsValid() {
				t.Fatalf("originalDestination() = %v, want none for the proxy's own address", dst)
			}
		})
	}
}

// Without an original destination or a name to resolve, the proxy cannot
// deliver the connection; it drops it and says so in the log.
func TestUnrestrictedProxyDropsConnectionWithoutDestination(t *testing.T) {
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = func(net.Conn) (netip.AddrPort, error) { return netip.AddrPort{}, errors.New("not redirected") }
	client := serveHTTPS(t, p)
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))

	// The proxy may drop the connection before the whole payload is written.
	_, _ = client.Write([]byte("PING not tls\r\n"))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection stayed open without a destination")
	}
	events := waitForAuditEvents(t, logPath, 1)
	if len(events) != 1 || events[0].Type != netlog.TypeTCP || events[0].Rule != ruleUpstreamError || events[0].Port != 443 {
		t.Fatalf("audit events = %+v, want one tcp upstream-error event", events)
	}
}

// Restricted mode keeps re-resolving SNI: an original destination the kernel
// reports does not let a ClientHello without SNI through.
func TestRestrictedProxyIgnoresOriginalDestination(t *testing.T) {
	logPath := t.TempDir() + "/network.log"
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = audit.Close() }()
	p := newProxy([]string{"example.com"}, nil, nil, false, audit, testSession)
	p.originalDst = fixedOriginalDestination(t, startTLSUpstream(t))
	client := tls.Client(serveHTTPS(t, p), &tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- test-only.

	if err := client.Handshake(); err == nil {
		t.Fatal("handshake without SNI succeeded in restricted mode")
	}
	events := waitForAuditEvents(t, logPath, 1)
	if len(events) != 1 || events[0].Verdict != netlog.VerdictDeny || events[0].Rule != "tls-clienthello" {
		t.Fatalf("audit events = %+v, want one tls-clienthello deny", events)
	}
}
