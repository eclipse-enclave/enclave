// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpointParserAndDenyCannotFallThrough(t *testing.T) {
	s, _ := fixture(t, false)
	calls := 0
	s.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return tokenResponse(`,"expires_in":60`), nil
	})
	cases := []struct{ name, url, scheme, method, content, body string }{
		{"unknown-grant", "https://platform.claude.com/v1/oauth/token", "https", "POST", "application/json", `{"grant_type":"token-exchange"}`},
		{"duplicate-json", "https://platform.claude.com/v1/oauth/token", "https", "POST", "application/json", `{"grant_type":"authorization_code","grant_type":"refresh_token"}`},
		{"form-not-supported", "https://platform.claude.com/v1/oauth/token", "https", "POST", "application/x-www-form-urlencoded", "grant_type=authorization_code&code=one&code=two"},
		{"trailing", "https://platform.claude.com/v1/oauth/token", "https", "POST", "application/json", `{"grant_type":"authorization_code","code":"test"}{}`},
		{"raw-refresh", "https://platform.claude.com/v1/oauth/token", "https", "POST", "application/json", `{"grant_type":"refresh_token","refresh_token":"synthetic-raw"}`},
		{"foreign-placeholder", "https://platform.claude.com/v1/oauth/token", "https", "POST", "application/json", `{"grant_type":"refresh_token","refresh_token":"ENCLAVE_SECRET_foreign"}`},
		{"marker-in-code", "https://platform.claude.com/v1/oauth/token", "https", "POST", "application/json", `{"grant_type":"authorization_code","code":"ENCLAVE_SECRET_thing"}`},
		{"missing-code", "https://platform.claude.com/v1/oauth/token", "https", "POST", "application/json", `{"grant_type":"authorization_code"}`},
		{"http", "http://platform.claude.com/v1/oauth/token", "http", "POST", "application/json", `{}`},
		{"port", "https://platform.claude.com:8443/v1/oauth/token", "https", "POST", "application/json", `{}`},
		{"query", "https://platform.claude.com/v1/oauth/token?route=other", "https", "POST", "application/json", `{}`},
		{"encoded", "https://platform.claude.com/v1/oauth/%74oken", "https", "POST", "application/json", `{}`},
		{"method", "https://platform.claude.com/v1/oauth/token", "https", "GET", "application/json", `{}`},
		{"denied", "https://api.anthropic.com/api/oauth/claude_cli/create_api_key", "https", "POST", "application/json", `{}`},
		{"encoded-deny", "https://api.anthropic.com/api/oauth/claude_cli/%63reate_api_key", "https", "POST", "application/json", `{}`},
		{"large", "https://platform.claude.com/v1/oauth/token", "https", "POST", "application/json", strings.Repeat(" ", maxBodyBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.url, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.content)
			resp, handled := s.Intercept(req, tc.scheme)
			if !handled || resp.StatusCode != 403 {
				t.Fatal("invalid endpoint request fell through")
			}
			_ = resp.Body.Close()
		})
	}
	if calls != 0 {
		t.Fatal("refused exchange dispatched")
	}
	req := httptest.NewRequest("POST", "https://PLATFORM.CLAUDE.COM:443/v1/oauth/token", strings.NewReader(`{"grant_type":"authorization_code","code":"synthetic-code","code_verifier":"synthetic-verifier"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, handled := s.Intercept(req, "https")
	if !handled || resp.StatusCode != 200 || calls != 1 {
		t.Fatal("JSON code exchange rejected")
	}
	_ = resp.Body.Close()
	req = httptest.NewRequest("GET", "https://api.anthropic.com/v1/messages", nil)
	if _, handled := s.Intercept(req, "https"); handled {
		t.Fatal("ordinary API request intercepted")
	}

}
