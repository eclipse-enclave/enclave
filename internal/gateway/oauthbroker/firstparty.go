// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
)

// provider describes the first-party domains of one credential issuer. The
// gateway mediates them in every protected session, whether or not the tool
// uses the provider: a client cannot obtain credentials there through an
// endpoint the gateway does not capture, and raw credentials sent there are
// refused, so a pasted key never authenticates a protected session.
type provider struct {
	name    string
	domains []string
	// credentialPath matches token exchanges and credential-minting endpoints
	// other than captured ones.
	credentialPath func(host, path string) bool
	// contentPath carries model output, user files or tool results, which may
	// legitimately contain credential-like text.
	contentPath func(host, path string) bool
	// patterns indicate credentials in a structured response elsewhere.
	patterns [][]byte
	// queryCredential names a URL parameter that can carry an API key.
	queryCredential string
}

var tokenFields = [][]byte{[]byte(`"access_token"`), []byte(`"refresh_token"`), []byte(`"id_token"`), []byte(`"accessToken"`), []byte(`"refreshToken"`)}

func withPatterns(extra ...string) [][]byte {
	patterns := slices.Clone(tokenFields)
	for _, pattern := range extra {
		patterns = append(patterns, []byte(pattern))
	}
	return patterns
}

func pathPrefix(path string, prefixes ...string) bool {
	return slices.ContainsFunc(prefixes, func(prefix string) bool {
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	})
}

// genericCredentialPath matches token and API-key endpoints by name.
func genericCredentialPath(_, path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "oauth/token") || strings.Contains(path, "oauth2/token") ||
		strings.Contains(path, "api_key") || strings.Contains(path, "api-key") || strings.HasSuffix(path, "/token") ||
		strings.Contains(path, "oauth/revoke")
}

var providers = []provider{
	{
		name:           "anthropic",
		domains:        []string{"anthropic.com", "claude.com", "claude.ai"},
		credentialPath: genericCredentialPath,
		contentPath: func(host, path string) bool {
			return host == "mcp-proxy.anthropic.com" || pathPrefix(path, "/v1/messages", "/v1/files", "/v1/complete")
		},
		patterns: withPatterns("sk-ant-"),
	},
	{
		name:    "openai",
		domains: []string{"openai.com", "chatgpt.com", "chatgpt-staging.com"},
		credentialPath: func(host, path string) bool {
			lower := strings.ToLower(path)
			// Token sharing and agent registration mint credentials the
			// gateway does not capture.
			return genericCredentialPath(host, path) || strings.HasPrefix(lower, "/api/accounts/oauth") || strings.HasPrefix(lower, "/api/accounts/v1/agent")
		},
		contentPath: func(host, path string) bool {
			return host == "api.openai.com" || pathPrefix(path, "/backend-api/codex", "/backend-api/files", "/backend-api/ps")
		},
		patterns: withPatterns("sk-proj-", "sk-svcacct-", "sk-admin-"),
	},
	{
		name:    "mistral",
		domains: []string{"mistral.ai"},
		credentialPath: func(host, path string) bool {
			// The browser sign-in exchange returns an API key.
			return genericCredentialPath(host, path) || strings.HasSuffix(strings.ToLower(path), "/exchange")
		},
		contentPath: func(host, _ string) bool { return host == "api.mistral.ai" },
		patterns:    withPatterns(`"api_key"`),
	},
	{
		name:           "copilot",
		domains:        []string{"githubcopilot.com"},
		credentialPath: genericCredentialPath,
		contentPath:    func(string, string) bool { return true },
	},
	{
		// The GitLab CLI authenticates with host-configured tokens; its OAuth
		// login would bring a token into the workload.
		name:           "gitlab",
		domains:        []string{"gitlab.com"},
		credentialPath: genericCredentialPath,
		contentPath:    func(string, string) bool { return true },
	},
	{
		name:            "google",
		domains:         []string{"generativelanguage.googleapis.com"},
		credentialPath:  func(string, string) bool { return false },
		contentPath:     func(string, string) bool { return true },
		queryCredential: "key",
	},
	{
		// GitHub logins issue tokens at github.com and Copilot tokens at
		// api.github.com. Repository content stays unscanned.
		name:    "github",
		domains: []string{"github.com"},
		credentialPath: func(host, path string) bool {
			lower := strings.ToLower(path)
			return strings.HasPrefix(lower, "/login/oauth") || strings.HasPrefix(lower, "/login/device") || strings.Contains(lower, "/copilot_internal/")
		},
		contentPath: func(host, _ string) bool { return host != "github.com" },
		patterns:    tokenFields,
	},
}

func providerFor(host string) *provider {
	host = protectedHost(host)
	for i := range providers {
		if slices.ContainsFunc(providers[i].domains, func(domain string) bool {
			return host == domain || strings.HasSuffix(host, "."+domain)
		}) {
			return &providers[i]
		}
	}
	return nil
}

// requestProvider names the provider of either the URL or the Host header, so
// a mismatch between them cannot route around mediation.
func requestProvider(req *http.Request) *provider {
	if p := providerFor(req.URL.Hostname()); p != nil {
		return p
	}
	return providerFor(req.Host)
}

func firstPartyHost(host string) bool {
	return providerFor(host) != nil
}

// releaseHosts receive the Claude access token in a Bearer Authorization
// header on any path, subject to the response tripwire.
var releaseHosts = []string{accessHost, "mcp-proxy.anthropic.com"}

func releaseOrigin(req *http.Request) bool {
	return slices.ContainsFunc(releaseHosts, func(host string) bool { return exactOrigin(req, host) })
}

// credentialHeaders carry provider credentials. In a protected session they
// must hold a handle or a rule-released secret when sent to a provider.
var credentialHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key", "Private-Token", "Job-Token"}

func carriesCredential(req *http.Request) bool {
	return slices.ContainsFunc(credentialHeaders, func(name string) bool { return len(req.Header.Values(name)) != 0 })
}

// rawCredential reports a credential header with content beyond an
// authentication scheme, once released secrets have been blanked.
func rawCredential(req *http.Request) bool {
	return slices.ContainsFunc(credentialHeaders, func(name string) bool {
		return slices.ContainsFunc(req.Header.Values(name), func(value string) bool {
			fields := strings.Fields(value)
			if len(fields) > 0 && slices.Contains([]string{"bearer", "basic", "token"}, strings.ToLower(fields[0])) {
				fields = fields[1:]
			}
			return len(fields) != 0
		})
	})
}

func structuredMedia(header http.Header) bool {
	value := header.Get("Content-Type")
	if value == "" {
		return true
	}
	media, _, err := mime.ParseMediaType(value)
	if err != nil {
		return true
	}
	return media == "application/json" || strings.HasSuffix(media, "+json") || media == "application/x-www-form-urlencoded" || strings.HasPrefix(media, "text/plain")
}

type releasedKey struct{}

// WithReleasedSecrets records the secrets the proxy released into a request
// by its declared rules, so the broker can accept them and guard responses
// against their reflection.
func WithReleasedSecrets(ctx context.Context, secrets []string) context.Context {
	if len(secrets) == 0 {
		return ctx
	}
	return context.WithValue(ctx, releasedKey{}, slices.Clone(secrets))
}

func releasedSecrets(ctx context.Context) []string {
	secrets, _ := ctx.Value(releasedKey{}).([]string)
	return secrets
}

// mediate forwards a request and guards its response. secrets are the real
// credentials released into req, if any; structured first-party responses are
// also checked for credentials the gateway did not capture.
func mediate(req *http.Request, transport http.RoundTripper, secrets ...string) (*http.Response, error) {
	var patterns [][]byte
	for _, secret := range secrets {
		if secret != "" {
			patterns = append(patterns, []byte(secret))
		}
	}
	if len(patterns) != 0 && req.Header.Get("Upgrade") != "" {
		if err := prepareWebSocket(req); err != nil {
			return nil, err
		}
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	for _, values := range resp.Header {
		if slices.ContainsFunc(values, func(value string) bool { return contains([]byte(value), patterns) }) {
			_ = resp.Body.Close()
			return nil, ErrResponse
		}
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		if len(patterns) == 0 || req.Header.Get("Upgrade") == "" {
			return resp, nil
		}
		return scanWebSocket(resp, patterns)
	}
	if p := requestProvider(req); p != nil && !p.contentPath(protectedHost(req.URL.Hostname()), req.URL.Path) && structuredMedia(resp.Header) {
		patterns = append(patterns, p.patterns...)
	}
	if len(patterns) == 0 {
		return resp, nil
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		_ = resp.Body.Close()
		return nil, ErrResponse
	}
	resp.Body = &tripwire{body: resp.Body, patterns: patterns}
	return resp, nil
}

// tripwire passes a response body through unchanged until it would contain a
// pattern, then fails before any byte of that occurrence is returned. It holds
// back only a trailing partial match, so streamed events are not delayed.
type tripwire struct {
	body     io.ReadCloser
	patterns [][]byte
	pending  []byte
	safe     int
	err      error
}

func (t *tripwire) Read(p []byte) (int, error) {
	for t.safe == 0 {
		if t.err != nil {
			return 0, t.err
		}
		buffer := make([]byte, 32<<10)
		n, err := t.body.Read(buffer)
		t.pending = append(t.pending, buffer[:n]...)
		if contains(t.pending, t.patterns) {
			t.pending, t.err = nil, ErrResponse
			return 0, t.err
		}
		switch {
		case errors.Is(err, io.EOF):
			t.err, t.safe = io.EOF, len(t.pending)
		case err != nil:
			t.pending, t.err = nil, err
		default:
			t.safe = len(t.pending) - partialMatch(t.pending, t.patterns)
		}
	}
	n := copy(p, t.pending[:t.safe])
	t.pending, t.safe = t.pending[n:], t.safe-n
	return n, nil
}

func (t *tripwire) Close() error {
	return t.body.Close()
}
