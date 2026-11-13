// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package token provides the credential values the gateway injects: a static
// secret, or OAuth2 access tokens minted from a refresh token.
package token

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"enclave/internal/filter"
)

// Source yields the value to inject. Invalidate drops a cached value after
// the upstream rejected it. It ignores values that are no longer cached, so a
// late rejection cannot discard a fresh token.
type Source interface {
	Token(ctx context.Context) (string, error)
	Invalidate(value string)
}

// New builds the source for a validated credential payload. client is used
// for OAuth2 refreshes and must verify TLS.
func New(cred *filter.Credential, client *http.Client) (Source, error) {
	if cred == nil {
		return nil, fmt.Errorf("filter credential is missing")
	}
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	if cred.Static != nil {
		return NewStatic(cred.Static.Value), nil
	}
	o := cred.OAuth2
	return NewOAuth2(OAuth2Config{
		TokenURL:     o.TokenURL,
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		RefreshToken: o.RefreshToken,
		Client:       client,
	}), nil
}

// Static is a Source for a fixed secret.
type Static struct{ value string }

// NewStatic returns a Source that always yields value.
func NewStatic(value string) *Static { return &Static{value: value} }

// Token returns the secret.
func (s *Static) Token(context.Context) (string, error) { return s.value, nil }

// Invalidate does nothing: a static secret cannot be renewed.
func (s *Static) Invalidate(string) {}

// Refresh timing.
const (
	// ExpiryMargin is how long before expiry a token is renewed.
	ExpiryMargin = 60 * time.Second
	// defaultLifetime applies when the token response omits expires_in.
	defaultLifetime = 5 * time.Minute
	// refreshTimeout bounds one refresh, independent of the caller.
	refreshTimeout = 30 * time.Second
	// failureBackoff is how long a failed refresh is reported to callers
	// before the next attempt, so a broken grant cannot flood the endpoint.
	failureBackoff = 2 * time.Second
)

// MaxResponseBytes bounds a token endpoint response. An access token larger
// than this would not fit a request header anyway.
const MaxResponseBytes = 64 << 10

// OAuth2Config configures an OAuth2 source. Now defaults to time.Now and
// Client to http.DefaultClient.
type OAuth2Config struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	RefreshToken string
	Client       *http.Client
	Now          func() time.Time
}

// ErrUnreachable is the cause of a refresh that got no response.
var ErrUnreachable = errors.New("token endpoint unreachable")

// grantErrorCodes are the OAuth2 error codes after which only a new login
// helps: the grant or the client is no longer valid.
var grantErrorCodes = map[string]bool{"invalid_grant": true, "unauthorized_client": true, "invalid_client": true}

// RefreshError is a failed token endpoint request, a refresh or a code
// exchange. It carries the HTTP status and the OAuth2 error code, never the
// response body or the form.
type RefreshError struct {
	Status int
	Code   string
	Err    error
}

// Cause returns a short category of a token error for logs, never a secret:
// the OAuth2 error code, "unreachable", "http-<status>", "bad-response",
// "canceled", or "error".
func Cause(err error) string {
	var refresh *RefreshError
	switch {
	case errors.As(err, &refresh):
		switch {
		case refresh.Code != "":
			return refresh.Code
		case errors.Is(refresh.Err, ErrUnreachable):
			return "unreachable"
		case refresh.Status != 0 && (refresh.Status < 200 || refresh.Status > 299):
			return fmt.Sprintf("http-%d", refresh.Status)
		default:
			return "bad-response"
		}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	}
	return "error"
}

// IsGrantError reports whether err means the grant or the OAuth client was
// rejected, so that only a new login helps.
func IsGrantError(err error) bool {
	var refresh *RefreshError
	return errors.As(err, &refresh) && grantErrorCodes[refresh.Code]
}

func (e *RefreshError) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("token request failed: %v", e.Err)
	case e.Code != "":
		return fmt.Sprintf("token request failed: HTTP %d, %s", e.Status, e.Code)
	default:
		return fmt.Sprintf("token request failed: HTTP %d", e.Status)
	}
}

func (e *RefreshError) Unwrap() error { return e.Err }

// OAuth2 mints access tokens with the refresh token grant. Concurrent callers
// share one refresh.
type OAuth2 struct {
	cfg OAuth2Config

	mu           sync.Mutex
	refreshToken string
	access       string
	expiry       time.Time
	inflight     *refreshCall
	lastErr      error
	lastErrAt    time.Time
}

type refreshCall struct {
	done   chan struct{}
	access string
	err    error
}

// NewOAuth2 returns an OAuth2 source.
func NewOAuth2(cfg OAuth2Config) *OAuth2 {
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &OAuth2{cfg: cfg, refreshToken: cfg.RefreshToken}
}

// Token returns a cached access token that is valid for at least
// ExpiryMargin, or waits for a refresh.
func (o *OAuth2) Token(ctx context.Context) (string, error) {
	o.mu.Lock()
	now := o.cfg.Now()
	if o.access != "" && now.Before(o.expiry) {
		access := o.access
		o.mu.Unlock()
		return access, nil
	}
	if o.inflight == nil && o.lastErr != nil && now.Sub(o.lastErrAt) < failureBackoff {
		err := o.lastErr
		o.mu.Unlock()
		return "", err
	}
	call := o.inflight
	if call == nil {
		call = &refreshCall{done: make(chan struct{})}
		o.inflight = call
		// #nosec G118 -- the shared refresh must outlive the context of the
		// caller that started it. It has its own timeout.
		go o.refresh(call)
	}
	o.mu.Unlock()

	select {
	case <-call.done:
		return call.access, call.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Invalidate drops the cached access token if it is value.
func (o *OAuth2) Invalidate(value string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if value != "" && o.access == value {
		o.access = ""
		o.expiry = time.Time{}
	}
}

func (o *OAuth2) refresh(call *refreshCall) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	o.mu.Lock()
	refreshToken := o.refreshToken
	o.mu.Unlock()

	resp, err := o.exchange(ctx, refreshToken)

	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.cfg.Now()
	if err != nil {
		call.err = err
		o.lastErr, o.lastErrAt = err, now
	} else {
		lifetime := defaultLifetime
		if resp.ExpiresIn > 0 {
			lifetime = time.Duration(resp.ExpiresIn) * time.Second
		}
		usable := lifetime - ExpiryMargin
		if usable <= 0 {
			usable = lifetime / 2
		}
		o.access = resp.AccessToken
		o.expiry = now.Add(usable)
		if resp.RefreshToken != "" {
			// A rotated refresh token is kept in memory only.
			o.refreshToken = resp.RefreshToken
		}
		o.lastErr = nil
		call.access = resp.AccessToken
	}
	o.inflight = nil
	close(call.done)
}

// Response is a successful token endpoint response. Scope lists the granted
// scopes, space separated, when the endpoint names them.
type Response struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func (o *OAuth2) exchange(ctx context.Context, refreshToken string) (Response, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {o.cfg.ClientID},
	}
	if o.cfg.ClientSecret != "" {
		form.Set("client_secret", o.cfg.ClientSecret)
	}
	return Exchange(ctx, o.cfg.Client, o.cfg.TokenURL, form)
}

// Exchange posts form to the token endpoint at tokenURL, for a refresh or an
// authorization code exchange. It never follows a redirect, which would send
// the form to wherever it points. A nil client means http.DefaultClient.
// Errors are *RefreshError values: they carry the HTTP status, an OAuth2
// error code of the RFC 6749 shape, and transport errors without the URL,
// never the response body or the form.
func Exchange(ctx context.Context, client *http.Client, tokenURL string, form url.Values) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Response{}, &RefreshError{Err: errors.New("invalid token URL")}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := noRedirects(client).Do(req) // #nosec G704 -- the token URL comes from the host-validated filter spec.
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return Response{}, &RefreshError{Err: fmt.Errorf("%w: %v", ErrUnreachable, err)}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil || len(body) > MaxResponseBytes {
		return Response{}, &RefreshError{Status: resp.StatusCode, Err: errors.New("unreadable token response")}
	}
	var parsed struct {
		Response
		Error string `json:"error"`
	}
	jsonErr := json.Unmarshal(body, &parsed)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Response{}, &RefreshError{Status: resp.StatusCode, Code: SafeCode(parsed.Error)}
	}
	if jsonErr != nil {
		return Response{}, &RefreshError{Status: resp.StatusCode, Err: errors.New("token response is not JSON")}
	}
	if parsed.AccessToken == "" || HasUnsafe(parsed.AccessToken) {
		return Response{}, &RefreshError{Status: resp.StatusCode, Err: errors.New("token response has no usable access_token")}
	}
	if HasUnsafe(parsed.RefreshToken) {
		return Response{}, &RefreshError{Status: resp.StatusCode, Err: errors.New("token response has an unusable refresh_token")}
	}
	return parsed.Response, nil
}

// noRedirects returns a copy of client that never follows a redirect.
func noRedirects(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &c
}

// SafeCode keeps an OAuth2 error code only when it has the RFC 6749 shape,
// so a hostile endpoint cannot smuggle text into logs.
func SafeCode(code string) string {
	if len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if !isCodeChar(r) {
			return ""
		}
	}
	return code
}

func isCodeChar(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// HasUnsafe reports whether s holds a space or a character outside printable
// ASCII. OAuth2 tokens, codes, and client credentials never do (RFC 6749,
// appendix A), and a value that does could split a header or a log line.
func HasUnsafe(s string) bool {
	for _, r := range s {
		if r <= 0x20 || r >= 0x7f {
			return true
		}
	}
	return false
}
