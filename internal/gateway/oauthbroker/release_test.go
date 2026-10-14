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
	"net/http/httptest"
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

// Trailers arrive with the end of the body, after the headers were checked;
// the proxy forwards them once the body reads cleanly.
func TestMediationChecksLateTrailers(t *testing.T) {
	const secret = "released-provider-secret-0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Trailer", "X-Echo")
		_, _ = io.WriteString(w, `{"ok":true}`)
		w.Header().Set("X-Echo", req.Header.Get("X-Api-Key"))
	}))
	defer server.Close()
	for _, echoed := range []bool{true, false} {
		req, err := http.NewRequest(http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if echoed {
			req.Header.Set("X-Api-Key", secret)
		}
		response, err := mediate(req, server.Client().Transport, secret)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(response.Body)
		_ = response.Body.Close()
		if echoed && !errors.Is(err, ErrResponse) {
			t.Fatalf("trailer reflection passed: %v", err)
		}
		if !echoed && err != nil {
			t.Fatalf("clean trailer refused: %v", err)
		}
	}
}

func TestMediationRefusesStackedEncodings(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mediate(req, fixtureTransport(func(*http.Request) (*http.Response, error) {
		response := rawFixtureResponse([]byte("compressed"))
		response.Header.Add("Content-Encoding", "identity")
		response.Header.Add("Content-Encoding", "gzip")
		return response, nil
	}), "released-provider-secret-0123456789")
	if !errors.Is(err, ErrResponse) {
		t.Fatalf("stacked encodings passed unscanned: %v", err)
	}
}

// A token the provider revoked early answers 401 long before it expires; the
// next refresh must rotate it rather than return the cached grant.
func TestRefreshRotatesAfterProviderRejectsAccess(t *testing.T) {
	b := fixtureBroker(t)
	enroll(t, b)
	rotated := fixtureToken()
	rotated.Access, rotated.Refresh = "synthetic-provider-access-token-02", "synthetic-provider-refresh-token-02"
	dispatched := 0
	refresh := func() {
		t.Helper()
		response, err := fixtureRoundTrip(b, refreshRequest(t, b), fixtureTransport(func(*http.Request) (*http.Response, error) {
			dispatched++
			return fixtureResponse(rotated), nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		responseToken(t, response)
	}
	refresh()
	if dispatched != 0 {
		t.Fatal("refresh far from expiry was not answered from the committed grant")
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+b.registry.Access)
	response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
		rejected := rawFixtureResponse([]byte(`{"type":"error"}`))
		rejected.StatusCode = http.StatusUnauthorized
		return rejected, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	refresh()
	g, err := b.readGrant()
	if dispatched != 1 || err != nil || g.Token.Access != rotated.Access {
		t.Fatalf("refresh after a 401 dispatched %d times, grant %+v, %v", dispatched, g, err)
	}
}

// encoding/json matches field names by Unicode simple folding, so duplicate
// detection must fold the same way.
func TestDuplicateKeysFoldLikeEncodingJSON(t *testing.T) {
	for _, document := range []string{
		`{"access_token":"a","ACCESS_TOKEN":"b"}`,
		`{"access_token":"a","acceſs_token":"b"}`,
		`{"token":"a","Key":"b","key":"c"}`,
	} {
		if uniqueJSON([]byte(document)) == nil {
			t.Errorf("duplicate keys accepted: %s", document)
		}
	}
	if err := uniqueJSON([]byte(`{"access_token":"a","refresh_token":"b"}`)); err != nil {
		t.Fatal(err)
	}
}

// A reflected JWT signature, the secret part of a ChatGPT token, is caught
// even without the token's header and claims around it.
func TestChatGPTReleaseGuardsTheSignature(t *testing.T) {
	f := newOpenAIFixture(t, "codex")
	handle := f.login()["access_token"].(string)
	signature := jwtSignature(f.access)
	req := releaseRequest(t, "https://chatgpt.com/backend-api/codex/responses", handle, nil)
	response, err := fixtureRoundTrip(f.b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
		return rawFixtureResponse([]byte(`{"echo":"` + signature + `"}`)), nil
	}))
	if err == nil {
		_, err = io.ReadAll(response.Body)
		_ = response.Body.Close()
	}
	if signature == "" || !errors.Is(err, ErrResponse) {
		t.Fatalf("reflected signature passed: %v", err)
	}
}

func TestChatGPTLoginRefusesExpiredAndBoundsLongLifetimes(t *testing.T) {
	for name, tc := range map[string]struct {
		exp     int64
		wantErr bool
	}{
		"expired":    {exp: 1800000000 - 60, wantErr: true},
		"centuries":  {exp: 1800000000 + 100*maxLifetime},
		"in an hour": {exp: 1800000000 + 3600},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOpenAIFixture(t, "codex")
			f.access = fixtureJWT(t, map[string]any{"exp": tc.exp, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": f.account}}, "real-access-signature")
			// Without expires_in, the token's own exp claim decides.
			f.respond = func(*http.Request) (*http.Response, error) {
				return fixtureResponse(map[string]any{"access_token": f.access, "refresh_token": f.rt, "id_token": f.id}), nil
			}
			_, err := fixtureRoundTrip(f.b, f.request(false, map[string]string{"grant_type": "authorization_code", "client_id": openaiClientID, "code": "synthetic-code", "code_verifier": "synthetic-verifier", "redirect_uri": "http://127.0.0.1:1455/auth/callback"}), f.transport())
			if tc.wantErr {
				if !errors.Is(err, ErrResponse) {
					t.Fatalf("expired token committed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			g, err := f.b.readOpenAIGrant()
			if err != nil || g.ExpiresAt > f.b.now().Unix()+maxLifetime {
				t.Fatalf("lifetime unbounded: %+v, %v", g, err)
			}
		})
	}
}

func TestCopilotDeviceRequestNeedsItsScope(t *testing.T) {
	b := fixtureToolBroker(t, "opencode")
	req := copilotRequest(t, githubDevicePath, map[string]string{"client_id": copilotClientID, "extra": "x"})
	if _, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Error("device request with a foreign field dispatched")
		return nil, io.EOF
	})); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign field accepted: %v", err)
	}
}
