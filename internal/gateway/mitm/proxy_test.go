// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"bytes"
	"context"
	"crypto/tls"
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
	"testing"
	"time"

	"enclave/internal/domainpattern"
	"enclave/internal/model"
)

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

	if err := rewriteHeaders("https", "api.example.com", headers, rules); err != nil {
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

	if err := rewriteHeaders("http", "api.example.com", headers, rules); err == nil {
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

	if err := rewriteHeaders("https", "evil.example.com", headers, rules); err == nil {
		t.Fatalf("rewriteHeaders() error = nil, want non-nil")
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

	if err := rewriteHeaders("https", "api.example.com", headers, rules); err != nil {
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
	case <-clientErr:
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
	audit, err := newAuditLogger(logPath)
	if err != nil {
		t.Fatalf("newAuditLogger() error = %v", err)
	}
	defer func() { _ = audit.Close() }()

	p := newProxy([]string{host}, nil, nil, false, audit)
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
}

func TestProxyAuditLogWritesDenyEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := newAuditLogger(logPath)
	if err != nil {
		t.Fatalf("newAuditLogger() error = %v", err)
	}
	defer func() { _ = audit.Close() }()

	p := newProxy([]string{"api.example.com"}, nil, nil, false, audit)
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
	p := newProxy(nil, nil, nil, false, nil)
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
	p := newProxy([]string{"example.com"}, []string{"tracking.example.com"}, nil, false, nil)
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
	audit, err := newAuditLogger(logPath)
	if err != nil {
		t.Fatalf("newAuditLogger() error = %v", err)
	}
	defer func() { _ = audit.Close() }()

	p := newProxy([]string{"api.example.com"}, nil, nil, false, audit)
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
	}}, false, nil)
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
	}}, false, nil)
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

	if err := rewriteHeaders("https", "api.example.com", headers, rules); err == nil {
		t.Fatalf("rewriteHeaders() error = nil, want non-nil")
	}
}

func TestProxyAuditLogWritesSNIHostMismatchEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := newAuditLogger(logPath)
	if err != nil {
		t.Fatalf("newAuditLogger() error = %v", err)
	}
	defer func() { _ = audit.Close() }()

	p := newProxy([]string{"api.example.com"}, nil, nil, false, audit)
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

func readAuditEvents(t *testing.T, path string) []auditEvent {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	events := make([]auditEvent, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event auditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("Unmarshal(%q) error = %v", line, err)
		}
		events = append(events, event)
	}
	return events
}
