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
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enclave/internal/domainpattern"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
	"enclave/internal/netlog"
)

// testSession stands in for the gateway container name the runtime injects.
const testSession = "enclave-test-claude"

func TestRewriteHeadersCustomPlaceholder(t *testing.T) {
	placeholder := "vendor_0123456789abcdef0123456789abcdef_end"
	rules := []model.SecretReleaseEntry{{Placeholder: placeholder, Value: "real-secret", Hosts: []string{"api.example.com"}, Header: "authorization", Format: "Bearer %s"}}
	for _, tc := range []struct {
		scheme, host string
		allowed      bool
	}{
		{"https", "api.example.com", true},
		{"http", "api.example.com", false},
		{"https", "other.example.com", false},
	} {
		for _, prefix := range []string{"", "Bearer "} {
			headers := http.Header{}
			headers.Set("Authorization", prefix+placeholder)
			_, err := rewriteHeaders(tc.scheme, tc.host, headers, rules)
			if (err == nil) != tc.allowed {
				t.Fatalf("%+v: error = %v", tc, err)
			}
			want := prefix + placeholder
			if tc.allowed {
				want = "Bearer real-secret"
			}
			if got := headers.Get("Authorization"); got != want {
				t.Fatalf("header = %q, want %q", got, want)
			}
		}
	}
}

func TestRewriteHeadersAuthorizedHost(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Api-Key", "ENCLAVE_SECRET_abc")
	rules := []model.SecretReleaseEntry{
		{
			Placeholder: "ENCLAVE_SECRET_abc",
			Value:       "real-secret",
			Hosts:       []string{"api.example.com"},
			Header:      "x-api-key",
		},
	}

	if _, err := rewriteHeaders("https", "api.example.com", headers, rules); err != nil {
		t.Fatalf("rewriteHeaders() error = %v", err)
	}
	if got := headers.Get("X-Api-Key"); got != "real-secret" {
		t.Fatalf("header value = %q, want %q", got, "real-secret")
	}
}

func TestRewriteHeadersPlaintextSecretReleaseBlocked(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Api-Key", "ENCLAVE_SECRET_abc")
	rules := []model.SecretReleaseEntry{
		{
			Placeholder: "ENCLAVE_SECRET_abc",
			Value:       "real-secret",
			Hosts:       []string{"api.example.com"},
			Header:      "x-api-key",
		},
	}

	if _, err := rewriteHeaders("http", "api.example.com", headers, rules); err == nil {
		t.Fatalf("rewriteHeaders() error = nil, want non-nil")
	}
	if got := headers.Get("X-Api-Key"); got != "ENCLAVE_SECRET_abc" {
		t.Fatalf("header value = %q, want placeholder unchanged", got)
	}
}

func TestRewriteHeadersUnauthorizedHostBlocked(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer ENCLAVE_SECRET_abc")
	rules := []model.SecretReleaseEntry{
		{
			Placeholder: "ENCLAVE_SECRET_abc",
			Value:       "real-secret",
			Hosts:       []string{"api.example.com"},
			Header:      "authorization",
			Format:      "Bearer %s",
		},
	}

	if _, err := rewriteHeaders("https", "evil.example.com", headers, rules); err == nil {
		t.Fatalf("rewriteHeaders() error = nil, want non-nil")
	}
}

// A host selected at runtime names one instance, so the token must not reach
// hosts beneath it the way a declared pattern would allow.
func TestRewriteHeadersExactHostRejectsSubdomain(t *testing.T) {
	rule := model.SecretReleaseEntry{
		Placeholder: "ENCLAVE_SECRET_abc",
		Value:       "real-secret",
		Hosts:       []string{"gitlab.example.com"},
		Header:      "private-token",
		ExactHosts:  true,
	}

	headers := http.Header{}
	headers.Set("Private-Token", "ENCLAVE_SECRET_abc")
	if _, err := rewriteHeaders("https", "runner.gitlab.example.com", headers, []model.SecretReleaseEntry{rule}); err == nil {
		t.Fatal("rewriteHeaders() error = nil, want the subdomain denied")
	}
	if got := headers.Get("Private-Token"); got != "ENCLAVE_SECRET_abc" {
		t.Fatalf("header value = %q, want placeholder unchanged", got)
	}

	headers = http.Header{}
	headers.Set("Private-Token", "ENCLAVE_SECRET_abc")
	if _, err := rewriteHeaders("https", "gitlab.example.com", headers, []model.SecretReleaseEntry{rule}); err != nil {
		t.Fatalf("rewriteHeaders() error = %v, want the selected host allowed", err)
	}
	if got := headers.Get("Private-Token"); got != "real-secret" {
		t.Fatalf("header value = %q, want %q", got, "real-secret")
	}
}

func TestRequiresMITMExactHostsIgnoresSubdomain(t *testing.T) {
	rules := []model.SecretReleaseEntry{
		{
			Placeholder: "ENCLAVE_SECRET_abc",
			Value:       "real-secret",
			Hosts:       []string{"gitlab.example.com"},
			Header:      "private-token",
			ExactHosts:  true,
		},
	}
	if !requiresMITM("gitlab.example.com", rules) {
		t.Fatal("requiresMITM(gitlab.example.com) = false, want true")
	}
	if requiresMITM("runner.gitlab.example.com", rules) {
		t.Fatal("requiresMITM(runner.gitlab.example.com) = true, want false")
	}
}

func TestNormalizeRuleRejectsWildcardInExactHostRule(t *testing.T) {
	_, err := normalizeRule(model.SecretReleaseEntry{
		SecretID:    "gitlab-token",
		Placeholder: "ENCLAVE_SECRET_abc",
		Value:       "real-secret",
		Hosts:       []string{"*.gitlab.example.com"},
		Header:      "private-token",
		ExactHosts:  true,
	})
	if err == nil {
		t.Fatal("normalizeRule() error = nil, want a wildcard in an exact-host rule rejected")
	}
}

func TestNormalizeHostStripsLeadingDot(t *testing.T) {
	got, err := domainpattern.NormalizeHost(".API.EXAMPLE.COM.")
	if err != nil {
		t.Fatalf("NormalizeHost() error = %v", err)
	}
	if got != "api.example.com" {
		t.Fatalf("NormalizeHost() = %q, want %q", got, "api.example.com")
	}
}

func TestRewriteHeadersKeepsExistingPrefixWhenFormatSet(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer ENCLAVE_SECRET_abc")
	rules := []model.SecretReleaseEntry{
		{
			Placeholder: "ENCLAVE_SECRET_abc",
			Value:       "real-secret",
			Hosts:       []string{"api.example.com"},
			Header:      "authorization",
			Format:      "Bearer %s",
		},
	}

	if _, err := rewriteHeaders("https", "api.example.com", headers, rules); err != nil {
		t.Fatalf("rewriteHeaders() error = %v", err)
	}
	if got := headers.Get("Authorization"); got != "Bearer real-secret" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer real-secret")
	}
}

func TestDomainMatch(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		pattern string
		want    bool
	}{
		{name: "exact", host: "api.example.com", pattern: "api.example.com", want: true},
		{name: "subdomain", host: "foo.api.example.com", pattern: "api.example.com", want: true},
		{name: "wildcard", host: "foo.example.com", pattern: "*.example.com", want: true},
		{name: "wildcard no apex", host: "example.com", pattern: "*.example.com", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domainpattern.MatchNormalizedHost(tt.host, tt.pattern); got != tt.want {
				t.Fatalf("MatchNormalizedHost(%q, %q) = %v, want %v", tt.host, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestHostAllowedEmptyPatternsDeny(t *testing.T) {
	if hostAllowed("api.example.com", nil) {
		t.Fatalf("hostAllowed(api.example.com, nil) = true, want false")
	}
}

func TestRequiresMITM(t *testing.T) {
	rules := []model.SecretReleaseEntry{
		{
			Placeholder: "ENCLAVE_SECRET_a",
			Value:       "secret-a",
			Hosts:       []string{"api.example.com"},
			Header:      "x-api-key",
		},
		{
			Placeholder: "ENCLAVE_SECRET_b",
			Value:       "secret-b",
			Hosts:       []string{"*.anthropic.com"},
			Header:      "x-api-key",
		},
	}
	if !requiresMITM("api.example.com", rules) {
		t.Fatalf("requiresMITM(api.example.com) = false, want true")
	}
	if !requiresMITM("platform.anthropic.com", rules) {
		t.Fatalf("requiresMITM(platform.anthropic.com) = false, want true")
	}
	if requiresMITM("example.net", rules) {
		t.Fatalf("requiresMITM(example.net) = true, want false")
	}
}

func TestRequiresMITMEmptyHostsFailClosed(t *testing.T) {
	rules := []model.SecretReleaseEntry{
		{
			Placeholder: "ENCLAVE_SECRET_a",
			Value:       "secret-a",
			Header:      "x-api-key",
		},
	}
	if requiresMITM("api.example.com", rules) {
		t.Fatalf("requiresMITM(api.example.com) = true, want false")
	}
}

func TestShouldMITMForced(t *testing.T) {
	if !shouldMITM("example.net", nil, true) {
		t.Fatal("shouldMITM(example.net, nil, true) = false, want true")
	}
}

func TestReadClientHelloExtractsSNI(t *testing.T) {
	host, preface := readClientHelloFromTLSClient(t, "api.example.com")
	if host != "api.example.com" {
		t.Fatalf("host = %q, want %q", host, "api.example.com")
	}
	if len(preface) == 0 {
		t.Fatalf("preface length = 0, want > 0")
	}
}

func TestReadClientHelloPreservesTrailingTLSBytes(t *testing.T) {
	host, preface := readClientHelloFromTLSClient(t, "api.example.com")
	trailer := []byte{0x14, 0x03, 0x03, 0x00, 0x01, 0x01}
	conn := &bufferedTestConn{Reader: bytes.NewReader(append(append([]byte{}, preface...), trailer...))}

	gotHost, gotPreface, err := readClientHello(conn)
	if err != nil {
		t.Fatalf("readClientHello() error = %v", err)
	}
	if gotHost != host {
		t.Fatalf("host = %q, want %q", gotHost, host)
	}
	if !bytes.Equal(gotPreface, preface) {
		t.Fatalf("preface mismatch")
	}

	remaining, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(remaining, trailer) {
		t.Fatalf("remaining bytes = %x, want %x", remaining, trailer)
	}
}

func readClientHelloFromTLSClient(t *testing.T, serverName string) (string, []byte) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	clientErr := make(chan error, 1)
	go func() {
		tlsClient := tls.Client(clientConn, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: true, // test-only; handshake is expected to fail after ClientHello read.
		})
		clientErr <- tlsClient.Handshake()
	}()

	host, preface, err := readClientHello(serverConn)
	if err != nil {
		t.Fatalf("readClientHello() error = %v", err)
	}
	_ = serverConn.Close()
	select {
	case err := <-clientErr:
		if err == nil {
			t.Fatal("client handshake unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("client handshake goroutine did not exit")
	}
	return host, preface
}

type bufferedTestConn struct {
	*bytes.Reader
}

func (c *bufferedTestConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *bufferedTestConn) Close() error                     { return nil }
func (c *bufferedTestConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *bufferedTestConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *bufferedTestConn) SetDeadline(time.Time) error      { return nil }
func (c *bufferedTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *bufferedTestConn) SetWriteDeadline(time.Time) error { return nil }

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

func TestParseClientHelloServerNameNoSNI(t *testing.T) {
	// Minimal ClientHello payload without extensions.
	payload, err := hex.DecodeString("0303000000000000000000000000000000000000000000000000000000000000000000000213010100")
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	got, err := parseClientHelloServerName(payload)
	if err != nil {
		t.Fatalf("parseClientHelloServerName() error = %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty SNI, got %q", got)
	}
}

func TestParseClientHelloServerNameMalformed(t *testing.T) {
	if _, err := parseClientHelloServerName([]byte{0x03, 0x03, 0x00}); err == nil {
		t.Fatal("expected malformed ClientHello to fail")
	}
}

func TestLoadRulesRejectsBroadWildcardHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	payload := `[{"secret_id":"api-key","placeholder":"ENCLAVE_SECRET_abc","value":"real-secret","hosts":["*.com"],"header":"authorization"}]`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}

	if _, err := LoadRules(path); err == nil {
		t.Fatalf("LoadRules(%q) error = nil, want non-nil", path)
	}
}

func TestProxyAuditLogWritesPassEvent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", upstream.URL, err)
	}
	host, port, err := net.SplitHostPort(upstreamURL.Host)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", upstreamURL.Host, err)
	}

	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatalf("netlog.NewAppender() error = %v", err)
	}
	defer func() { _ = audit.Close() }()

	p := newProxy([]string{host}, nil, nil, false, audit, testSession)
	req := httptest.NewRequest(http.MethodPost, upstream.URL+"/v1/messages?token=secret", strings.NewReader("body"))
	req.Host = upstreamURL.Host
	recorder := httptest.NewRecorder()

	p.handler("http")(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("proxy status = %d, want %d", recorder.Code, http.StatusCreated)
	}

	events := readAuditEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Type != "http" {
		t.Fatalf("event type = %q, want %q", event.Type, "http")
	}
	if event.Verdict != "pass" {
		t.Fatalf("event verdict = %q, want %q", event.Verdict, "pass")
	}
	if event.Rule != "allowlist" {
		t.Fatalf("event rule = %q, want %q", event.Rule, "allowlist")
	}
	if event.Domain != host {
		t.Fatalf("event domain = %q, want %q", event.Domain, host)
	}
	if event.Port <= 0 || strconv.Itoa(event.Port) != port {
		t.Fatalf("event port = %d, want %s", event.Port, port)
	}
	if event.Path != "/v1/messages" {
		t.Fatalf("event path = %q, want %q", event.Path, "/v1/messages")
	}
	if event.Status != http.StatusCreated {
		t.Fatalf("event status = %d, want %d", event.Status, http.StatusCreated)
	}
	if event.ResponseSize == 0 {
		t.Fatalf("event response size = 0, want > 0")
	}
	if event.Session != testSession {
		t.Fatalf("event session = %q, want %q", event.Session, testSession)
	}
}

// echoUpgradeHandler switches to an "echo" protocol on request and then echoes
// one line, staying silent until the client's first frame like a WebSocket
// server. Other requests get their protocol version back.
func echoUpgradeHandler(w http.ResponseWriter, r *http.Request) {
	if upgradeType(r.Header) != "echo" {
		_, _ = io.WriteString(w, r.Proto)
		return
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	_ = rw.Flush()
	// Give up like a real server would, so a stuck relay fails the test.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := rw.ReadString('\n')
	if err != nil {
		return
	}
	_, _ = rw.WriteString("echo: " + line)
	_ = rw.Flush()
}

// assertEchoUpgradeRelayed upgrades a connection to host through the proxy and
// checks that a frame makes the round trip.
func assertEchoUpgradeRelayed(t *testing.T, proxyServer *httptest.Server, host string) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxyServer.URL, "http://"))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, "GET /stream HTTP/1.1\r\nHost: "+host+"\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n"); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != "echo" {
		t.Fatalf("upgrade response = %d %v, want 101 with Upgrade: echo", resp.StatusCode, resp.Header)
	}
	if _, err := io.WriteString(conn, "ping\n"); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read relayed frame: %v", err)
	}
	if line != "echo: ping\n" {
		t.Fatalf("relayed frame = %q, want %q", line, "echo: ping\n")
	}
}

func TestProxyRelaysProtocolUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(echoUpgradeHandler))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	host, _, err := net.SplitHostPort(upstreamHost)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", upstreamHost, err)
	}

	p := newProxy([]string{host}, nil, nil, false, nil, testSession)
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()

	assertEchoUpgradeRelayed(t, proxyServer, upstreamHost)
}

func TestProxyRelaysProtocolUpgradeToHTTP2Upstream(t *testing.T) {
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(echoUpgradeHandler))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "https://")
	host, _, err := net.SplitHostPort(upstreamHost)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", upstreamHost, err)
	}

	p := newProxy([]string{host}, nil, nil, false, nil, testSession)
	// Trusting the test CA takes a TLS config, which turns off the HTTP/2
	// the default transport otherwise negotiates; force it back on.
	trust := upstream.Client().Transport.(*http.Transport).TLSClientConfig
	p.transport.TLSClientConfig = trust.Clone()
	p.transport.ForceAttemptHTTP2 = true
	p.upgradeTransport.TLSClientConfig = trust.Clone()
	p.upgradeTransport.ForceAttemptHTTP2 = true
	proxyServer := httptest.NewServer(p.handler("https"))
	defer proxyServer.Close()

	req, err := http.NewRequest(http.MethodGet, proxyServer.URL+"/proto", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Host = upstreamHost
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("plain request through proxy: %v", err)
	}
	proto, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(proto) != "HTTP/2.0" {
		t.Fatalf("upstream saw %q for a plain request, want HTTP/2.0", proto)
	}

	assertEchoUpgradeRelayed(t, proxyServer, upstreamHost)
}

// The servers' Shutdown leaves upgraded connections alone, so the proxy closes
// them itself and waits for their events to be written before it exits.
func TestProxyLogsUpgradedConnectionsOpenWhenItStops(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(echoUpgradeHandler))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	host, _, err := net.SplitHostPort(upstreamHost)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", upstreamHost, err)
	}
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatalf("netlog.NewAppender() error = %v", err)
	}
	defer func() { _ = audit.Close() }()
	p := newProxy([]string{host}, nil, nil, false, audit, testSession)
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(proxyServer.URL, "http://"))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, "GET /stream HTTP/1.1\r\nHost: "+upstreamHost+"\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n"); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}
	reader := bufio.NewReader(conn)
	if resp, err := http.ReadResponse(reader, nil); err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade response = %v, %v", resp, err)
	}
	if events := readAuditEvents(t, logPath); len(events) != 0 {
		t.Fatalf("events logged while the connection is open: %+v", events)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.exchanges.closeUpgraded()
	p.exchanges.wait(ctx)

	events := readAuditEvents(t, logPath)
	if len(events) != 1 || events[0].Status != http.StatusSwitchingProtocols {
		t.Fatalf("events after stopping = %+v, want the 101 of the open connection", events)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("the client connection is still open after stopping")
	}
}

func TestProxyRejectsUnrequestedProtocolSwitch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = rw.Flush()
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	host, _, err := net.SplitHostPort(upstreamHost)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", upstreamHost, err)
	}

	p := newProxy([]string{host}, nil, nil, false, nil, testSession)
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()

	req, err := http.NewRequest(http.MethodGet, proxyServer.URL+"/", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Host = upstreamHost
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("proxy status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
}

func TestProxyFlushesStreamedResponse(t *testing.T) {
	sendFirst := make(chan struct{})
	sendLast := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_ = rc.Flush()
		<-sendFirst
		_, _ = io.WriteString(w, "data: first\n\n")
		_ = rc.Flush()
		<-sendLast
		_, _ = io.WriteString(w, "data: last\n\n")
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	host, _, err := net.SplitHostPort(upstreamHost)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", upstreamHost, err)
	}

	p := newProxy([]string{host}, nil, nil, false, nil, testSession)
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()
	releaseFirst := sync.OnceFunc(func() { close(sendFirst) })
	releaseLast := sync.OnceFunc(func() { close(sendLast) })
	defer releaseLast()
	defer releaseFirst()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyServer.URL+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Host = upstreamHost
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("stream not opened before its first event: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	releaseFirst()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("first line = %q, %v; want %q", line, err, "data: first\n")
	}

	releaseLast()
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read rest of stream: %v", err)
	}
	if !strings.Contains(string(rest), "data: last") {
		t.Fatalf("rest of stream = %q, want the last event", rest)
	}
}

func TestProxyStampsSessionOnTCPEvents(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatalf("netlog.NewAppender() error = %v", err)
	}
	defer func() { _ = audit.Close() }()

	p := newProxy(nil, nil, nil, false, audit, testSession)
	p.logEvent(newTCPAuditEvent("blocked.example", netlog.VerdictDeny, "allowlist"))

	events := readAuditEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	if events[0].Session != testSession || events[0].Type != netlog.TypeTCP {
		t.Fatalf("event = %+v", events[0])
	}
}

func TestProxyAuditLogWritesDenyEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatalf("netlog.NewAppender() error = %v", err)
	}
	defer func() { _ = audit.Close() }()

	p := newProxy([]string{"api.example.com"}, nil, nil, false, audit, testSession)
	req := httptest.NewRequest(http.MethodGet, "http://evil.example.com/blocked?foo=bar", nil)
	req.Host = "evil.example.com"
	recorder := httptest.NewRecorder()

	p.handler("http")(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("proxy status = %d, want %d", recorder.Code, http.StatusForbidden)
	}

	events := readAuditEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Verdict != "deny" {
		t.Fatalf("event verdict = %q, want %q", event.Verdict, "deny")
	}
	if event.Rule != "allowlist" {
		t.Fatalf("event rule = %q, want %q", event.Rule, "allowlist")
	}
	if event.Path != "/blocked" {
		t.Fatalf("event path = %q, want %q", event.Path, "/blocked")
	}
	if event.Status != http.StatusForbidden {
		t.Fatalf("event status = %d, want %d", event.Status, http.StatusForbidden)
	}
}

func TestProxyDenyWhenAllowlistEmpty(t *testing.T) {
	p := newProxy(nil, nil, nil, false, nil, testSession)
	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil)
	req.Host = "api.example.com"
	recorder := httptest.NewRecorder()

	p.handler("http")(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("proxy status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestProxyDenyWinsOverSubdomainOfAllowedParent(t *testing.T) {
	// allow example.com, deny tracking.example.com: the denied host is a
	// subdomain of an allowed parent, so suffix-matching the allow set alone
	// would pass it. The deny-first check must reject it while a different
	// subdomain of the same parent still passes.
	p := newProxy([]string{"example.com"}, []string{"tracking.example.com"}, nil, false, nil, testSession)
	if p.hostPermitted("tracking.example.com") {
		t.Fatal("expected denied subdomain to be rejected")
	}
	if p.hostPermitted("metrics.tracking.example.com") {
		t.Fatal("expected subdomain of denied domain to be rejected")
	}
	if !p.hostPermitted("api.example.com") {
		t.Fatal("expected a non-denied subdomain of the allowed parent to pass")
	}
}

func TestProxyAuditLogWritesUpstreamErrorRule(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatalf("netlog.NewAppender() error = %v", err)
	}
	defer func() { _ = audit.Close() }()

	p := newProxy([]string{"api.example.com"}, nil, nil, false, audit, testSession)
	p.transport = &http.Transport{
		Proxy: nil,
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("forced dial failure")
		},
	}

	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/fail", nil)
	req.Host = "api.example.com"
	recorder := httptest.NewRecorder()

	p.handler("http")(recorder, req)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("proxy status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}

	events := readAuditEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Verdict != "pass" {
		t.Fatalf("event verdict = %q, want %q", event.Verdict, "pass")
	}
	if event.Rule != "upstream-error" {
		t.Fatalf("event rule = %q, want %q", event.Rule, "upstream-error")
	}
	if event.Status != http.StatusBadGateway {
		t.Fatalf("event status = %d, want %d", event.Status, http.StatusBadGateway)
	}
}

func TestProxyHandlerReturnsGenericBodyForSecretInjectionDeny(t *testing.T) {
	p := newProxy([]string{"api.example.com"}, nil, []model.SecretReleaseEntry{{
		Placeholder: "ENCLAVE_SECRET_abc",
		Value:       "real-secret",
		Hosts:       []string{"api.example.com"},
		Header:      "authorization",
	}}, false, nil, testSession)
	req := httptest.NewRequest(http.MethodGet, "https://evil.example.com/", nil)
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer ENCLAVE_SECRET_abc")
	recorder := httptest.NewRecorder()

	p.handler("https")(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("proxy status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if strings.Contains(recorder.Body.String(), "placeholder") {
		t.Fatalf("response body leaked placeholder-specific denial: %q", recorder.Body.String())
	}
}

func TestProxyHandlerDeniesPlaintextSecretRelease(t *testing.T) {
	p := newProxy([]string{"api.example.com"}, nil, []model.SecretReleaseEntry{{
		Placeholder: "ENCLAVE_SECRET_abc",
		Value:       "real-secret",
		Hosts:       []string{"api.example.com"},
		Header:      "authorization",
	}}, false, nil, testSession)
	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil)
	req.Host = "api.example.com"
	req.Header.Set("Authorization", "Bearer ENCLAVE_SECRET_abc")
	recorder := httptest.NewRecorder()

	p.handler("http")(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("proxy status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer ENCLAVE_SECRET_abc" {
		t.Fatalf("Authorization = %q, want placeholder unchanged", got)
	}
}

func TestRewriteHeadersEmptyHostsBlocked(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Api-Key", "ENCLAVE_SECRET_abc")
	rules := []model.SecretReleaseEntry{
		{
			Placeholder: "ENCLAVE_SECRET_abc",
			Value:       "real-secret",
			Header:      "x-api-key",
		},
	}

	if _, err := rewriteHeaders("https", "api.example.com", headers, rules); err == nil {
		t.Fatalf("rewriteHeaders() error = nil, want non-nil")
	}
}

func TestProxyAuditLogWritesSNIHostMismatchEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatalf("netlog.NewAppender() error = %v", err)
	}
	defer func() { _ = audit.Close() }()

	p := newProxy([]string{"api.example.com"}, nil, nil, false, audit, testSession)
	req := httptest.NewRequest(http.MethodGet, "https://api.example.com/v1/messages", nil)
	req.Host = "api.example.com"
	req.TLS = &tls.ConnectionState{ServerName: "different.example.com"}
	recorder := httptest.NewRecorder()

	p.transport = &http.Transport{
		Proxy: nil,
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("forced dial failure")
		},
	}

	p.handler("https")(recorder, req)

	events := readAuditEvents(t, logPath)
	if len(events) != 2 {
		t.Fatalf("audit events = %d, want 2", len(events))
	}
	if events[0].Rule != "sni-host-mismatch" {
		t.Fatalf("first audit rule = %q, want %q", events[0].Rule, "sni-host-mismatch")
	}
}

func readAuditEvents(t *testing.T, path string) []netlog.Event {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	events := make([]netlog.Event, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event netlog.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("Unmarshal(%q) error = %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestProxyStreamsBeforeEOFAndCancelsUpstream(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			upstreamCanceled := make(chan struct{})
			const event = "data: first-token\n\n"
			client, target, _ := streamingTestProxy(t, scheme, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, event)
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("flush upstream: %v", err)
				}
				<-req.Context().Done()
				close(upstreamCanceled)
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("receive stream headers before upstream EOF: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			buf := make([]byte, len(event))
			if _, err := io.ReadFull(resp.Body, buf); err != nil {
				t.Fatalf("receive first event before upstream EOF: %v", err)
			}
			if string(buf) != event {
				t.Fatalf("first event = %q, want %q", buf, event)
			}
			cancel()
			select {
			case <-upstreamCanceled:
			case <-time.After(5 * time.Second):
				t.Fatal("downstream cancellation did not reach upstream")
			}
		})
	}
}

func TestProxyAbortsAndAuditsTruncatedStreams(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			client, target, logPath := streamingTestProxy(t, scheme, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Content-Length", "100")
				_, _ = io.WriteString(w, "data: partial\n\n")
				_ = http.NewResponseController(w).Flush()
			}))
			resp, err := client.Get(target)
			if err != nil {
				t.Fatalf("receive initial stream headers: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("read truncated stream error = %v, want unexpected EOF", err)
			}
			if string(body) != "data: partial\n\n" {
				t.Fatalf("partial response = %q, want upstream bytes without appended error body", body)
			}
			events := readAuditEvents(t, logPath)
			var last netlog.Event
			for _, event := range events {
				if event.Type == netlog.TypeHTTP {
					last = event
				}
			}
			if last.Rule != "upstream-body-error" || last.ResponseSize != int64(len(body)) {
				t.Fatalf("truncated stream audit = %+v", last)
			}
		})
	}
}

func TestProxyStripsHopHeadersAndForwardsTrailers(t *testing.T) {
	client, target, _ := streamingTestProxy(t, "http", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		for _, name := range []string{"X-Request-Hop", "Proxy-Authorization"} {
			if req.Header.Get(name) != "" {
				t.Errorf("upstream received hop-by-hop header %s", name)
			}
		}
		w.Header().Set("Connection", "X-Response-Hop")
		w.Header().Set("X-Response-Hop", "must-not-forward")
		w.Header().Set("Trailer", "X-Checksum")
		_, _ = io.WriteString(w, "complete response")
		w.Header().Set("X-Checksum", "verified")
	}))
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "X-Request-Hop")
	req.Header.Set("X-Request-Hop", "must-not-forward")
	req.Header.Set("Proxy-Authorization", "must-not-forward")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "complete response" {
		t.Fatalf("response = %q, error = %v", body, err)
	}
	if resp.Header.Get("X-Response-Hop") != "" {
		t.Fatal("downstream received hop-by-hop response header")
	}
	if got := resp.Trailer.Get("X-Checksum"); got != "verified" {
		t.Fatalf("trailer = %q, want verified", got)
	}
}

func TestProxyRejectsUnsupportedProtocolUpgrade(t *testing.T) {
	client, target, _ := streamingTestProxy(t, "http", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Upgrade", "websocket")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	resp, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("upgrade status = %d, want 502", resp.StatusCode)
	}
}

func streamingTestProxy(t *testing.T, scheme string, handler http.Handler) (*http.Client, string, string) {
	t.Helper()
	upstream := httptest.NewUnstartedServer(handler)
	if scheme == "https" {
		upstream.StartTLS()
	} else {
		upstream.Start()
	}
	t.Cleanup(upstream.Close)
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = audit.Close() })
	p := newProxy([]string{"stream.example"}, nil, nil, true, audit, testSession)
	p.transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	t.Cleanup(p.transport.CloseIdleConnections)
	var proxyAddr string
	clientTransport := &http.Transport{}
	if scheme == "https" {
		upstreamRoots := x509.NewCertPool()
		upstreamRoots.AddCert(upstream.Certificate())
		p.transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: upstreamRoots, ServerName: "127.0.0.1"}
		store := tlsstore.New(t.TempDir())
		if err := store.EnsureCA(); err != nil {
			t.Fatal(err)
		}
		caPEM, err := os.ReadFile(store.CACertPath)
		if err != nil {
			t.Fatal(err)
		}
		clientRoots := x509.NewCertPool()
		if !clientRoots.AppendCertsFromPEM(caPEM) {
			t.Fatal("load proxy CA")
		}
		clientTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: clientRoots}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		dispatch, err := newHTTPSDispatchServer(p, store, listener)
		if err != nil {
			_ = listener.Close()
			t.Fatal(err)
		}
		served := make(chan error, 1)
		go func() { served <- dispatch.Serve() }()
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := dispatch.Shutdown(ctx); err != nil {
				t.Errorf("shutdown HTTPS proxy: %v", err)
			}
			if err := <-served; err != nil {
				t.Errorf("serve HTTPS proxy: %v", err)
			}
		})
		proxyAddr = listener.Addr().String()
	} else {
		server := httptest.NewServer(p.handler(scheme))
		t.Cleanup(server.Close)
		proxyAddr = server.Listener.Addr().String()
	}
	clientTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, proxyAddr)
	}
	t.Cleanup(clientTransport.CloseIdleConnections)
	return &http.Client{Transport: clientTransport, Timeout: 10 * time.Second}, scheme + "://stream.example/stream", logPath
}

// A release host can reflect arbitrary request headers in error messages. The
// declared authentication header is the only field allowed to receive a key.
func TestProxyRejectsSecretInReflectableHeaders(t *testing.T) {
	const placeholder = "ENCLAVE_SECRET_esr001"
	const canary = "synthetic-provider-api-key"
	for _, tc := range []struct {
		name, header, format, value, extraHeader string
		extraValues                              []string
		deny                                     bool
	}{
		{name: "reflected-beta", header: "x-api-key", value: placeholder, extraHeader: "Anthropic-Beta", extraValues: []string{placeholder}, deny: true},
		{name: "other-header", header: "x-api-key", value: placeholder, extraHeader: "X-Trace", extraValues: []string{"trace=" + placeholder}, deny: true},
		{name: "duplicate-header", header: "x-api-key", value: placeholder, extraHeader: "Anthropic-Beta", extraValues: []string{"safe", placeholder}, deny: true},
		{name: "similar-header", header: "x-api-key", value: placeholder, extraHeader: "X-Api-Key-Extra", extraValues: []string{placeholder}, deny: true},
		{name: "declared-api-key", header: "x-api-key", value: placeholder},
		{name: "bare-bearer", header: "authorization", format: "Bearer %s", value: placeholder},
		{name: "existing-bearer", header: "authorization", format: "Bearer %s", value: "Bearer " + placeholder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				want := canary
				if tc.format != "" {
					want = "Bearer " + canary
				}
				if req.Header.Get(tc.header) != want {
					t.Error("declared authentication header did not receive expected credential")
				}
				_, _ = io.WriteString(w, "Unexpected value(s) "+req.Header.Get("Anthropic-Beta")+" for the anthropic-beta header")
			}))
			defer upstream.Close()
			target, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			p := newProxy([]string{target.Hostname()}, nil, []model.SecretReleaseEntry{{
				SecretID: "anthropic-api-key", Placeholder: placeholder, Value: canary,
				Hosts: []string{target.Hostname()}, Header: tc.header, Format: tc.format,
			}}, false, nil, testSession)
			p.transport = upstream.Client().Transport.(*http.Transport)
			req := httptest.NewRequest(http.MethodPost, upstream.URL+"/v1/messages", strings.NewReader("{}"))
			// Deliberately use a non-canonical casing: HTTP header matching is
			// case-insensitive, but only the declared field may be substituted.
			req.Header[tc.header] = []string{tc.value}
			if tc.extraHeader != "" {
				req.Header[tc.extraHeader] = tc.extraValues
			}
			w := httptest.NewRecorder()
			p.handler("https")(w, req)
			wantStatus, wantCalls := http.StatusOK, int32(1)
			if tc.deny {
				wantStatus, wantCalls = http.StatusForbidden, 0
			}
			if w.Code != wantStatus || calls.Load() != wantCalls {
				t.Fatalf("status = %d, upstream calls = %d; want %d, %d", w.Code, calls.Load(), wantStatus, wantCalls)
			}
			if strings.Contains(w.Body.String(), canary) {
				t.Fatal("real credential reflected to workload")
			}
			if tc.deny && req.Header[tc.header][0] != tc.value {
				t.Fatal("denied request partially rewrote authentication header")
			}
		})
	}
}

func TestRunReturnsReadinessFailure(t *testing.T) {
	want := errors.New("injected readiness failure")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	called := false
	err := Run(ctx, Options{
		HTTPAddr: "127.0.0.1:0", HTTPSAddr: "127.0.0.1:0",
		TLSStore: tlsstore.New(t.TempDir()),
		OnReady:  func() error { called = true; return want },
	})
	if !called || !errors.Is(err, want) {
		t.Fatalf("readiness failure reported success: %v, called=%t", err, called)
	}
}

// In allow-all mode a Host naming the proxy itself would loop the request
// through it; other local services stay reachable.
func TestProxyRefusesToDialItself(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "local service") }))
	defer local.Close()
	p := newProxy(nil, nil, nil, false, nil, testSession)
	p.allowAllDestinations = true
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: p.handler("http"), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	p.guardSelfDial(listener)

	for target, want := range map[string]int{
		listener.Addr().String():                 http.StatusBadGateway,
		strings.TrimPrefix(local.URL, "http://"): http.StatusOK,
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+target+"/", nil)
		w := httptest.NewRecorder()
		p.handler("http")(w, req)
		if w.Code != want {
			t.Fatalf("%s: status %d, want %d", target, w.Code, want)
		}
	}
}
