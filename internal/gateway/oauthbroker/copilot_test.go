// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureCopilotToken = "gho_realcopilot0123456789abcdef"

func copilotRequest(t *testing.T, path string, fields map[string]string) *http.Request {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://github.com"+path, strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return req
}

func copilotDeviceCode() string {
	digest := sha256.Sum256([]byte("copilot-device"))
	return hex.EncodeToString(digest[:20])
}

func copilotPoll(t *testing.T, b *Broker, upstream func() *http.Response) (map[string]any, error) {
	t.Helper()
	req := copilotRequest(t, githubTokenPath, map[string]string{"client_id": copilotClientID, "device_code": copilotDeviceCode(), "grant_type": githubDeviceGrant})
	response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) { return upstream(), nil }))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || strings.Contains(string(data), fixtureCopilotToken) {
		t.Fatalf("poll response = %q, %v", data, err)
	}
	var body map[string]any
	return body, json.Unmarshal(data, &body)
}

func TestCopilotDeviceLoginCapturesToken(t *testing.T) {
	b := fixtureToolBroker(t, "opencode")
	device := copilotRequest(t, githubDevicePath, map[string]string{"client_id": copilotClientID, "scope": "read:user"})
	var forwarded string
	response, err := fixtureRoundTrip(b, device, fixtureTransport(func(out *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(out.Body)
		forwarded = string(data)
		return fixtureResponse(map[string]any{"device_code": copilotDeviceCode(), "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5}), nil
	}))
	if err != nil || !strings.Contains(forwarded, copilotClientID) {
		t.Fatalf("device code request = %v, forwarded %q", err, forwarded)
	}
	_ = response.Body.Close()

	pending, err := copilotPoll(t, b, func() *http.Response {
		return fixtureResponse(map[string]any{"error": "authorization_pending", "error_description": "wait " + fixtureCopilotToken})
	})
	if err != nil || pending["error"] != "authorization_pending" || pending["error_description"] != nil {
		t.Fatalf("pending poll = %v, %v", pending, err)
	}
	token, err := copilotPoll(t, b, func() *http.Response {
		return fixtureResponse(map[string]any{"access_token": fixtureCopilotToken, "token_type": "bearer", "scope": "read:user"})
	})
	if err != nil || token["access_token"] != b.copilot.Token {
		t.Fatalf("token poll = %v, %v", token, err)
	}

	for _, tc := range []struct {
		url      string
		released bool
	}{
		{"https://api.githubcopilot.com/chat/completions", true},
		{"https://api.individual.githubcopilot.com/models", true},
		{"https://api.github.com/user", false},
		{"https://reflect.example.test/", false},
	} {
		req, err := http.NewRequest(http.MethodPost, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+b.copilot.Token)
		var seen string
		response, err := fixtureRoundTrip(b, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
			seen = out.Header.Get("Authorization")
			return rawFixtureResponse([]byte(`{}`)), nil
		}))
		if tc.released {
			if err != nil || seen != "Bearer "+fixtureCopilotToken {
				t.Fatalf("%s: release = %v, upstream saw %q", tc.url, err, seen)
			}
			_ = response.Body.Close()
		} else if !errors.Is(err, ErrDenied) || seen != "" {
			t.Fatalf("%s: handle escaped: %v, %q", tc.url, err, seen)
		}
	}
	if _, err := copilotPoll(t, b, func() *http.Response {
		return fixtureResponse(map[string]any{"access_token": "gho_another0123456789", "token_type": "bearer", "scope": "read:user"})
	}); !errors.Is(err, ErrDenied) {
		t.Fatalf("second login replaced the token behind sibling handles: %v", err)
	}

	next := reopenAsTool(t, b, "opencode")
	view := filepath.Join(t.TempDir(), "view")
	if err := os.Mkdir(view, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := next.InitializeView(view); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(view, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]map[string]any
	if err := json.Unmarshal(raw, &auth); err != nil {
		t.Fatal(err)
	}
	entry := auth["github-copilot"]
	if entry["type"] != "oauth" || entry["access"] != next.copilot.Token || entry["refresh"] != next.copilot.Token || strings.Contains(string(raw), fixtureCopilotToken) {
		t.Fatalf("opencode Copilot entry = %v", entry)
	}
}

func TestCopilotLeavesOtherClientsLogins(t *testing.T) {
	b := fixtureToolBroker(t, "opencode")
	req := copilotRequest(t, githubDevicePath, map[string]string{"client_id": githubClientID, "scope": "repo"})
	if _, handled, _ := (copilotAdapter{b: b}).roundTrip(req, fixtureTransport(nil), fixtureTransport(nil)); handled {
		t.Fatal("Copilot adapter claimed the GitHub CLI's login")
	}
	if data, err := io.ReadAll(req.Body); err != nil || !strings.Contains(string(data), githubClientID) {
		t.Fatal("request body consumed for the next adapter")
	}
	// Without the GitHub CLI adapter, the login is refused rather than passed.
	if _, err := fixtureRoundTrip(b, copilotRequest(t, githubDevicePath, map[string]string{"client_id": githubClientID, "scope": "repo"}), fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Error("uncaptured GitHub login dispatched")
		return nil, io.EOF
	})); !errors.Is(err, ErrDenied) {
		t.Fatalf("uncaptured login = %v", err)
	}
}
