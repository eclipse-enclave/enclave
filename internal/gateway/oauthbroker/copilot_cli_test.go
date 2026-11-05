// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureCopilotCLIScopes = "read:user,read:org,repo,gist,codespace,write:plugin_gateway_connections"

func copilotCLIRequest(t *testing.T, path string, fields url.Values) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://github.com"+path, strings.NewReader(fields.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func captureCopilotCLI(t *testing.T, broker *Broker, user map[string]any) (*http.Response, error) {
	t.Helper()
	req := copilotCLIRequest(t, githubTokenPath, url.Values{
		"client_id": {copilotCLIClientID}, "device_code": {copilotDeviceCode()}, "grant_type": {githubDeviceGrant},
	})
	return fixtureRoundTrip(broker, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
		switch out.URL.Path {
		case githubTokenPath:
			return fixtureResponse(map[string]string{"access_token": fixtureCopilotToken, "token_type": "bearer", "scope": fixtureCopilotCLIScopes}), nil
		case copilotUserPath:
			if out.Method != http.MethodGet || out.Header.Get("Authorization") != "Bearer "+fixtureCopilotToken {
				t.Fatal("account lookup did not use the gateway-private grant")
			}
			return fixtureResponse(user), nil
		default:
			t.Fatalf("unexpected request: %s", out.URL)
			return nil, io.EOF
		}
	}))
}

func TestCopilotCLICaptureAndNativeReload(t *testing.T) {
	broker := fixtureToolBroker(t, "copilot")
	device := copilotCLIRequest(t, githubDevicePath, url.Values{"client_id": {copilotCLIClientID}, "scope": {fixtureCopilotCLIScopes}})
	response, err := fixtureRoundTrip(broker, device, fixtureTransport(func(out *http.Request) (*http.Response, error) {
		if err := out.ParseForm(); err != nil || out.Form.Get("scope") != fixtureCopilotCLIScopes {
			t.Fatal("native comma-separated scopes were changed")
		}
		return fixtureResponse(map[string]any{"device_code": copilotDeviceCode(), "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	response, err = captureCopilotCLI(t, broker, map[string]any{"login": "engine-check", "copilot_plan": "individual"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || strings.Contains(string(data), fixtureCopilotToken) || !strings.Contains(string(data), broker.copilot.Token) {
		t.Fatalf("captured response = %s, %v", data, err)
	}
	grant, err := broker.readCopilotGrant()
	if err != nil || grant.Login != "engine-check" {
		t.Fatalf("account was not persisted: %+v, %v", grant, err)
	}
	for range 2 {
		broker = reopenAsTool(t, broker, "copilot")
		view := filepath.Join(t.TempDir(), "view")
		if err := os.Mkdir(view, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := broker.InitializeView(view); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(view, "config.json"))
		if err != nil || strings.Contains(string(data), fixtureCopilotToken) {
			t.Fatalf("native view contains a grant: %s, %v", data, err)
		}
		var native struct {
			Tokens map[string]struct{ Token string }    `json:"authTokens"`
			Last   struct{ Host, Login string }         `json:"lastLoggedInUser"`
			Users  []struct{ Host, Login, Kind string } `json:"loggedInUsers"`
		}
		if err := json.Unmarshal(data, &native); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"https://github.com:engine-check:github", "https://github.com:engine-check"} {
			if native.Tokens[key].Token != broker.copilot.Token {
				t.Fatalf("native token key %q does not contain this session's handle", key)
			}
		}
		if native.Last.Host != "https://github.com" || native.Last.Login != "engine-check" || len(native.Users) != 1 || native.Users[0].Kind != "githubDotCom" {
			t.Fatalf("native account selection = %+v", native)
		}
	}
}

func TestCopilotCLIReleaseIsPathScoped(t *testing.T) {
	broker := fixtureToolBroker(t, "copilot")
	response, err := captureCopilotCLI(t, broker, map[string]any{"login": "engine-check"})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	for _, test := range []struct {
		method, target string
		allowed        bool
	}{
		{http.MethodGet, "https://api.github.com/copilot_internal/user", true},
		{http.MethodGet, "https://api.githubcopilot.com/models", true},
		{http.MethodPost, "https://api.individual.githubcopilot.com/chat/completions", true},
		{http.MethodGet, "https://api.github.com/user", false},
		{http.MethodGet, "https://api.github.com/repos/acme/private", false},
		{http.MethodPut, "https://api.github.com/repos/acme/private/contents/file", false},
		{http.MethodPost, "https://api.github.com/graphql", false},
		{http.MethodPost, "https://api.github.com/gists", false},
		{http.MethodGet, "https://api.github.com/copilot_internal/v2/token", false},
		{http.MethodPost, "https://api.github.com/copilot_internal/user", false},
		{http.MethodGet, "https://api.github.com/copilot_internal/user?token=x", false},
		{http.MethodGet, "https://api.github.com:8443/copilot_internal/user", false},
		{http.MethodGet, "https://api.github.com/copilot_internal/%75ser", false},
		{http.MethodGet, "https://unrelated.example.test/", false},
	} {
		t.Run(test.method+" "+test.target, func(t *testing.T) {
			req, err := http.NewRequest(test.method, test.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+broker.copilot.Token)
			called := false
			response, err := fixtureRoundTrip(broker, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
				called = true
				if out.Header.Get("Authorization") != "Bearer "+fixtureCopilotToken {
					t.Fatal("upstream did not receive the captured grant")
				}
				response := fixtureResponse(map[string]any{"login": "engine-check", "copilot_plan": "individual", "future_field": "omit"})
				response.Header.Set("Set-Cookie", "ignored=value; Secure")
				return response, nil
			}))
			if !test.allowed {
				if called || !errors.Is(err, ErrDenied) {
					t.Fatalf("unapproved release: dispatched=%t error=%v", called, err)
				}
				return
			}
			if err != nil || !called {
				t.Fatalf("approved release: dispatched=%t error=%v", called, err)
			}
			data, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if req.URL.Path == copilotUserPath && (strings.Contains(string(data), "future_field") || response.Header.Get("Set-Cookie") != "") {
				t.Fatal("account response retained unapproved fields or headers")
			}
		})
	}
}

func TestCopilotCLICaptureRequiresVerifiedAccount(t *testing.T) {
	for _, user := range []map[string]any{{}, {"login": "invalid_name"}, {"login": "engine-check", "debug": fixtureCopilotToken}} {
		broker := fixtureToolBroker(t, "copilot")
		response, err := captureCopilotCLI(t, broker, user)
		if response != nil || !errors.Is(err, ErrResponse) {
			t.Fatalf("unsafe account response accepted: %v", err)
		}
		if grant, err := broker.readCopilotGrant(); err != nil || grant != nil {
			t.Fatalf("failed capture committed a grant: %+v, %v", grant, err)
		}
	}
}

func TestCopilotCLIDeclaredKeyEntitlementGuard(t *testing.T) {
	const secret = "github_pat_host_configured_fixture"
	for _, test := range []struct {
		name       string
		registered bool
		extraRaw   bool
		reflect    bool
		wantError  error
	}{
		{"declared key", true, false, false, nil},
		{"unregistered raw key", false, false, false, ErrDenied},
		{"mixed raw key", true, true, false, ErrDenied},
		{"reflected key", true, false, true, ErrResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker := fixtureToolBroker(t, "copilot")
			req, err := http.NewRequest(http.MethodGet, "https://api.github.com"+copilotUserPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+secret)
			if test.registered {
				req = req.WithContext(WithReleasedSecrets(req.Context(), []string{secret}))
			}
			if test.extraRaw {
				req.Header.Set("X-Api-Key", "unregistered-key")
			}
			called := false
			response, err := fixtureRoundTrip(broker, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
				called = true
				if out.Header.Get("Authorization") != "Bearer "+secret {
					t.Fatal("declared key was lost")
				}
				fields := map[string]any{"login": "engine-check"}
				if test.reflect {
					fields["debug"] = secret
				}
				return fixtureResponse(fields), nil
			}))
			if !errors.Is(err, test.wantError) || (errors.Is(test.wantError, ErrDenied) && called) {
				t.Fatalf("declared-key guard: dispatched=%t error=%v want=%v", called, err, test.wantError)
			}
			if response != nil {
				_ = response.Body.Close()
			}
			if grant, err := broker.readCopilotGrant(); err != nil || grant != nil {
				t.Fatal("declared env credential was imported as a captured grant")
			}
		})
	}
}
