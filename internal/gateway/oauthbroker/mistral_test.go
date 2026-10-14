// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureMistralKey = "real-mistral-api-key-0123456789"

func mistralSignIn(t *testing.T, b *Broker, upstream func() *http.Response) (string, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://console.mistral.ai/api/vibe/sign-in/proc_1/exchange", strings.NewReader(`{"exchange_token":"synthetic-exchange","code_verifier":"synthetic-verifier"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := fixtureRoundTrip(b, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(out.Body)
		if !strings.Contains(string(data), "synthetic-exchange") {
			t.Error("exchange not forwarded")
		}
		return upstream(), nil
	}))
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if strings.Contains(string(data), fixtureMistralKey) {
		t.Fatal("sign-in response carries the real key")
	}
	return string(data), err
}

func TestMistralSignInCapturesKeyAndReleasesHandle(t *testing.T) {
	b := fixtureToolBroker(t, "mistral-vibe")
	body, err := mistralSignIn(t, b, func() *http.Response { return fixtureResponse(map[string]string{"api_key": fixtureMistralKey}) })
	if err != nil || !strings.Contains(body, b.mistral.Key) {
		t.Fatalf("sign-in = %q, %v", body, err)
	}
	for _, tc := range []struct {
		url      string
		released bool
	}{
		{"https://api.mistral.ai/v1/chat/completions", true},
		{"https://console.mistral.ai/api/vibe/whoami", true},
		{"https://chat.mistral.ai/api/v1/code/managed-config", true},
		{"https://api.openai.com/v1/responses", false},
		{"https://reflect.example.test/", false},
	} {
		req, err := http.NewRequest(http.MethodGet, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+b.mistral.Key)
		var seen string
		response, err := fixtureRoundTrip(b, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
			seen = out.Header.Get("Authorization")
			return rawFixtureResponse([]byte(`{}`)), nil
		}))
		if tc.released {
			if err != nil || seen != "Bearer "+fixtureMistralKey {
				t.Fatalf("%s: release = %v, upstream saw %q", tc.url, err, seen)
			}
			_ = response.Body.Close()
		} else if !errors.Is(err, ErrDenied) || seen != "" {
			t.Fatalf("%s: handle escaped: %v, %q", tc.url, err, seen)
		}
	}
	if _, err := mistralSignIn(t, b, func() *http.Response {
		return fixtureResponse(map[string]string{"api_key": "another-real-key-0123456789"})
	}); !errors.Is(err, ErrDenied) {
		t.Fatalf("second sign-in replaced the key behind sibling handles: %v", err)
	}

	next := reopenAsTool(t, b, "mistral-vibe")
	view := filepath.Join(t.TempDir(), "view")
	if err := os.Mkdir(view, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := next.InitializeView(view); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(view, "vibe.env"))
	if err != nil || string(env) != "MISTRAL_API_KEY='"+next.mistral.Key+"'\n" {
		t.Fatalf("vibe.env = %q, %v", env, err)
	}
}

func TestMistralSignInFailuresFailClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		upstream func() *http.Response
		wantBody string
		wantErr  error
	}{
		"ineligible": {upstream: func() *http.Response {
			response := fixtureResponse(map[string]any{"detail": "workspace " + fixtureMistralKey, "error_code": 9004})
			response.StatusCode = http.StatusForbidden
			return response
		}, wantBody: `{"error_code":9004}`},
		"unknown field": {upstream: func() *http.Response {
			return fixtureResponse(map[string]any{"api_key": fixtureMistralKey, "workspace": "w"})
		}, wantErr: ErrResponse},
		"server error": {upstream: func() *http.Response {
			response := fixtureResponse(map[string]any{"detail": "oops"})
			response.StatusCode = http.StatusBadGateway
			return response
		}, wantErr: ErrResponse},
	} {
		t.Run(name, func(t *testing.T) {
			b := fixtureToolBroker(t, "mistral-vibe")
			body, err := mistralSignIn(t, b, tc.upstream)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("sign-in = %q, %v", body, err)
				}
			} else if err != nil || body != tc.wantBody {
				t.Fatalf("sign-in = %q, %v", body, err)
			}
			g, err := b.readMistralGrant()
			if err != nil || g == nil || g.Status != "uncertain" || g.Key != "" {
				t.Fatalf("failed sign-in state = %+v, %v", g, err)
			}
			// A failed sign-in can be retried.
			if _, err := mistralSignIn(t, b, func() *http.Response { return fixtureResponse(map[string]string{"api_key": fixtureMistralKey}) }); err != nil {
				t.Fatalf("retry refused: %v", err)
			}
		})
	}
}
