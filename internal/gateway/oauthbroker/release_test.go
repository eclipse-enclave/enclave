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
	"strings"
	"testing"
)

// releaseFixture is a session with a ready grant whose handle the gateway
// releases at allowed and must refuse at forbidden, a credential endpoint of
// the same provider.
type releaseFixture struct {
	broker             *Broker
	handle, real       string
	allowed, forbidden string
}

func releaseFixtures(t *testing.T) map[string]releaseFixture {
	t.Helper()
	claude := fixtureToolBroker(t, "claude")
	enroll(t, claude)
	openai := newOpenAIFixture(t, "codex")
	openaiHandle := openai.login()["access_token"].(string)
	mistral := fixtureToolBroker(t, "mistral-vibe")
	if _, err := mistralSignIn(t, mistral, func() *http.Response { return fixtureResponse(map[string]string{"api_key": fixtureMistralKey}) }); err != nil {
		t.Fatal(err)
	}
	copilot := fixtureToolBroker(t, "opencode")
	if _, err := copilotPoll(t, copilot, func() *http.Response {
		return fixtureResponse(map[string]any{"access_token": fixtureCopilotToken, "token_type": "bearer", "scope": "read:user"})
	}); err != nil {
		t.Fatal(err)
	}
	github := fixtureGitHub(t, githubNativeFixture())
	return map[string]releaseFixture{
		"claude": {claude, claude.registry.Access, fixtureAccess,
			"https://api.anthropic.com/v1/messages", "https://api.anthropic.com/api/oauth/claude_cli/create_api_key"},
		"chatgpt": {openai.b, openaiHandle, openai.access,
			"https://chatgpt.com/backend-api/codex/responses", "https://chatgpt.com/api/accounts/oauth/token"},
		"mistral": {mistral, mistral.mistral.Key, fixtureMistralKey,
			"https://api.mistral.ai/v1/chat/completions", "https://console.mistral.ai/api/api-keys"},
		"copilot": {copilot, copilot.copilot.Token, fixtureCopilotToken,
			"https://api.githubcopilot.com/chat/completions", "https://api.githubcopilot.com/oauth/token"},
		"github": {github, github.github.registry.Handles["alice"], githubRealAlice,
			"https://api.github.com/user", "https://api.github.com/copilot_internal/v2/token"},
	}
}

func releaseRequest(t *testing.T, url, handle string, headers map[string]string, released ...string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+handle)
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return req.WithContext(WithReleasedSecrets(req.Context(), released))
}

func TestEveryReleaseIsRefusedAtCredentialEndpoints(t *testing.T) {
	for name, f := range releaseFixtures(t) {
		t.Run(name, func(t *testing.T) {
			called := false
			_, err := fixtureRoundTrip(f.broker, releaseRequest(t, f.forbidden, f.handle, nil), fixtureTransport(func(*http.Request) (*http.Response, error) {
				called = true
				return rawFixtureResponse([]byte(`{}`)), nil
			}))
			if !errors.Is(err, ErrDenied) || called {
				t.Fatalf("handle released at a credential endpoint: %v, upstream called %t", err, called)
			}
		})
	}
}

func TestEveryReleaseRefusesRawCredentialsBesideTheHandle(t *testing.T) {
	for name, f := range releaseFixtures(t) {
		t.Run(name, func(t *testing.T) {
			called := false
			_, err := fixtureRoundTrip(f.broker, releaseRequest(t, f.allowed, f.handle, map[string]string{"X-Api-Key": "pasted-raw-provider-key"}), fixtureTransport(func(*http.Request) (*http.Response, error) {
				called = true
				return rawFixtureResponse([]byte(`{}`)), nil
			}))
			if !errors.Is(err, ErrDenied) || called {
				t.Fatalf("raw credential beside a handle passed: %v, upstream called %t", err, called)
			}
		})
	}
}

// Responses are guarded against the released credential and against secrets
// the proxy released into the same request by declared rules.
func TestEveryReleaseGuardsItsResponse(t *testing.T) {
	const ruleReleased = "host-configured-provider-secret-0123"
	for name, f := range releaseFixtures(t) {
		for _, echoed := range []string{"handle credential", "rule-released secret"} {
			t.Run(name+"/"+echoed, func(t *testing.T) {
				secret := f.real
				if echoed == "rule-released secret" {
					secret = ruleReleased
				}
				req := releaseRequest(t, f.allowed, f.handle, map[string]string{"X-Api-Key": ruleReleased}, ruleReleased)
				response, err := fixtureRoundTrip(f.broker, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
					return rawFixtureResponse([]byte(`{"echo":"` + secret + `"}`)), nil
				}))
				var body []byte
				if err == nil {
					body, err = io.ReadAll(response.Body)
					_ = response.Body.Close()
				}
				if !errors.Is(err, ErrResponse) || strings.Contains(string(body), secret) {
					t.Fatalf("reflected %s reached the workload: %q, %v", echoed, body, err)
				}
			})
		}
	}
}

// A host-configured token for GitHub's API passes the GitHub CLI adapter to
// the common path for released secrets.
func TestHostConfiguredGitHubSecretIsReleased(t *testing.T) {
	b := fixtureGitHub(t, githubNativeFixture())
	const secret = "ghp_hostconfigured0123456789"
	for _, released := range []bool{true, false} {
		req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		if released {
			req = req.WithContext(WithReleasedSecrets(req.Context(), []string{secret}))
		}
		response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
			return rawFixtureResponse([]byte(`{}`)), nil
		}))
		if released {
			if err != nil {
				t.Fatalf("released host-configured secret refused: %v", err)
			}
			_ = response.Body.Close()
		} else if !errors.Is(err, ErrDenied) {
			t.Fatalf("pasted GitHub token passed: %v", err)
		}
	}
}

// A released Gemini header does not admit a raw key in the query.
func TestReleasedSecretDoesNotAdmitQueryKey(t *testing.T) {
	b := fixtureToolBroker(t, "theia")
	const secret = "AIzaHostConfiguredKey"
	req, err := http.NewRequest(http.MethodGet, "https://generativelanguage.googleapis.com/v1beta/models?key=AIzaPasted", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Goog-Api-Key", secret)
	req = req.WithContext(WithReleasedSecrets(req.Context(), []string{secret}))
	if _, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Error("raw query key reached upstream")
		return rawFixtureResponse(nil), nil
	})); !errors.Is(err, ErrDenied) {
		t.Fatalf("query key beside a released header passed: %v", err)
	}
}
