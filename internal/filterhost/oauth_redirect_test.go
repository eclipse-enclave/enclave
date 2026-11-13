// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/filter/spec"
)

// redirectingEndpoint answers every request with a 307 to a plain HTTP sink
// and records what the sink receives.
type redirectingEndpoint struct {
	url string

	mu     sync.Mutex
	leaked []string
}

func newRedirectingEndpoint(t *testing.T) *redirectingEndpoint {
	t.Helper()
	e := &redirectingEndpoint{}
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.leaked = append(e.leaked, string(body))
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","scope":"s"}`)
	}))
	t.Cleanup(sink.Close)
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(endpoint.Close)
	e.url = endpoint.URL
	return e
}

func (e *redirectingEndpoint) sinkSaw() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.leaked...)
}

// A client like the one the CLI builds: redirects are on by default in
// net/http, so only the filterhost wrapper keeps them off.
func insecureClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- test server
	}}
}

// Regression: a token endpoint that redirects must not receive the refresh
// token and client secret a second time at the redirect target.
func TestImportDoesNotFollowTokenRedirect(t *testing.T) {
	endpoint := newRedirectingEndpoint(t)
	f := &spec.Filter{Name: "gws", Credential: spec.Credential{OAuth2: &spec.OAuth2{TokenURL: endpoint.url + "/token"}}}
	cliClient := NewHTTPClient()
	cliClient.Transport = insecureClient().Transport
	for _, client := range []*http.Client{insecureClient(), cliClient} {
		_, err := Import(context.Background(), ImportRequest{
			Filter:     f,
			Data:       []byte(`{"client_id":"cid","client_secret":"SECRET","refresh_token":"REFRESH"}`),
			HTTPClient: client,
		})
		if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
			t.Fatalf("Import error = %v, want the redirect reported as HTTP 307", err)
		}
	}
	if leaked := endpoint.sinkSaw(); len(leaked) != 0 {
		t.Fatalf("redirect target received %q", leaked)
	}
}

func TestRevokeDoesNotFollowRedirect(t *testing.T) {
	endpoint := newRedirectingEndpoint(t)
	f := &spec.Filter{Name: "gws", Credential: spec.Credential{OAuth2: &spec.OAuth2{RevokeURL: endpoint.url + "/revoke"}}}
	ok, err := Revoke(context.Background(), insecureClient(), f, Grant{ID: "g", RefreshToken: "REFRESH"})
	if ok || err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("Revoke = %v, %v, want the redirect reported as HTTP 307", ok, err)
	}
	if leaked := endpoint.sinkSaw(); len(leaked) != 0 {
		t.Fatalf("redirect target received %q", leaked)
	}
}

func TestLoginExchangeDoesNotFollowRedirect(t *testing.T) {
	oauth := newFakeOAuth(t)
	f := oauth.gwsFilter(t, t.TempDir())
	endpoint := newRedirectingEndpoint(t)
	f.Credential.OAuth2.TokenURL = endpoint.url + "/token"
	_, err := Login(context.Background(), LoginRequest{
		Filter:      f,
		Client:      Client{ClientID: "client-1", ClientSecret: "SECRET"},
		Scopes:      []string{scopePrefix + "gmail.readonly"},
		GrantID:     "readonly",
		OpenBrowser: oauth.browser,
		HTTPClient:  insecureClient(),
		Timeout:     10 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("Login error = %v, want the redirect reported as HTTP 307", err)
	}
	if leaked := endpoint.sinkSaw(); len(leaked) != 0 {
		t.Fatalf("redirect target received %q", leaked)
	}
}
