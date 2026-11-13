// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"enclave/internal/filter/spec"
	"enclave/internal/filter/token"
)

// LoginTimeout bounds how long login waits for the browser redirect.
const LoginTimeout = 5 * time.Minute

const (
	// httpTimeout bounds one token exchange or revocation.
	httpTimeout  = 30 * time.Second
	callbackPath = "/callback"
)

// redactURLError drops the URL from a transport error. Revoke URLs carry no
// secrets, but the form never belongs in a message.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// NewHTTPClient returns the client for token exchanges, refreshes, and
// revocations: a 30 second timeout, and no redirects.
func NewHTTPClient() *http.Client {
	return httpClient(nil)
}

// httpClient returns a copy of client, or a default one, that never follows
// a redirect: a token or revoke endpoint that answers with one would
// otherwise get the client secret and refresh token re-sent to wherever it
// points, plain HTTP included.
func httpClient(client *http.Client) *http.Client {
	var c http.Client
	if client != nil {
		c = *client
	} else {
		c.Timeout = httpTimeout
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &c
}

func splitScopes(scope string) []string {
	return sortedUnique(strings.Fields(scope))
}

// LoginRequest configures the loopback authorization code flow.
type LoginRequest struct {
	Filter *spec.Filter
	Client Client
	// Scopes are the full scopes to request.
	Scopes  []string
	GrantID string
	// Out receives the authorization URL and progress lines.
	Out io.Writer
	// OpenBrowser opens the URL. Nil means print only.
	OpenBrowser func(url string) error
	HTTPClient  *http.Client
	Timeout     time.Duration
	Now         func() time.Time
}

// Login runs the OAuth2 authorization code flow with PKCE (S256) and a state
// parameter against a callback server on 127.0.0.1 at a random port, and
// returns the grant. The caller stores it.
func Login(ctx context.Context, req LoginRequest) (Grant, error) {
	o := req.Filter.Credential.OAuth2
	if o == nil {
		return Grant{}, fmt.Errorf("filter %s does not use an oauth2 credential", req.Filter.Name)
	}
	if len(req.Scopes) == 0 {
		return Grant{}, fmt.Errorf("no scopes to request")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = LoginTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Grant{}, fmt.Errorf("start the login callback listener: %w", err)
	}
	redirectURI := "http://" + listener.Addr().String() + callbackPath
	state, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return Grant{}, err
	}
	verifier, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return Grant{}, err
	}
	authURL, err := authorizationURL(o, req.Client.ClientID, redirectURI, req.Scopes, state, verifier)
	if err != nil {
		_ = listener.Close()
		return Grant{}, err
	}

	codes := make(chan callbackResult, 1)
	server := &http.Server{
		Handler:           callbackHandler(state, codes),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
	}()

	out := req.Out
	if out == nil {
		out = io.Discard
	}
	_, _ = fmt.Fprintf(out, "Open this URL to authorize filter %s:\n\n  %s\n\n", req.Filter.Name, authURL)
	if req.OpenBrowser != nil {
		if err := req.OpenBrowser(authURL); err != nil {
			_, _ = fmt.Fprintf(out, "Could not open a browser (%v). Open the URL yourself.\n", err)
		}
	}
	_, _ = fmt.Fprintf(out, "Waiting for the redirect to %s (timeout %s)\n", redirectURI, timeout)

	var result callbackResult
	select {
	case result = <-codes:
	case <-waitCtx.Done():
		if err := ctx.Err(); err != nil {
			return Grant{}, fmt.Errorf("login canceled: %w", err)
		}
		return Grant{}, fmt.Errorf("login timed out after %s", timeout)
	}
	if result.err != nil {
		return Grant{}, result.err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {result.code},
		"redirect_uri":  {redirectURI},
		"client_id":     {req.Client.ClientID},
		"code_verifier": {verifier},
	}
	if req.Client.ClientSecret != "" {
		form.Set("client_secret", req.Client.ClientSecret)
	}
	// The exchange has its own deadline, so an approval just before the wait
	// times out still completes.
	exchangeCtx, stop := context.WithTimeout(ctx, httpTimeout)
	defer stop()
	resp, err := token.Exchange(exchangeCtx, httpClient(req.HTTPClient), o.TokenURL, form)
	if err != nil {
		return Grant{}, fmt.Errorf("exchange the authorization code: %w", err)
	}
	if resp.RefreshToken == "" {
		return Grant{}, fmt.Errorf("the token endpoint returned no refresh token: the filter must request offline access in credential.oauth2.authParams")
	}
	scopes := splitScopes(resp.Scope)
	if len(scopes) == 0 {
		scopes = sortedUnique(req.Scopes)
	}
	return Grant{
		SchemaVersion: GrantSchemaVersion,
		ID:            req.GrantID,
		Filter:        req.Filter.Name,
		ClientID:      req.Client.ClientID,
		ClientSecret:  req.Client.ClientSecret,
		RefreshToken:  resp.RefreshToken,
		Scopes:        scopes,
		CreatedAt:     now(req.Now),
	}, nil
}

func now(clock func() time.Time) time.Time {
	if clock == nil {
		return time.Now().UTC().Truncate(time.Second)
	}
	return clock().UTC()
}

type callbackResult struct {
	code string
	err  error
}

// callbackHandler accepts the first redirect that carries the expected state.
// A redirect with any other state is answered with 400 and ignored, so a stray
// or forged request cannot end the flow.
func callbackHandler(state string, results chan<- callbackResult) http.Handler {
	done := false
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Unexpected state parameter.", http.StatusBadRequest)
			return
		}
		if done {
			http.Error(w, "This login has already completed.", http.StatusConflict)
			return
		}
		done = true
		if code := token.SafeCode(query.Get("error")); query.Get("error") != "" {
			if code == "" {
				code = "invalid error code"
			}
			http.Error(w, "Authorization failed. Return to the terminal.", http.StatusForbidden)
			results <- callbackResult{err: fmt.Errorf("authorization failed: %s", code)}
			return
		}
		code := query.Get("code")
		if code == "" || token.HasUnsafe(code) {
			http.Error(w, "The redirect carries no authorization code.", http.StatusBadRequest)
			results <- callbackResult{err: fmt.Errorf("the redirect carried no authorization code")}
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "Authorization received. You can close this tab.\n")
		results <- callbackResult{code: code}
	})
	return serialized(mux)
}

// serialized handles one request at a time, so the handler's state needs no
// lock.
func serialized(next http.Handler) http.Handler {
	lock := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock <- struct{}{}
		defer func() { <-lock }()
		next.ServeHTTP(w, r)
	})
}

func authorizationURL(o *spec.OAuth2, clientID string, redirectURI string, scopes []string, state string, verifier string) (string, error) {
	u, err := url.Parse(o.AuthURL)
	if err != nil {
		return "", fmt.Errorf("invalid authUrl")
	}
	sum := sha256.Sum256([]byte(verifier))
	query := u.Query()
	for key, value := range o.AuthParams {
		query.Set(key, value)
	}
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", strings.Join(scopes, " "))
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	query.Set("code_challenge_method", "S256")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// randomToken returns 32 random bytes, base64url encoded: a PKCE verifier of
// 43 characters, or a state value.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ImportRequest configures importing an authorized_user credential.
type ImportRequest struct {
	Filter *spec.Filter
	// Data is the authorized_user JSON document.
	Data []byte
	// Scopes are used when the token response names none.
	Scopes     []string
	GrantID    string
	HTTPClient *http.Client
	Now        func() time.Time
}

// Import turns an authorized_user credential into a grant marked as
// imported. One refresh proves the credential works and reports the granted
// scopes. A rotated refresh token in that response replaces the imported one.
func Import(ctx context.Context, req ImportRequest) (Grant, error) {
	o := req.Filter.Credential.OAuth2
	if o == nil {
		return Grant{}, fmt.Errorf("filter %s does not use an oauth2 credential", req.Filter.Name)
	}
	user, err := parseAuthorizedUser(req.Data)
	if err != nil {
		return Grant{}, err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {user.RefreshToken},
		"client_id":     {user.ClientID},
	}
	if user.ClientSecret != "" {
		form.Set("client_secret", user.ClientSecret)
	}
	resp, err := token.Exchange(ctx, httpClient(req.HTTPClient), o.TokenURL, form)
	if err != nil {
		return Grant{}, fmt.Errorf("refresh the imported credential: %w", err)
	}
	scopes := splitScopes(resp.Scope)
	if len(scopes) == 0 {
		scopes = sortedUnique(req.Scopes)
	}
	if len(scopes) == 0 {
		return Grant{}, fmt.Errorf("the token response names no scopes: pass --scopes")
	}
	refresh := user.RefreshToken
	if resp.RefreshToken != "" {
		refresh = resp.RefreshToken
	}
	id := req.GrantID
	if id == "" {
		id = DefaultGrantID("", scopes)
	}
	return Grant{
		SchemaVersion: GrantSchemaVersion,
		ID:            id,
		Filter:        req.Filter.Name,
		ClientID:      user.ClientID,
		ClientSecret:  user.ClientSecret,
		RefreshToken:  refresh,
		Scopes:        scopes,
		Source:        GrantSourceImport,
		CreatedAt:     now(req.Now),
	}, nil
}

// Revoke revokes a refresh token at the filter's revokeUrl. Filters without
// one report nothing to do with ok false.
func Revoke(ctx context.Context, client *http.Client, f *spec.Filter, g Grant) (ok bool, err error) {
	if f.Credential.OAuth2 == nil || f.Credential.OAuth2.RevokeURL == "" {
		return false, nil
	}
	form := url.Values{"token": {g.RefreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Credential.OAuth2.RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("invalid revokeUrl")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient(client).Do(req) // #nosec G704 -- the revoke URL comes from the installed filter spec.
	if err != nil {
		return false, fmt.Errorf("revoke endpoint unreachable: %w", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, token.MaxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, fmt.Errorf("revoke endpoint returned HTTP %d", resp.StatusCode)
	}
	return true, nil
}
