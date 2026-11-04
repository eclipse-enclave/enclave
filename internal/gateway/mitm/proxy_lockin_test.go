// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"enclave/internal/model"
	"enclave/internal/netlog"
)

// TestLockinRestrictedProxyDecisions pins the restricted proxy's per-request
// verdicts: which hosts pass, which are denied and why, and which HTTPS hosts
// are intercepted rather than tunnelled.
func TestLockinRestrictedProxyDecisions(t *testing.T) {
	allowed := []string{"example.com", "api.service.io"}
	denied := []string{"tracking.example.com"}
	rules := []model.SecretReleaseEntry{{
		SecretID:    "api-key",
		Placeholder: "ENCLAVE_SECRET_abc",
		Value:       "real-secret",
		Hosts:       []string{"api.service.io"},
		Header:      "x-api-key",
	}}

	t.Run("host permitted", func(t *testing.T) {
		p := newProxy(allowed, denied, rules, false, nil, testSession)
		for host, want := range map[string]bool{
			"example.com":                  true,
			"www.example.com":              true,
			"api.service.io":               true,
			"service.io":                   false,
			"tracking.example.com":         false,
			"metrics.tracking.example.com": false,
			"evil.test":                    false,
			"example.com.evil.test":        false,
		} {
			if got := p.hostPermitted(host); got != want {
				t.Errorf("hostPermitted(%q) = %v, want %v", host, got, want)
			}
		}
	})

	t.Run("https interception", func(t *testing.T) {
		for _, tc := range []struct {
			host   string
			forced bool
			want   bool
		}{
			{"api.service.io", false, true},
			{"example.com", false, false},
			{"example.com", true, true},
		} {
			if got := shouldMITM(tc.host, rules, tc.forced); got != tc.want {
				t.Errorf("shouldMITM(%q, forced=%v) = %v, want %v", tc.host, tc.forced, got, tc.want)
			}
		}
	})

	t.Run("plaintext denials", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			host   string
			header string
			status int
			rule   string
		}{
			{"missing host", "", "", http.StatusBadRequest, "invalid-host"},
			{"not allowlisted", "evil.test", "", http.StatusForbidden, "allowlist"},
			{"denylisted subdomain of allowed parent", "tracking.example.com", "", http.StatusForbidden, "allowlist"},
			{"secret release over plaintext", "api.service.io", "ENCLAVE_SECRET_abc", http.StatusForbidden, "secret-injection"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				logPath := filepath.Join(t.TempDir(), "network.log")
				audit, err := netlog.NewAppender(logPath)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = audit.Close() }()
				p := newProxy(allowed, denied, rules, false, audit, testSession)
				req := httptest.NewRequest(http.MethodGet, "http://placeholder.invalid/path", nil)
				req.Host = tc.host
				req.URL.Host = tc.host
				if tc.header != "" {
					req.Header.Set("X-Api-Key", tc.header)
				}
				recorder := httptest.NewRecorder()

				p.handler("http")(recorder, req)

				if recorder.Code != tc.status {
					t.Fatalf("status = %d, want %d", recorder.Code, tc.status)
				}
				events := readAuditEvents(t, logPath)
				if len(events) != 1 {
					t.Fatalf("audit events = %d, want 1", len(events))
				}
				event := events[0]
				if event.Type != netlog.TypeHTTP || event.Verdict != netlog.VerdictDeny || event.Rule != tc.rule || event.Status != tc.status || event.Session != testSession {
					t.Fatalf("event = %+v, want http deny rule=%s status=%d", event, tc.rule, tc.status)
				}
			})
		}
	})
}
