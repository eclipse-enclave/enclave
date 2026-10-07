// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// Candidate constants inspected in the official 2.1.63 npm artifact. Static
// inspection is NOT the real-client compatibility gate; see the verification doc.
const tokenHost = "platform.claude.com"
const accessHost = "api.anthropic.com"
const tokenPath = "/v1/oauth/token" // #nosec G101 -- public OAuth endpoint, not a credential.
const issuer = "https://platform.claude.com"
const clientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
const refreshWindow = 5 * time.Minute
const refreshReplayWindow = 30 * time.Second
const exchangeTimeout = 2 * time.Minute

// RequiresMITM installs capture before the first login (there is no grant yet).
func (b *Broker) RequiresMITM(host string) bool {
	return protectedHost(host) == tokenHost || protectedHost(host) == accessHost || (b.github != nil && githubHost(host))
}

func protectedHost(value string) string {
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return strings.TrimRight(strings.ToLower(value), ".")
}

func exactOrigin(req *http.Request, host string) bool {
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Opaque != "" {
		return false
	}
	if req.URL.Host != host && req.URL.Host != host+":443" {
		return false
	}
	if req.Host != "" && req.Host != host && req.Host != host+":443" {
		return false
	}
	return req.TLS == nil || req.TLS.ServerName == "" || req.TLS.ServerName == host
}

func exactPath(req *http.Request, path string) bool {
	return req.URL.Path == path && req.URL.RawPath == "" && req.URL.RawQuery == "" && !req.URL.ForceQuery && req.URL.Fragment == ""
}

// RoundTrip returns either a fully sanitized token response or an unrelated
// streaming response. It never uses http.Client, so redirects cannot escape the
// declared endpoint. exchangeTransport must use fresh connections (a dedicated
// http.Transport with DisableKeepAlives), without redirects, retries or automatic
// decompression. A stale pooled connection must not invalidate a rotating grant;
// compression stays explicit so encoded and decoded bounds can both be enforced.
func (b *Broker) RoundTrip(req *http.Request, transport, exchangeTransport http.RoundTripper) (*http.Response, error) {
	if req == nil || req.URL == nil || transport == nil || exchangeTransport == nil {
		return nil, ErrDenied
	}
	if b.github != nil {
		if response, handled, err := b.github.roundTrip(req, transport, exchangeTransport); handled {
			return response, err
		}
	}
	if exactOrigin(req, tokenHost) && exactPath(req, tokenPath) {
		return b.exchange(req, exchangeTransport)
	}
	// Never let a spelling/port/query mismatch on the enrollment origin turn
	// capture into raw passthrough, even before any handles or grant exist.
	if protectedHost(req.URL.Hostname()) == tokenHost || protectedHost(req.Host) == tokenHost {
		return nil, ErrDenied
	}
	// No secret is ever substituted in arbitrary headers, bodies or URLs. This
	// also rejects handles from ended sessions at declared authentication fields.
	authorization := req.Header.Values("Authorization")
	if len(authorization) == 0 {
		if containsHandle(req) {
			return nil, ErrDenied
		}
		return transport.RoundTrip(req)
	}
	if len(authorization) != 1 || authorization[0] != "Bearer "+b.registry.Access || !exactOrigin(req, accessHost) || !accessEndpoint(req) {
		if containsHandle(req) || b.RequiresMITM(req.URL.Hostname()) {
			return nil, ErrDenied
		}
		return transport.RoundTrip(req)
	}
	outgoing := req.Clone(req.Context())
	outgoing.Header.Del("Authorization")
	if containsHandle(outgoing) {
		return nil, ErrDenied
	}
	err := b.locked(func() error {
		g, err := b.readGrant()
		if err != nil {
			return err
		}
		if g == nil {
			return ErrDenied
		}
		if g.Status != "ready" {
			return ErrUncertain
		}
		if g.ExpiresAt <= b.now().Unix() {
			return ErrDenied
		}
		outgoing.Header.Set("Authorization", "Bearer "+g.Token.Access)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return transport.RoundTrip(outgoing)
}

func accessEndpoint(req *http.Request) bool {
	if req.Method == http.MethodPost && (exactPath(req, "/v1/messages") || (req.URL.Path == "/v1/messages" && req.URL.RawPath == "" && req.URL.RawQuery == "beta=true" && req.URL.Fragment == "")) {
		return true
	}
	return req.Method == http.MethodGet && (exactPath(req, "/api/oauth/profile") || exactPath(req, "/api/oauth/claude_cli/roles"))
}

// ContainsHandle conservatively detects credential prefixes for rejection and
// audit scrubbing, including malformed or wrong-session handles. It does not
// authenticate a handle or distinguish it from a real provider credential.
func ContainsHandle(value string) bool {
	return strings.Contains(value, accessHandlePrefix) || strings.Contains(value, refreshHandlePrefix) || strings.Contains(value, githubHandlePrefix)
}

func containsHandle(req *http.Request) bool {
	if ContainsHandle(req.URL.String()) || ContainsHandle(req.Host) {
		return true
	}
	for key, values := range req.Header {
		if ContainsHandle(key) {
			return true
		}
		for _, value := range values {
			if ContainsHandle(value) {
				return true
			}
			if strings.EqualFold(key, "Authorization") && strings.HasPrefix(value, "Basic ") {
				if decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "Basic ")); err == nil && ContainsHandle(string(decoded)) {
					return true
				}
			}
		}
	}
	for key, values := range req.Trailer {
		if ContainsHandle(key) {
			return true
		}
		for _, value := range values {
			if ContainsHandle(value) {
				return true
			}
		}
	}
	return false
}

func (b *Broker) validateExchange(req *http.Request) (tokenRequest, error) {
	if req.Method != http.MethodPost || !jsonContent(req.Header) || req.Header.Get("Content-Encoding") != "" || req.Body == nil || req.ContentLength > maxDocument || len(req.Trailer) != 0 || containsHandle(req) {
		return tokenRequest{}, ErrDenied
	}
	defer func() { _ = req.Body.Close() }()
	data, err := boundedRead(req.Body)
	if err != nil {
		return tokenRequest{}, ErrDenied
	}
	var input tokenRequest
	if strictJSON(data, &input) != nil || exactKeys(data, "grant_type", "client_id", "code", "redirect_uri", "code_verifier", "state", "refresh_token", "scope") != nil || input.ClientID != clientID {
		return tokenRequest{}, ErrDenied
	}
	switch input.GrantType {
	case "refresh_token":
		if input.Refresh != b.registry.Refresh || input.Code != "" || input.Verifier != "" || input.State != "" || input.RedirectURI != "" {
			return tokenRequest{}, ErrDenied
		}
	case "authorization_code":
		if input.Refresh != "" || !safeText(input.Code, 4096) || !safeText(input.Verifier, 256) || !safeText(input.RedirectURI, 2048) {
			return tokenRequest{}, ErrDenied
		}
	default:
		return tokenRequest{}, ErrDenied
	}
	return input, nil
}

func (b *Broker) exchange(req *http.Request, transport http.RoundTripper) (*http.Response, error) {
	input, err := b.validateExchange(req)
	if err != nil {
		return nil, err
	}
	var response *http.Response
	err = b.locked(func() error {
		if err := req.Context().Err(); err != nil {
			return ErrDenied
		}
		g, err := b.readGrant()
		if err != nil {
			return err
		}
		refresh := input.GrantType == "refresh_token"
		if refresh {
			if g == nil {
				return ErrDenied
			}
			if g.Status != "ready" {
				return ErrUncertain
			}
			// Retries and waiting duplicates project a usable committed result.
			// Client-written expiry and claimed generations are never authoritative.
			if reuseCommittedGrant(g, b.now()) {
				response, err = b.project(g)
				return err
			}
			input.Refresh = g.Token.Refresh
		} else if g != nil && g.Status == "ready" {
			return ErrDenied // Fresh identities only; no silent replacement/import.
		}
		pending := grant{Schema: 1, Identity: b.config.Identity, Issuer: issuer, Status: "uncertain"}
		if g != nil {
			pending = *g
			pending.Status = "uncertain"
		}
		// Persist intent BEFORE dispatch. Any later network/schema/persistence
		// failure leaves uncertain state, never permission to reuse the old token.
		if err := b.writeGrant(&pending); err != nil {
			return err
		}
		payload, err := json.Marshal(input)
		if err != nil {
			return ErrState
		}
		exchangeContext, cancel := context.WithTimeout(req.Context(), exchangeTimeout)
		defer cancel()
		outgoing := req.Clone(exchangeContext)
		outgoing.Body = io.NopCloser(bytes.NewReader(payload))
		outgoing.GetBody = nil
		outgoing.ContentLength = int64(len(payload))
		// Do not forward client idempotency/retry hints, bearer tokens, cookies or
		// extra routing headers into the token exchange.
		outgoing.Header = http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}, "Accept-Encoding": {"identity"}}
		upstream, err := transport.RoundTrip(outgoing)
		if err != nil {
			if upstream != nil && upstream.Body != nil {
				_ = upstream.Body.Close()
			}
			return ErrUncertain
		}
		body, err := responseBody(upstream)
		if err != nil {
			return ErrResponse
		}
		if upstream.StatusCode != http.StatusOK {
			response, err = sanitizedError(upstream, body)
			return err // Remain uncertain even for apparently definitive failures.
		}
		response, err = b.commitToken(body, &pending, refresh)
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func reuseCommittedGrant(g *grant, now time.Time) bool {
	if g.ExpiresAt > now.Add(refreshWindow).Unix() {
		return true
	}
	// Deduplicate recent/lost replies, including short-lived grants, without
	// deferring every subsequent refresh until expiry. Half-lifetime caps the
	// grace period so even tokens shorter than the grace can rotate early.
	grace := min(refreshReplayWindow, time.Duration(g.Token.Expires)*time.Second/2)
	return g.ExpiresAt > now.Unix() && now.Before(g.CommittedAt.Add(grace))
}

// commitToken runs under the identity lock and never consults native auth files.
func (b *Broker) commitToken(body []byte, pending *grant, refresh bool) (*http.Response, error) {
	token, err := decodeToken(body)
	if err != nil || validateToken(token, !refresh) != nil {
		return nil, ErrResponse
	}
	if token.Access == b.registry.Access || token.Access == b.registry.Refresh || token.Refresh == b.registry.Access || token.Refresh == b.registry.Refresh {
		return nil, ErrResponse
	}
	if refresh {
		if token.Refresh == "" {
			token.Refresh = pending.Token.Refresh
		}
		if token.Account == nil {
			token.Account = pending.Token.Account
		}
		if token.Organization == nil {
			token.Organization = pending.Token.Organization
		}
	}
	// Reauthentication of an uncertain grant must not silently switch the
	// account behind handles already held by other sessions.
	if refresh || pending.Generation != 0 {
		if token.Account == nil || pending.Token.Account == nil || token.Organization == nil || pending.Token.Organization == nil || *token.Account != *pending.Token.Account || *token.Organization != *pending.Token.Organization {
			return nil, ErrResponse
		}
	}
	committedAt := b.now().UTC()
	committed := grant{Schema: 1, Identity: b.config.Identity, Issuer: issuer, Generation: pending.Generation + 1, Status: "ready", CommittedAt: committedAt, ExpiresAt: committedAt.Unix() + token.Expires, Token: token}
	if err := b.writeGrant(&committed); err != nil {
		return nil, err
	}
	return b.project(&committed)
}

func (b *Broker) writeGrant(g *grant) error {
	if err := b.write(filepath.Join(b.config.StoreDir, grantFile), g); err != nil {
		// The rename may already be visible despite a failed directory fsync.
		// Do not trust it in this process; Open must durably fence recovered state.
		b.failed = true
		return ErrState
	}
	return nil
}

func (b *Broker) project(g *grant) (*http.Response, error) {
	token := g.Token
	token.Access = b.registry.Access
	token.Refresh = b.registry.Refresh
	token.Expires = g.ExpiresAt - b.now().Unix()
	if token.Expires < 1 {
		return nil, ErrUncertain
	}
	return projectedResponse(http.StatusOK, token)
}

// NativeView is regenerated for each new session. Workload-written files are
// never read back into trusted grant state. Subscription metadata starts unknown
// and can be fetched by the client through the approved profile endpoint.
func (b *Broker) NativeView() ([]byte, error) {
	var result []byte
	err := b.locked(func() error {
		g, err := b.readGrant()
		if err != nil {
			return err
		}
		result, err = b.nativeView(g)
		return err
	})
	return result, err
}

func (b *Broker) nativeView(g *grant) ([]byte, error) {
	if g == nil {
		return nil, nil
	}
	if g.Status != "ready" {
		return nil, ErrUncertain
	}
	view := struct {
		OAuth struct {
			Access           string   `json:"accessToken"`  // #nosec G117 -- workload projection contains a handle, never the provider token.
			Refresh          string   `json:"refreshToken"` // #nosec G117 -- workload projection contains a handle, never the provider token.
			Expires          int64    `json:"expiresAt"`
			Scopes           []string `json:"scopes"`
			SubscriptionType *string  `json:"subscriptionType"`
			RateLimitTier    *string  `json:"rateLimitTier"`
		} `json:"claudeAiOauth"`
	}{}
	view.OAuth.Access = b.registry.Access
	view.OAuth.Refresh = b.registry.Refresh
	view.OAuth.Expires = g.ExpiresAt * 1000
	view.OAuth.Scopes = strings.Fields(g.Token.Scope)
	return json.Marshal(view)
}
