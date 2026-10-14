// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"enclave/internal/gateway/oauthbroker"
	"enclave/internal/netlog"
)

// This exercises the actual forwarder seam, including default-port URL/Host
// normalization. Unit tests that call Broker.RoundTrip directly miss that seam.
func TestOAuthCaptureThroughForwarder(t *testing.T) {
	const access = "synthetic-provider-access-credential"
	const refresh = "synthetic-provider-refresh-credential"
	var exchanges atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/oauth/token":
			exchanges.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Set-Cookie", "credential="+access)
			_, _ = io.WriteString(w, `{"access_token":"`+access+`","refresh_token":"`+refresh+`","token_type":"Bearer","expires_in":3600,"scope":"user:inference user:profile","account":{"uuid":"11111111-1111-1111-1111-111111111111","email_address":"synthetic@example.test"},"organization":{"uuid":"22222222-2222-2222-2222-222222222222"}}`)
		case "/v1/messages":
			if req.Header.Get("Authorization") != "Bearer "+access {
				t.Error("provider received incorrect credential")
			}
			_, _ = io.WriteString(w, "authenticated")
		default:
			t.Error("undeclared provider path was reached")
		}
	}))
	defer upstream.Close()
	p, logPath, _ := oauthTestProxy(t, upstream)
	payload := `{"grant_type":"authorization_code","client_id":"9d1c250a-e61b-44d9-88ed-5944d1962f5e","code":"synthetic-code","code_verifier":"synthetic-verifier","redirect_uri":"https://platform.claude.com/oauth/code/callback"}`
	req := httptest.NewRequest(http.MethodPost, "https://platform.claude.com/v1/oauth/token", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.handler("https")(w, req)
	if w.Code != http.StatusOK || exchanges.Load() != 1 {
		t.Fatalf("capture status = %d, exchanges = %d", w.Code, exchanges.Load())
	}
	if strings.Contains(w.Body.String(), access) || strings.Contains(w.Body.String(), refresh) || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("capture leaked provider credentials")
	}
	var token struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &token); err != nil || token.Access == "" {
		t.Fatal("capture did not return a handle")
	}
	request := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token.Access)
	result := httptest.NewRecorder()
	p.handler("https")(result, request)
	if result.Code != http.StatusOK || result.Body.String() != "authenticated" {
		t.Fatalf("authenticated request status = %d", result.Code)
	}
	for _, target := range []string{
		"https://platform.claude.com/v1/oauth/token?variant=1",
		"https://platform.claude.com./v1/oauth/token",
		"https://PLATFORM.CLAUDE.COM./v1/oauth/token",
		"http://platform.claude.com/v1/oauth/token",
		"https://platform.claude.com:8443/v1/oauth/token",
	} {
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		p.handler(request.URL.Scheme)(result, request)
		if result.Code != http.StatusForbidden || exchanges.Load() != 1 {
			t.Fatal("alternate token endpoint escaped capture")
		}
		events := readAuditEvents(t, logPath)
		last := events[len(events)-1]
		if last.Status != http.StatusForbidden || last.Verdict != netlog.VerdictDeny || last.Rule != "oauth-broker-denied" {
			t.Fatalf("broker denial audit = %+v", last)
		}
	}
	bad := httptest.NewRequest(http.MethodGet, "https://api.anthropic.com/"+token.Access, nil)
	badResult := httptest.NewRecorder()
	p.handler("https")(badResult, bad)
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), token.Access) || strings.Contains(string(logged), access) || strings.Contains(string(logged), refresh) {
		t.Fatal("OAuth credential or handle reached audit log")
	}
}

func oauthTestProxy(t *testing.T, upstream *httptest.Server) (*proxy, string, oauthbroker.Config) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"grant", "session"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "identity.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config := oauthbroker.Config{
		StoreDir: filepath.Join(root, "grant"), RuntimeDir: filepath.Join(root, "session"),
		LockPath: filepath.Join(root, "identity.lock"), Identity: "fixture", Session: testSession, Incarnation: "first",
	}
	broker, err := oauthbroker.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "audit.jsonl")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = audit.Close() })
	p := newProxy([]string{"platform.claude.com", "api.anthropic.com"}, nil, nil, false, audit, testSession)
	p.transport = upstream.Client().Transport.(*http.Transport).Clone()
	p.transport.TLSClientConfig.ServerName = "example.com" // httptest certificate identity.
	p.transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	p.setBroker(broker)
	t.Cleanup(p.transport.CloseIdleConnections)
	return p, logPath, config
}

const oauthEnrollmentPayload = `{"grant_type":"authorization_code","client_id":"9d1c250a-e61b-44d9-88ed-5944d1962f5e","code":"synthetic-code","code_verifier":"synthetic-verifier","redirect_uri":"https://platform.claude.com/oauth/code/callback"}`

func TestOAuthForwarderFailureAudit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		rule    string
		verdict string
		calls   int32
	}{
		{"denied", http.StatusForbidden, "oauth-broker-denied", netlog.VerdictDeny, 0},
		{"state", http.StatusServiceUnavailable, "oauth-broker-state", netlog.VerdictDeny, 0},
		{"uncertain", http.StatusServiceUnavailable, "oauth-broker-uncertain", netlog.VerdictDeny, 1},
		{"response", http.StatusBadGateway, "oauth-broker-response", netlog.VerdictDeny, 1},
		{"unrelated-upstream", http.StatusBadGateway, "upstream-error", netlog.VerdictPass, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				if tc.name == "response" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"unexpected":"synthetic-provider-secret"}`)
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}))
			defer upstream.Close()
			p, logPath, config := oauthTestProxy(t, upstream)
			target := "https://platform.claude.com/v1/oauth/token"
			switch tc.name {
			case "denied":
				target += "?spoof=1"
			case "state":
				if err := os.Chmod(config.StoreDir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "unrelated-upstream":
				target = "https://api.anthropic.com/unrelated"
			}
			req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(oauthEnrollmentPayload))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			p.handler("https")(w, req)
			if w.Code != tc.status || calls.Load() != tc.calls {
				t.Fatalf("status = %d, calls = %d; want %d, %d", w.Code, calls.Load(), tc.status, tc.calls)
			}
			events := readAuditEvents(t, logPath)
			if len(events) != 1 || events[0].Status != tc.status || events[0].Verdict != tc.verdict || events[0].Rule != tc.rule {
				t.Fatalf("failure audit = %+v", events)
			}
			if strings.Contains(w.Body.String(), "synthetic-") {
				t.Fatal("provider or bootstrap material reached error response")
			}
		})
	}
}

func TestOAuthExchangesUseFreshConnections(t *testing.T) {
	type connection struct {
		address string
		close   bool
	}
	connections := make(chan connection, 4)
	var exchanges atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		connections <- connection{req.RemoteAddr, req.Close}
		if req.URL.Path == "/v1/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			if exchanges.Add(1) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","token_type":"Bearer","expires_in":30,"scope":"user:inference user:profile","account":{"uuid":"11111111-1111-1111-1111-111111111111","email_address":"synthetic@example.test"},"organization":{"uuid":"22222222-2222-2222-2222-222222222222"}}`)
			return
		}
		_, _ = io.WriteString(w, "ordinary response")
	}))
	defer upstream.Close()
	p, _, _ := oauthTestProxy(t, upstream)
	for i := 0; i < 2; i++ {
		// Re-enrollment after a provider rejection dispatches a second exchange
		// without relying on sleeps or the refresh deduplication window.
		req := httptest.NewRequest(http.MethodPost, "https://platform.claude.com/v1/oauth/token", strings.NewReader(oauthEnrollmentPayload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		p.handler("https")(w, req)
		wantStatus := http.StatusOK
		if i == 0 {
			wantStatus = http.StatusBadRequest
		}
		if w.Code != wantStatus {
			t.Fatalf("token exchange %d = %d", i, w.Code)
		}
	}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		p.handler("https")(w, httptest.NewRequest(http.MethodGet, "https://api.anthropic.com/unrelated", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("ordinary request %d = %d", i, w.Code)
		}
	}
	if len(connections) != 4 {
		t.Fatalf("upstream dispatches = %d, want 4", len(connections))
	}
	first, second, third, fourth := <-connections, <-connections, <-connections, <-connections
	if first.address == second.address || !first.close || !second.close {
		t.Fatalf("token exchanges reused connections: %+v, %+v", first, second)
	}
	if third.address != fourth.address || third.close || fourth.close {
		t.Fatalf("ordinary connection pooling changed: %+v, %+v", third, fourth)
	}
}

func TestOAuthAuditRetainsOnlyKnownMetadata(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = audit.Close() }()
	p := newProxy([]string{"docs.example.com", "sk-ant-oat01-synthetic.docs.example.com", "sk-ant-ort01-synthetic.docs.example.com"}, []string{"blocked.example.com"}, nil, false, audit, testSession)
	p.setBroker(&oauthbroker.Broker{})
	for _, tc := range []struct {
		name                        string
		event                       netlog.Event
		domain, method, contentType string
	}{
		{"known", netlog.Event{Domain: "docs.example.com", Method: "GET", ContentType: "application/json; charset=synthetic-bootstrap"}, "docs.example.com", "GET", "application/json"},
		{"known-denial", netlog.Event{Domain: "blocked.example.com", Method: "POST"}, "blocked.example.com", "POST", ""},
		{"tunnel", newTCPAuditEvent("docs.example.com", netlog.VerdictPass, "allowlist"), "docs.example.com", "", ""},
		{"subdomain", netlog.Event{Domain: "synthetic-bootstrap.docs.example.com", Method: "GET"}, "", "GET", ""},
		{"unknown", netlog.Event{Domain: "synthetic-bootstrap.example.net", Method: "GET"}, "", "GET", ""},
		{"handle-host", netlog.Event{Domain: "sk-ant-oat01-synthetic.docs.example.com", Method: "GET"}, "", "GET", ""},
		{"refresh-handle-host", netlog.Event{Domain: "sk-ant-ort01-synthetic.docs.example.com", Method: "GET"}, "", "GET", ""},
		{"custom-method", netlog.Event{Domain: "docs.example.com", Method: "SYNTHETIC-BOOTSTRAP"}, "docs.example.com", "", ""},
		{"broker", netlog.Event{Domain: "platform.claude.com", Method: "POST", ContentType: "application/json"}, "platform.claude.com", "POST", "application/json"},
		{"custom-media", netlog.Event{Domain: "docs.example.com", Method: "GET", ContentType: "application/synthetic-bootstrap"}, "docs.example.com", "GET", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.event.Path = "/synthetic-bootstrap/sk-ant-oat01-synthetic"
			p.logEvent(tc.event)
			events := readAuditEvents(t, logPath)
			last := events[len(events)-1]
			if last.Domain != tc.domain || last.Method != tc.method || last.ContentType != tc.contentType || last.Path != "" {
				t.Fatalf("audit metadata = %+v", last)
			}
		})
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(logged)), "synthetic") {
		t.Fatal("bootstrap material or handle reached audit log")
	}
}

func TestUnrestrictedBrokerAllowsUnrelatedHostsWithoutReleasingHandles(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls.Add(1); _, _ = io.WriteString(w, "unrelated") }))
	defer upstream.Close()
	p, _, _ := oauthTestProxy(t, upstream)
	p.allowAllDestinations = true
	for _, handle := range []string{"", "sk-ant-oat01-wrong-session"} {
		req := httptest.NewRequest(http.MethodGet, "https://unlisted.example.test/", nil)
		if handle != "" {
			req.Header.Set("Authorization", "Bearer "+handle)
		}
		w := httptest.NewRecorder()
		p.handler("https")(w, req)
		want := http.StatusOK
		if handle != "" {
			want = http.StatusForbidden
		}
		if w.Code != want {
			t.Fatalf("unrestricted request status=%d, want%d", w.Code, want)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("credential-bearing unrelated request reached upstream")
	}
}

func TestBrokerProxyRejectsProtocolUpgrade(t *testing.T) {
	var upgradeRequested atomic.Bool
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Upgrade") != "" || req.Header.Get("Connection") != "" {
			upgradeRequested.Store(true)
		}
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Upgrade", "websocket")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer upstream.Close()
	p, _, _ := oauthTestProxy(t, upstream)
	req := httptest.NewRequest(http.MethodGet, "https://api.anthropic.com/stream", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	p.handler("https")(w, req)
	if upgradeRequested.Load() {
		t.Fatal("broker session forwarded the upgrade request")
	}
	if w.Code != http.StatusBadGateway {
		t.Fatalf("upgrade status = %d, want 502", w.Code)
	}
}

func TestBrokerProxyRelaysUpgradeToUnmediatedHost(t *testing.T) {
	tlsUpstream := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsUpstream.Close()
	p, _, _ := oauthTestProxy(t, tlsUpstream)
	upstream := httptest.NewServer(http.HandlerFunc(echoUpgradeHandler))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	host, _, err := net.SplitHostPort(upstreamHost)
	if err != nil {
		t.Fatal(err)
	}
	p.allowedDomains = append(p.allowedDomains, host)
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()

	assertEchoUpgradeRelayed(t, proxyServer, upstreamHost)
}

func TestBrokerProxyRejectsUpgradeCarryingHandle(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	tlsUpstream := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsUpstream.Close()
	p, _, _ := oauthTestProxy(t, tlsUpstream)
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	host, _, err := net.SplitHostPort(upstreamHost)
	if err != nil {
		t.Fatal(err)
	}
	p.allowedDomains = append(p.allowedDomains, host)
	for _, header := range []string{"Authorization", "Sec-WebSocket-Protocol"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+upstreamHost+"/stream", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set(header, "Bearer sk-ant-oat01-handle")
		w := httptest.NewRecorder()
		p.handler("http")(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s handle upgrade status = %d, want 403", header, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("handle-bearing upgrade reached upstream")
	}
}

func TestOAuthLockFailureIsAuditedAsBrokerDenial(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	p, logPath, cfg := oauthTestProxy(t, upstream)
	if err := os.Remove(cfg.LockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg.LockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := `{"grant_type":"authorization_code","client_id":"9d1c250a-e61b-44d9-88ed-5944d1962f5e","code":"synthetic-code","code_verifier":"synthetic-verifier","redirect_uri":"https://platform.claude.com/oauth/code/callback"}`
	req := httptest.NewRequest(http.MethodPost, "https://platform.claude.com/v1/oauth/token", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.handler("https")(w, req)
	if w.Code != http.StatusServiceUnavailable || calls.Load() != 0 {
		t.Fatalf("lock failure status=%d, upstream calls=%d", w.Code, calls.Load())
	}
	events := readAuditEvents(t, logPath)
	last := events[len(events)-1]
	if last.Verdict != netlog.VerdictDeny || last.Rule != "oauth-broker-state" {
		t.Fatalf("lock failure audit = %+v", last)
	}
}

func TestOAuthForwarderAbortsResponseReflectingCredential(t *testing.T) {
	const access = "synthetic-provider-access-credential"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"`+access+`","refresh_token":"synthetic-provider-refresh-credential","token_type":"Bearer","expires_in":3600,"scope":"user:inference","account":{"uuid":"11111111-1111-1111-1111-111111111111","email_address":"synthetic@example.test"},"organization":{"uuid":"22222222-2222-2222-2222-222222222222"}}`)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: start\n\n")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "data: "+req.Header.Get("Authorization")+"\n\n")
		}
	}))
	defer upstream.Close()
	p, logPath, _ := oauthTestProxy(t, upstream)
	login := httptest.NewRequest(http.MethodPost, "https://platform.claude.com/v1/oauth/token", strings.NewReader(oauthEnrollmentPayload))
	login.Header.Set("Content-Type", "application/json")
	captured := httptest.NewRecorder()
	p.handler("https")(captured, login)
	var token struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(captured.Body.Bytes(), &token); err != nil || token.Access == "" {
		t.Fatalf("capture failed: %d %s", captured.Code, captured.Body)
	}

	request := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token.Access)
	result := httptest.NewRecorder()
	aborted := func() (aborted bool) {
		defer func() { aborted = recover() == http.ErrAbortHandler }()
		p.handler("https")(result, request)
		return false
	}()
	if !aborted || strings.Contains(result.Body.String(), access) {
		t.Fatalf("reflected credential reached the workload: aborted=%v body=%q", aborted, result.Body)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), access) {
		t.Fatal("reflected credential reached the audit log")
	}
	events := readAuditEvents(t, logPath)
	if last := events[len(events)-1]; last.Verdict != netlog.VerdictDeny || last.Rule != "oauth-broker-response" {
		t.Fatalf("tripwire abort audit = %+v", last)
	}
}
