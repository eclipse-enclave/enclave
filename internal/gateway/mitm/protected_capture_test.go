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
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/gateway/oauthbroker"
	"enclave/internal/netcapture"
)

func TestProtectedCaptureDiscardsDeniedCredentials(t *testing.T) {
	const access = "synthetic-provider-access-credential"
	upstream, _, _ := websocketUpstream(t, access)
	defer upstream.Close()
	p, _, _ := oauthTestProxy(t, upstream)
	dir := t.TempDir()
	var err error
	p.captures, err = netcapture.Open(netcapture.Options{Dir: dir, Session: testSession})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "https://platform.claude.com/v1/oauth/token", strings.NewReader(oauthEnrollmentPayload))
	login.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.handler("https")(w, login)
	if w.Code != http.StatusOK {
		t.Fatalf("login %d", w.Code)
	}
	assertClean := func(t *testing.T) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(access)) {
				t.Errorf("broker-owned synthetic credential persisted in %s", entry.Name())
			}
		}
	}
	t.Run("sanitized-token-response", func(t *testing.T) { assertClean(t) })
	for _, where := range []string{"header", "query", "body"} {
		t.Run(where, func(t *testing.T) {
			dir = t.TempDir()
			p.captures, err = netcapture.Open(netcapture.Options{Dir: dir, Session: testSession})
			if err != nil {
				t.Fatal(err)
			}
			target := "https://api.anthropic.com/v1/messages"
			var body io.Reader
			if where == "query" {
				target = "https://platform.claude.com/v1/oauth/token?key=" + access
			}
			if where == "body" {
				target = "https://platform.claude.com/v1/oauth/token"
				body = strings.NewReader(`{"refresh_token":"` + access + `"}`)
			}
			req := httptest.NewRequest(http.MethodPost, target, body)
			if where == "header" {
				req.Header.Set("Authorization", "Bearer "+access)
			}
			if where == "body" {
				req.Header.Set("Content-Type", "application/json")
			}
			denied := httptest.NewRecorder()
			p.handler("https")(denied, req)
			if denied.Code != http.StatusForbidden {
				t.Fatalf("status %d, want broker denial", denied.Code)
			}
			assertClean(t)
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatal("broker denial left capture files")
			}
		})
	}
}

func TestProtectedWebSocketCapturePaths(t *testing.T) {
	for _, tc := range []struct {
		name, host, message string
		denied              bool
	}{
		{"mediated-clean", "api.anthropic.com", "hello", false},
		{"mediated-tripwire", "api.anthropic.com", "reflect", true},
		{"unmediated-clean", "unmediated.test", "hello", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const access = "synthetic-provider-access-credential"
			upstream, _, _ := websocketUpstream(t, access)
			defer upstream.Close()
			p, logPath, _ := oauthTestProxy(t, upstream)
			p.allowedDomains = append(p.allowedDomains, "unmediated.test")
			p.upgradeTransport = p.transport.Clone()
			t.Cleanup(p.upgradeTransport.CloseIdleConnections)
			login := httptest.NewRequest(http.MethodPost, "https://platform.claude.com/v1/oauth/token", strings.NewReader(oauthEnrollmentPayload))
			login.Header.Set("Content-Type", "application/json")
			result := httptest.NewRecorder()
			p.handler("https")(result, login)
			var token struct {
				Access string `json:"access_token"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &token); err != nil || token.Access == "" {
				t.Fatal("login failed")
			}
			dir := t.TempDir()
			var err error
			p.captures, err = netcapture.Open(netcapture.Options{Dir: dir, Session: testSession})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(p.handler("https"))
			defer server.Close()
			conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			auth := ""
			if tc.host == "api.anthropic.com" {
				auth = "Authorization: Bearer " + token.Access + "\r\n"
			}
			_, err = io.WriteString(conn, "GET /v1/stream HTTP/1.1\r\nHost: "+tc.host+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+auth+"\r\n")
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, nil)
			if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("upgrade failed: %v", err)
			}
			if _, err = conn.Write(websocketFrame(1, true, true, []byte(tc.message))); err != nil {
				t.Fatal(err)
			}
			_, reply, err := readWebSocketFrame(reader)
			if tc.denied {
				if err == nil {
					t.Fatal("tripwire relayed reflected credential")
				}
			} else if err != nil || string(reply) != "echo: "+tc.message {
				t.Fatalf("reply %q error %v", reply, err)
			}
			_ = conn.Close()
			deadline := time.Now().Add(3 * time.Second)
			for {
				events := readAuditEvents(t, logPath)
				if len(events) == 2 {
					last := events[1]
					if tc.denied {
						record, err := netcapture.Load(netcapture.RecordPath(dir, last.CaptureID))
						if err != nil || last.Verdict != "deny" || record.Error != "oauth-broker-response" || record.WebSocket == nil || !record.WebSocket.Truncated {
							t.Fatalf("tripwire capture: %+v, %v", record, err)
						}
						entries, err := os.ReadDir(dir)
						if err != nil {
							t.Fatal(err)
						}
						for _, entry := range entries {
							raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
							if err != nil {
								t.Fatal(err)
							}
							if bytes.Contains(raw, []byte(access)) {
								t.Fatal("tripwire capture contains credential")
							}
						}
					} else {
						record, err := netcapture.Load(netcapture.RecordPath(dir, last.CaptureID))
						if err != nil || record.WebSocket == nil || record.WebSocket.StoredMessages != 2 {
							t.Fatalf("missing upgrade capture: %v", err)
						}
						if strings.Contains(record.Request.Headers.Get("Authorization"), access) {
							t.Fatal("released credential in handshake capture")
						}
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("upgrade audit event missing")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func assertNoCapturedCredentials(t *testing.T, dir string, secrets ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range secrets {
			if bytes.Contains(bytes.ToLower(raw), bytes.ToLower([]byte(secret))) {
				t.Errorf("capture file %s contains a broker credential", entry.Name())
			}
		}
	}
}

func TestCopilotCaptureThroughForwarder(t *testing.T) {
	for _, mode := range []string{"clean", "header-value", "header-name", "error-header-name", "upgrade-header-name", "malformed-header", "canonical-name", "canonical-error-name", "canonical-upgrade-name"} {
		t.Run(mode, func(t *testing.T) {
			const credential = "gho_reAlcoPilot0123456789ABcdef"
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if req.URL.Path == "/login/oauth/access_token" {
					_, _ = io.WriteString(w, `{"access_token":"`+credential+`","token_type":"bearer","scope":"read:user"}`)
					return
				}
				if req.Header.Get("Authorization") != "Bearer "+credential {
					t.Error("Copilot release missing")
				}
				if strings.HasPrefix(mode, "canonical-") {
					conn, rw, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.Close() }()
					status := "200 OK"
					extra := "Content-Length: 0\r\n"
					if mode == "canonical-error-name" {
						status = "401 Unauthorized"
					}
					if mode == "canonical-upgrade-name" {
						status = "101 Switching Protocols"
						extra = "Connection: Upgrade\r\nUpgrade: websocket\r\n"
					}
					_, _ = rw.WriteString("HTTP/1.1 " + status + "\r\nX-" + credential + ": present\r\n" + extra + "\r\n")
					_ = rw.Flush()
					return
				}
				if mode == "malformed-header" {
					conn, rw, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.Close() }()
					_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nX_" + credential + "\x00: present\r\nContent-Length: 0\r\n\r\n")
					_ = rw.Flush()
					return
				}
				switch mode {
				case "header-value":
					w.Header().Set("X-Echo", credential)
				case "header-name", "error-header-name", "upgrade-header-name":
					w.Header().Set("X_"+credential, "present")
				}
				if mode == "upgrade-header-name" {
					w.Header().Set("Connection", "Upgrade")
					w.Header().Set("Upgrade", "websocket")
					w.WriteHeader(http.StatusSwitchingProtocols)
					return
				}
				if mode == "error-header-name" {
					w.WriteHeader(http.StatusUnauthorized)
				}
				_, _ = io.WriteString(w, `{"reply":"ok"}`)
			}))
			defer upstream.Close()
			p, logPath, config := oauthTestProxy(t, upstream)
			p.upgradeTransport = p.transport.Clone()
			t.Cleanup(p.upgradeTransport.CloseIdleConnections)
			config.Tool = "opencode"
			broker, err := oauthbroker.Open(config)
			if err != nil {
				t.Fatal(err)
			}
			p.setBroker(broker)
			p.allowedDomains = append(p.allowedDomains, "github.com", "api.githubcopilot.com")
			dir := t.TempDir()
			p.captures, err = netcapture.Open(netcapture.Options{Dir: dir, Session: testSession})
			if err != nil {
				t.Fatal(err)
			}
			payload := `{"client_id":"Ov23li8tweQw6odWQebz","device_code":"0123456789abcdef0123456789abcdef01234567","grant_type":"urn:ietf:params:oauth:grant-type:device_code"}`
			req := httptest.NewRequest(http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			p.handler("https")(response, req)
			var token struct {
				Access string `json:"access_token"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil || token.Access == "" || token.Access == credential {
				t.Fatalf("Copilot login status %d", response.Code)
			}
			req = httptest.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+token.Access)
			if mode == "upgrade-header-name" || mode == "canonical-upgrade-name" {
				req.Method = http.MethodGet
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			response = httptest.NewRecorder()
			p.handler("https")(response, req)
			wantStatus := http.StatusOK
			if mode != "clean" {
				wantStatus = http.StatusBadGateway
			}
			if response.Code != wantStatus {
				t.Errorf("Copilot API status %d, want %d", response.Code, wantStatus)
			}
			events := readAuditEvents(t, logPath)
			if len(events) != 2 || events[0].CaptureID == "" || events[1].CaptureID == "" {
				t.Fatal("missing Copilot captures")
			}
			record, err := netcapture.Load(netcapture.RecordPath(dir, events[1].CaptureID))
			if err != nil {
				t.Fatal(err)
			}
			if record.Request.Headers.Get("Authorization") != "Bearer "+token.Access {
				t.Fatal("capture did not preserve the handle")
			}
			wantError := "oauth-broker-response"
			if mode == "malformed-header" {
				wantError = "upstream request failed"
			}
			if mode != "clean" && (record.Response != nil || record.Error != wantError) {
				t.Errorf("reflected header was not blocked by the broker: %+v", record)
			}
			assertNoCapturedCredentials(t, dir, credential)
		})
	}
}

func TestProviderTokenShapeInCaptureHeaders(t *testing.T) {
	const minted = "glpat-MixedCase0123456789ABcdef"
	for _, tc := range []struct {
		name            string
		inName, upgrade bool
	}{
		{"http-value", false, false}, {"http-name", true, false},
		{"upgrade-value", false, true}, {"upgrade-name", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if tc.inName {
					w.Header().Set("X-"+minted, "present")
				} else {
					w.Header().Set("X-Echo", minted)
				}
				if tc.upgrade {
					w.Header().Set("Connection", "Upgrade")
					w.Header().Set("Upgrade", "websocket")
					w.WriteHeader(http.StatusSwitchingProtocols)
				}
			}))
			defer upstream.Close()
			p, logPath, _ := oauthTestProxy(t, upstream)
			p.allowedDomains = append(p.allowedDomains, "gitlab.com")
			p.upgradeTransport = p.transport.Clone()
			t.Cleanup(p.upgradeTransport.CloseIdleConnections)
			req := httptest.NewRequest(http.MethodGet, "https://gitlab.com/api/v4/user", nil)
			if tc.upgrade {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			response := httptest.NewRecorder()
			p.handler("https")(response, req)
			if response.Code != http.StatusBadGateway {
				t.Errorf("provider-shaped header status %d, want 502", response.Code)
			}
			event := readAuditEvents(t, logPath)[0]
			if event.CaptureID == "" {
				t.Fatal("missing failure capture")
			}
			dir := filepath.Join(filepath.Dir(logPath), "capture")
			record, err := netcapture.Load(netcapture.RecordPath(dir, event.CaptureID))
			if err != nil {
				t.Fatal(err)
			}
			if record.Response != nil || record.Error != "oauth-broker-response" {
				t.Errorf("provider-shaped header reached capture: %+v", record)
			}
			assertNoCapturedCredentials(t, dir, minted)
		})
	}
}
