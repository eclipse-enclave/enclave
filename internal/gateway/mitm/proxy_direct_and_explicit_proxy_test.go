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
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"enclave/internal/netlog"
)

func TestSniffRequestLine(t *testing.T) {
	overLong := "GET /" + strings.Repeat("a", maxSniffedLineLength) + " HTTP/1.1\r\n"
	for name, tc := range map[string]struct {
		input      string
		want       requestLineKind
		wantDomain string
	}{
		"SMTP EHLO":            {input: "EHLO x\r\n", want: notHTTP},
		"SMTP QUIT":            {input: "QUIT\r\n", want: notHTTP},
		"SSH banner":           {input: "SSH-2.0-OpenSSH_9.6\r\n", want: notHTTP},
		"binary":               {input: "\x16\x03\x01\x00\xa5\x01\x00\x00", want: notHTTP},
		"no line end":          {input: "GET / HTTP/1.1", want: notHTTP},
		"HTTP/2 preface":       {input: "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n", want: notHTTP},
		"HTTP/0.9":             {input: "GET /\r\n", want: notHTTP},
		"lower-case method":    {input: "get / HTTP/1.1\r\n", want: notHTTP},
		"request line":         {input: "GET /path?q=1 HTTP/1.1\r\nHost: example.com\r\n\r\n", want: originFormRequest},
		"bare LF":              {input: "POST / HTTP/1.0\n", want: originFormRequest},
		"asterisk form":        {input: "OPTIONS * HTTP/1.1\r\n", want: originFormRequest},
		"over-long line":       {input: overLong, want: originFormRequest},
		"over-long non-method": {input: strings.Repeat("A", maxSniffedLineLength+1), want: notHTTP},
		"CONNECT":              {input: "CONNECT example.com:443 HTTP/1.1\r\n", want: proxyRequest, wantDomain: "example.com"},
		"absolute form":        {input: "GET http://Example.com:8080/x HTTP/1.1\r\n", want: proxyRequest, wantDomain: "example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			reader := bufio.NewReaderSize(strings.NewReader(tc.input), maxSniffedLineLength)
			got := sniffRequestLine(reader)
			if got.kind != tc.want || got.domain != tc.wantDomain {
				t.Fatalf("sniffRequestLine(%q) = %+v, want kind %d domain %q", tc.input, got, tc.want, tc.wantDomain)
			}
		})
	}
}

type countingListener struct {
	net.Listener
	accepted atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

// A request sent to the proxy listener itself has no original destination.
// Dialling its Host header instead would reach the proxy again and recurse.
func TestUnrestrictedProxyAnswersDirectRequestWithoutDialling(t *testing.T) {
	for _, loopback := range []struct{ network, addr string }{{"tcp4", "127.0.0.1:0"}, {"tcp6", "[::1]:0"}} {
		for name, host := range map[string]string{"listener": "", "forged": "localhost"} {
			t.Run(loopback.network+"/"+name, func(t *testing.T) {
				inner, err := net.Listen(loopback.network, loopback.addr)
				if err != nil {
					t.Skipf("no %s loopback: %v", loopback.network, err)
				}
				listener := &countingListener{Listener: inner}
				p, logPath := newUnrestrictedTestProxy(t)
				p.originalDst = func(net.Conn) (netip.AddrPort, error) { return netip.AddrPort{}, errors.New("not redirected") }
				serveHTTPOn(t, p, listener)

				hostHeader := inner.Addr().String()
				if host != "" {
					_, port, _ := net.SplitHostPort(hostHeader)
					hostHeader = net.JoinHostPort(host, port)
				}
				client := &http.Client{Timeout: 5 * time.Second}
				req, err := http.NewRequest(http.MethodGet, "http://"+inner.Addr().String()+"/", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Host = hostHeader
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("direct request error = %v", err)
				}
				_ = resp.Body.Close()

				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
				}
				if got := listener.accepted.Load(); got != 1 {
					t.Fatalf("listener accepted %d connections, want only the client's", got)
				}
				events := waitForAuditEvents(t, logPath, 1)
				if len(events) != 1 || events[0].Type != netlog.TypeHTTP || events[0].Verdict != netlog.VerdictDeny ||
					events[0].Rule != "invalid-host" || events[0].Status != http.StatusBadRequest {
					t.Fatalf("audit events = %+v, want one http invalid-host deny", events)
				}
			})
		}
	}
}

// Intercepted HTTPS (--network-log=requests) has no original destination when
// it reached the proxy directly either.
func TestUnrestrictedProxyRefusesInterceptedRequestWithoutDestination(t *testing.T) {
	p, logPath := newUnrestrictedTestProxy(t)
	req := httptest.NewRequest(http.MethodGet, "https://localhost:15443/", nil)
	recorder := httptest.NewRecorder()

	p.handler("https")(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	events := readAuditEvents(t, logPath)
	if len(events) != 1 || events[0].Verdict != netlog.VerdictDeny || events[0].Rule != "invalid-host" {
		t.Fatalf("audit events = %+v, want one invalid-host deny", events)
	}
}

// startForwardProxy is an explicit HTTP proxy: it tunnels CONNECT to the
// requested authority and answers absolute-form requests with the request
// target it received.
func startForwardProxy(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			_, _ = io.WriteString(w, "proxied "+r.RequestURI)
			return
		}
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		downstream, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		go func() {
			_, _ = io.Copy(upstream, buffered)
			closeWrite(upstream)
		}()
		_, _ = io.Copy(downstream, upstream)
		_ = downstream.Close()
		_ = upstream.Close()
	}))
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

// With HTTPS_PROXY pointing at a proxy on port 80, the client's CONNECT is
// redirected into the gateway; it must reach that proxy as sent.
func TestUnrestrictedProxyTunnelsConnectToExplicitProxy(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "through tunnel")
	}))
	defer target.Close()
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, startForwardProxy(t))
	gatewayURL, err := url.Parse("http://" + serveHTTP(t, p))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(gatewayURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- test-only.
		},
	}

	resp, err := client.Get(target.URL + "/")
	if err != nil {
		t.Fatalf("request through explicit proxy error = %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if string(body) != "through tunnel" {
		t.Fatalf("body = %q, want the target's response", body)
	}
	assertSingleTCPPass(t, logPath, "127.0.0.1", 80)
}

// An absolute-form request is meant for an explicit proxy, which rejects the
// origin form the HTTP server would forward.
func TestUnrestrictedProxyTunnelsAbsoluteFormToExplicitProxy(t *testing.T) {
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, startForwardProxy(t))
	conn, err := net.Dial("tcp", serveHTTP(t, p))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	const requestURI = "http://upstream.enclave-test.invalid/path?q=1"
	if _, err := io.WriteString(conn, "GET "+requestURI+" HTTP/1.1\r\nHost: upstream.enclave-test.invalid\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if string(body) != "proxied "+requestURI {
		t.Fatalf("body = %q, want the request target unchanged", body)
	}
	assertSingleTCPPass(t, logPath, unresolvableHost, 80)
}

// SMTP and other line protocols open with an upper-case word; they are not
// HTTP and must reach their destination instead of an HTTP 400.
func TestUnrestrictedProxyTunnelsLineProtocolOnHTTPPort(t *testing.T) {
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, startEchoUpstream(t))
	conn, err := net.Dial("tcp", serveHTTP(t, p))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	assertEcho(t, conn, []byte("EHLO x\r\n"))
	assertSingleTCPPass(t, logPath, "", 80)
}
