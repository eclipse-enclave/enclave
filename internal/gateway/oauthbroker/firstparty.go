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
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
)

// The Claude client is not pinned, so the gateway cannot rely on a fixed set
// of client endpoints. It mediates every first-party domain instead: a changed
// client that obtains credentials anywhere but the captured exchange fails
// there rather than receiving them unmediated.
var firstPartyDomains = []string{"anthropic.com", "claude.com", "claude.ai"}

// releaseHosts receive the access token in a Bearer Authorization header on
// any path, subject to credentialPath and the response tripwire.
var releaseHosts = []string{accessHost, "mcp-proxy.anthropic.com"}

// genericCredentialPatterns indicate credentials in a structured first-party
// response. Model and file content may legitimately contain them, so content
// paths are only checked for the real tokens themselves.
var genericCredentialPatterns = [][]byte{[]byte(`"access_token"`), []byte(`"refresh_token"`), []byte("sk-ant-")}

func firstPartyHost(host string) bool {
	host = protectedHost(host)
	return slices.ContainsFunc(firstPartyDomains, func(domain string) bool {
		return host == domain || strings.HasSuffix(host, "."+domain)
	})
}

func firstPartyRequest(req *http.Request) bool {
	return firstPartyHost(req.URL.Hostname()) || firstPartyHost(req.Host)
}

func releaseOrigin(req *http.Request) bool {
	return slices.ContainsFunc(releaseHosts, func(host string) bool { return exactOrigin(req, host) })
}

// credentialPath matches token exchanges and credential-minting endpoints other
// than the captured exchange.
func credentialPath(path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "oauth/token") || strings.Contains(path, "oauth2/token") ||
		strings.Contains(path, "api_key") || strings.Contains(path, "api-key") || strings.HasSuffix(path, "/token")
}

// contentPath carries model output, user files or MCP tool results.
func contentPath(req *http.Request) bool {
	if exactOrigin(req, "mcp-proxy.anthropic.com") {
		return true
	}
	return slices.ContainsFunc([]string{"/v1/messages", "/v1/files", "/v1/complete"}, func(prefix string) bool {
		return req.URL.Path == prefix || strings.HasPrefix(req.URL.Path, prefix+"/")
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
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	var patterns [][]byte
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		patterns = append(patterns, []byte(secret))
		for _, values := range resp.Header {
			if slices.ContainsFunc(values, func(value string) bool { return strings.Contains(value, secret) }) {
				_ = resp.Body.Close()
				return nil, ErrResponse
			}
		}
	}
	if firstPartyRequest(req) && !contentPath(req) && structuredMedia(resp.Header) {
		patterns = append(patterns, genericCredentialPatterns...)
	}
	// An upgraded stream is relayed as is; it cannot be scanned.
	if len(patterns) == 0 || resp.StatusCode == http.StatusSwitchingProtocols {
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
		if slices.ContainsFunc(t.patterns, func(pattern []byte) bool { return bytes.Contains(t.pending, pattern) }) {
			t.pending, t.err = nil, ErrResponse
			return 0, t.err
		}
		switch {
		case errors.Is(err, io.EOF):
			t.err, t.safe = io.EOF, len(t.pending)
		case err != nil:
			t.pending, t.err = nil, err
		default:
			t.safe = len(t.pending) - t.partialMatch()
		}
	}
	n := copy(p, t.pending[:t.safe])
	t.pending, t.safe = t.pending[n:], t.safe-n
	return n, nil
}

func (t *tripwire) partialMatch() int {
	longest := 0
	for _, pattern := range t.patterns {
		for k := min(len(pattern)-1, len(t.pending)); k > longest; k-- {
			if bytes.HasSuffix(t.pending, pattern[:k]) {
				longest = k
				break
			}
		}
	}
	return longest
}

func (t *tripwire) Close() error {
	return t.body.Close()
}
