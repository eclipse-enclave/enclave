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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

func fixtureToolBroker(t *testing.T, tool string) *Broker {
	t.Helper()
	return reopenAsTool(t, fixtureBroker(t), tool)
}

// reopenAsTool opens a new session of b's identity for another tool.
func reopenAsTool(t *testing.T, b *Broker, tool string) *Broker {
	t.Helper()
	config := b.config
	config.Tool = tool
	config.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(config.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = b.now
	return reopened
}

func TestProvidersRefuseRawCredentialsAndUncapturedEndpoints(t *testing.T) {
	b := fixtureToolBroker(t, "codex")
	for _, tc := range []struct {
		name, method, url, header, value string
		body, contentType                string
		denied                           bool
	}{
		{name: "pasted Anthropic key", method: http.MethodPost, url: "https://api.anthropic.com/v1/messages", header: "X-Api-Key", value: "sk-ant-api03-pasted", denied: true},
		{name: "raw ChatGPT token", method: http.MethodPost, url: "https://chatgpt.com/backend-api/codex/responses", header: "Authorization", value: "Bearer raw-token", denied: true},
		{name: "pasted Mistral key", method: http.MethodPost, url: "https://api.mistral.ai/v1/chat/completions", header: "Authorization", value: "Bearer pasted", denied: true},
		{name: "pasted Copilot token", method: http.MethodPost, url: "https://api.githubcopilot.com/chat/completions", header: "Authorization", value: "Bearer gho_pasted", denied: true},
		{name: "pasted Gemini key", method: http.MethodPost, url: "https://generativelanguage.googleapis.com/v1beta/models/m:generateContent", header: "X-Goog-Api-Key", value: "AIzaPasted", denied: true},
		{name: "pasted Gemini key in query", method: http.MethodGet, url: "https://generativelanguage.googleapis.com/v1beta/models?key=AIzaPasted", denied: true},
		{name: "pasted GitLab token", method: http.MethodGet, url: "https://gitlab.com/api/v4/user", header: "Private-Token", value: "glpat-pasted", denied: true},
		{name: "GitLab OAuth login", method: http.MethodPost, url: "https://gitlab.com/oauth/token", denied: true},
		{name: "Claude token endpoint without adapter", method: http.MethodPost, url: "https://platform.claude.com/v1/oauth/token", denied: true},
		{name: "OpenAI API-key exchange", method: http.MethodPost, url: "https://auth.openai.com/oauth/token", denied: true, contentType: "application/x-www-form-urlencoded",
			body: "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange&client_id=" + openaiClientID + "&requested_token=openai-api-key&subject_token=x&subject_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Aid_token"},
		{name: "ChatGPT device code request", method: http.MethodPost, url: "https://auth.openai.com/api/accounts/deviceauth/usercode"},
		{name: "ChatGPT device code poll", method: http.MethodPost, url: "https://auth.openai.com/api/accounts/deviceauth/token"},
		{name: "GitLab token rotation", method: http.MethodPost, url: "https://gitlab.com/api/v4/personal_access_tokens/self/rotate", denied: true},
		{name: "GitLab project access token", method: http.MethodPost, url: "https://gitlab.com/api/v4/projects/acme%2Fapp/access_tokens", denied: true},
		{name: "GitLab trigger tokens", method: http.MethodGet, url: "https://gitlab.com/api/v4/projects/7/triggers", denied: true},
		{name: "GitLab repository named like a key", method: http.MethodGet, url: "https://gitlab.com/acme/api-keys.git/info/refs?service=git-upload-pack"},
		{name: "GitLab repository named token", method: http.MethodGet, url: "https://gitlab.com/acme/token"},
		{name: "GitHub login behind a doubled slash", method: http.MethodPost, url: "https://github.com//login/oauth/access_token", denied: true},
		{name: "GitHub runner registration", method: http.MethodPost, url: "https://api.github.com/repos/acme/app/actions/runners/registration-token", denied: true},
		{name: "GitHub authorizations", method: http.MethodPost, url: "https://api.github.com/authorizations", denied: true},
		{name: "OpenAI token sharing", method: http.MethodPost, url: "https://auth.openai.com/api/accounts/oauth/token", denied: true},
		{name: "Mistral sign-in exchange", method: http.MethodPost, url: "https://console.mistral.ai/api/vibe/sign-in/proc/exchange", denied: true},
		{name: "GitHub device token", method: http.MethodPost, url: "https://github.com/login/oauth/access_token", denied: true},
		{name: "Copilot token exchange", method: http.MethodGet, url: "https://api.github.com/copilot_internal/v2/token", denied: true},
		{name: "anonymous provider request", method: http.MethodGet, url: "https://api.openai.com/v1/models"},
		{name: "other host credential", method: http.MethodGet, url: "https://api.example.test/v1", header: "Authorization", value: "Bearer theirs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == "" {
				body = "{}"
			}
			req, err := http.NewRequest(tc.method, tc.url, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			called := false
			response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
				called = true
				return rawFixtureResponse([]byte(`{}`)), nil
			}))
			if tc.denied {
				if !errors.Is(err, ErrDenied) || called {
					t.Fatalf("request reached the provider: %v, called=%v", err, called)
				}
				return
			}
			if err != nil || !called {
				t.Fatalf("request refused: %v", err)
			}
			_ = response.Body.Close()
		})
	}
}

// A token minted through an API that path rules miss still cannot reach the
// workload in a structured response; repository content is not scanned.
func TestGitLabResponsesAreCheckedForTokens(t *testing.T) {
	b := fixtureToolBroker(t, "claude")
	const released = "glpat-host-configured-0123456789"
	for _, tc := range []struct {
		path string
		ok   bool
	}{
		{"/api/graphql", false},
		{"/api/v4/projects/7/repository/files/README.md/raw", true},
	} {
		req, err := http.NewRequest(http.MethodPost, "https://gitlab.com"+tc.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Private-Token", released)
		req = req.WithContext(WithReleasedSecrets(req.Context(), []string{released}))
		response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
			return rawFixtureResponse([]byte(`{"token":"glpat-minted-0123456789"}`)), nil
		}))
		if err == nil {
			_, err = io.ReadAll(response.Body)
			_ = response.Body.Close()
		}
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrResponse)) {
			t.Fatalf("%s: %v", tc.path, err)
		}
	}
}

func TestToolWithoutAnthropicNeverReleasesClaudeHandles(t *testing.T) {
	b := fixtureToolBroker(t, "codex")
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+b.registry.Access)
	if _, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Error("Claude handle released for a tool without the adapter")
		return nil, io.EOF
	})); !errors.Is(err, ErrDenied) {
		t.Fatalf("handle accepted: %v", err)
	}
}

func TestPiViewProjectsAnthropicHandles(t *testing.T) {
	b := fixtureBroker(t)
	enroll(t, b)
	pi := reopenAsTool(t, b, "pi")
	view := filepath.Join(t.TempDir(), "view")
	if err := os.Mkdir(view, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pi.InitializeView(view); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(view, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]struct {
		Type, Access, Refresh string
		Expires               int64
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		t.Fatal(err)
	}
	entry := auth["anthropic"]
	if entry.Type != "oauth" || entry.Access != pi.registry.Access || entry.Refresh != pi.registry.Refresh || entry.Expires <= 0 {
		t.Fatalf("pi view entry = %+v", entry)
	}
	if strings.Contains(string(data), fixtureAccess) || strings.Contains(string(data), fixtureRefresh) {
		t.Fatal("pi view contains the real tokens")
	}
}

func TestInstanceProvidersFollowReplacedDeclaredHosts(t *testing.T) {
	got := InstanceProviders([]model.SecretReleaseEntry{
		{Hosts: []string{"GitLab.Example.com"}, ExactHosts: true, DeclaredHosts: []string{"gitlab.com", "*.gitlab.com"}},
		{Hosts: []string{"other.example.com"}, ExactHosts: true, DeclaredHosts: []string{"api.vendor.example"}},
		{Hosts: []string{"gitlab.com"}},
	})
	if len(got) != 1 || got["gitlab.example.com"] != "gitlab" {
		t.Fatalf("instances = %v", got)
	}
}

// A self-managed GitLab instance named by the session's configuration is
// mediated like gitlab.com: its configured token is released, but logins and
// pasted tokens are refused there.
func TestDeclaredInstanceIsMediatedLikeItsProvider(t *testing.T) {
	b := fixtureToolBroker(t, "claude")
	config := b.config
	config.Instances = map[string]string{"gitlab.example.com": "gitlab"}
	instance, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	if !instance.RequiresMITM("gitlab.example.com") || b.RequiresMITM("gitlab.example.com") {
		t.Fatal("only a declared instance is intercepted")
	}
	const released = "glpat-host-configured-token"
	for _, tc := range []struct {
		name, method, url, header, value string
		released                         bool
		denied                           bool
	}{
		{name: "configured token", method: http.MethodGet, url: "https://gitlab.example.com/api/v4/user", header: "Private-Token", value: released, released: true},
		{name: "pasted token", method: http.MethodGet, url: "https://gitlab.example.com/api/v4/user", header: "Private-Token", value: "glpat-pasted", denied: true},
		{name: "OAuth login", method: http.MethodPost, url: "https://gitlab.example.com/oauth/token", denied: true},
		{name: "anonymous read", method: http.MethodGet, url: "https://gitlab.example.com/api/v4/projects"},
		{name: "other host", method: http.MethodGet, url: "https://other.example.com/api/v4/user", header: "Private-Token", value: "glpat-pasted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			if tc.released {
				req = req.WithContext(WithReleasedSecrets(req.Context(), []string{released}))
			}
			called := false
			response, err := fixtureRoundTrip(instance, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
				called = true
				return rawFixtureResponse([]byte(`{}`)), nil
			}))
			if tc.denied {
				if !errors.Is(err, ErrDenied) || called {
					t.Fatalf("not refused: %v, upstream called %t", err, called)
				}
				return
			}
			if err != nil || !called {
				t.Fatalf("refused: %v", err)
			}
			_ = response.Body.Close()
		})
	}
	config.Instances = map[string]string{"gitlab.example.com": "unknown"}
	if _, err := Open(config); !errors.Is(err, ErrState) {
		t.Fatalf("unknown instance provider accepted: %v", err)
	}
}
