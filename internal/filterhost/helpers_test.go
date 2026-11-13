// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/filter/spec"
	"enclave/internal/testutil"
)

// fakeOAuth is an authorization server: /auth redirects to the client's
// redirect_uri with a code, /token exchanges codes and refresh tokens, and
// /revoke records revoked tokens.
type fakeOAuth struct {
	t      *testing.T
	server *httptest.Server

	mu         sync.Mutex
	challenge  string
	authQuery  map[string]string
	code       string
	scope      string
	noRefresh  bool
	tokenForms []map[string]string
	revoked    []string
	revokeCode int
	// tokenDelay holds each token response back.
	tokenDelay time.Duration
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	t.Helper()
	f := &fakeOAuth{t: t, code: "the-code", revokeCode: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", f.auth)
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/revoke", f.revoke)
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOAuth) auth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.authQuery = map[string]string{}
	for key := range q {
		f.authQuery[key] = q.Get(key)
	}
	f.challenge = q.Get("code_challenge")
	f.mu.Unlock()
	target := q.Get("redirect_uri") + "?code=" + f.code + "&state=" + q.Get("state")
	http.Redirect(w, r, target, http.StatusFound)
}

func (f *fakeOAuth) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	form := map[string]string{}
	for key := range r.PostForm {
		form[key] = r.PostForm.Get(key)
	}
	f.mu.Lock()
	f.tokenForms = append(f.tokenForms, form)
	challenge := f.challenge
	delay := f.tokenDelay
	f.mu.Unlock()
	time.Sleep(delay)
	w.Header().Set("Content-Type", "application/json")
	switch form["grant_type"] {
	case "authorization_code":
		sum := sha256.Sum256([]byte(form["code_verifier"]))
		if form["code"] != f.code || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		resp := map[string]any{"access_token": "at-1", "expires_in": 3600, "scope": f.scope}
		if !f.noRefresh {
			resp["refresh_token"] = "rt-new"
		}
		_ = json.NewEncoder(w).Encode(resp)
	case "refresh_token":
		if form["refresh_token"] != "rt-imported" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"secret rt-imported"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-2", "expires_in": 3600, "scope": f.scope})
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *fakeOAuth) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.revoked = append(f.revoked, r.PostForm.Get("token"))
	code := f.revokeCode
	f.mu.Unlock()
	w.WriteHeader(code)
}

// browser follows the authorization URL like a user who approves.
func (f *fakeOAuth) browser(url string) error {
	go func() {
		resp, err := f.server.Client().Get(url) // #nosec G107 -- test URL.
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	return nil
}

// gwsFilter loads the gws fixture with its OAuth endpoints pointing at the
// fake server, installed under home's extension root when paths is used.
func (f *fakeOAuth) gwsFilter(t *testing.T, dir string) *spec.Filter {
	t.Helper()
	installed := testutil.InstallGWSFilter(t, dir)
	specPath := filepath.Join(installed, "spec.yaml")
	data, err := os.ReadFile(specPath) // #nosec G304 -- test fixture.
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer(
		"https://accounts.google.com/o/oauth2/auth", f.server.URL+"/auth",
		"https://oauth2.googleapis.com/token", f.server.URL+"/token",
		"https://oauth2.googleapis.com/revoke", f.server.URL+"/revoke",
	).Replace(string(data))
	if err := os.WriteFile(specPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := spec.Load(installed, "gws")
	if err != nil {
		t.Fatalf("load gws: %v", err)
	}
	return loaded
}

func (f *fakeOAuth) forms() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.tokenForms...)
}
