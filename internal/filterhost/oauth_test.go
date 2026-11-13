// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/filter/spec"
)

const scopePrefix = "https://www.googleapis.com/auth/"

func TestLoginRunsPKCEFlow(t *testing.T) {
	oauth := newFakeOAuth(t)
	f := oauth.gwsFilter(t, t.TempDir())
	oauth.scope = scopePrefix + "gmail.readonly " + scopePrefix + "drive.readonly"
	var out bytes.Buffer

	g, err := Login(context.Background(), LoginRequest{
		Filter:      f,
		Client:      Client{ClientID: "client-1", ClientSecret: "secret-1"},
		Scopes:      []string{scopePrefix + "drive.readonly", scopePrefix + "gmail.readonly"},
		GrantID:     "readonly",
		Out:         &out,
		OpenBrowser: oauth.browser,
		HTTPClient:  oauth.server.Client(),
		Timeout:     10 * time.Second,
		Now:         func() time.Time { return time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Login: %v\n%s", err, out.String())
	}
	if g.ID != "readonly" || g.Filter != "gws" || g.RefreshToken != "rt-new" || g.ClientSecret != "secret-1" || g.Imported() ||
		!slices.Equal(g.Scopes, []string{scopePrefix + "drive.readonly", scopePrefix + "gmail.readonly"}) {
		t.Fatalf("grant = %+v", g)
	}

	q := oauth.authQuery
	redirect, err := url.Parse(q["redirect_uri"])
	if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" {
		t.Errorf("redirect_uri = %q", q["redirect_uri"])
	}
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "client-1",
		"code_challenge_method": "S256",
		"access_type":           "offline",
		"prompt":                "consent",
		"scope":                 scopePrefix + "drive.readonly " + scopePrefix + "gmail.readonly",
	} {
		if q[key] != want {
			t.Errorf("auth param %s = %q, want %q", key, q[key], want)
		}
	}
	if len(q["state"]) < 40 || len(q["code_challenge"]) != 43 {
		t.Errorf("state %q or code_challenge %q has the wrong length", q["state"], q["code_challenge"])
	}
	forms := oauth.forms()
	if len(forms) != 1 || forms[0]["grant_type"] != "authorization_code" || forms[0]["client_secret"] != "secret-1" ||
		forms[0]["redirect_uri"] != q["redirect_uri"] || len(forms[0]["code_verifier"]) != 43 {
		t.Errorf("token request = %v", forms)
	}
	if !strings.Contains(out.String(), "Open this URL to authorize filter gws") {
		t.Errorf("output = %q", out.String())
	}
	if strings.Contains(out.String(), "secret-1") || strings.Contains(out.String(), "rt-new") {
		t.Errorf("output leaks a secret: %q", out.String())
	}
}

func TestLoginCallbackRejectsForgedState(t *testing.T) {
	oauth := newFakeOAuth(t)
	f := oauth.gwsFilter(t, t.TempDir())
	forged := make(chan int, 1)
	browser := func(authURL string) error {
		u, _ := url.Parse(authURL)
		redirect := u.Query().Get("redirect_uri")
		go func() {
			status := 0
			resp, err := http.Get(redirect + "?code=evil&state=forged") // #nosec G107 -- test URL.
			if err == nil {
				status = resp.StatusCode
				_ = resp.Body.Close()
			}
			forged <- status
			_ = oauth.browser(authURL)
		}()
		return nil
	}
	g, err := Login(context.Background(), LoginRequest{
		Filter: f, Client: Client{ClientID: "c"}, Scopes: []string{scopePrefix + "gmail.readonly"}, GrantID: "x",
		OpenBrowser: browser, HTTPClient: oauth.server.Client(), Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	select {
	case status := <-forged:
		if status != http.StatusBadRequest {
			t.Errorf("forged callback status = %d, want 400", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the forged callback never completed")
	}
	if g.RefreshToken != "rt-new" {
		t.Errorf("grant = %+v", g)
	}
	if forms := oauth.forms(); len(forms) != 1 || forms[0]["code"] != "the-code" {
		t.Errorf("token requests = %v", forms)
	}
}

func TestLoginFailures(t *testing.T) {
	t.Run("authorization error", func(t *testing.T) {
		oauth := newFakeOAuth(t)
		f := oauth.gwsFilter(t, t.TempDir())
		browser := func(authURL string) error {
			u, _ := url.Parse(authURL)
			go func() {
				resp, err := http.Get(u.Query().Get("redirect_uri") + "?error=access_denied&state=" + u.Query().Get("state")) // #nosec G107 -- test URL.
				if err == nil {
					_ = resp.Body.Close()
				}
			}()
			return nil
		}
		_, err := Login(context.Background(), LoginRequest{Filter: f, Client: Client{ClientID: "c"},
			Scopes: []string{"s"}, OpenBrowser: browser, HTTPClient: oauth.server.Client(), Timeout: 10 * time.Second})
		if err == nil || !strings.Contains(err.Error(), "access_denied") {
			t.Fatalf("Login = %v, want access_denied", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		oauth := newFakeOAuth(t)
		f := oauth.gwsFilter(t, t.TempDir())
		_, err := Login(context.Background(), LoginRequest{Filter: f, Client: Client{ClientID: "c"},
			Scopes: []string{"s"}, HTTPClient: oauth.server.Client(), Timeout: 100 * time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("Login = %v, want a timeout", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		oauth := newFakeOAuth(t)
		f := oauth.gwsFilter(t, t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Login(ctx, LoginRequest{Filter: f, Client: Client{ClientID: "c"},
			Scopes: []string{"s"}, HTTPClient: oauth.server.Client(), Timeout: 10 * time.Second})
		if err == nil || !strings.Contains(err.Error(), "login canceled") || strings.Contains(err.Error(), "timed out") {
			t.Fatalf("Login = %v, want canceled", err)
		}
	})
	t.Run("no refresh token", func(t *testing.T) {
		oauth := newFakeOAuth(t)
		oauth.noRefresh = true
		f := oauth.gwsFilter(t, t.TempDir())
		_, err := Login(context.Background(), LoginRequest{Filter: f, Client: Client{ClientID: "c"},
			Scopes: []string{"s"}, OpenBrowser: oauth.browser, HTTPClient: oauth.server.Client(), Timeout: 10 * time.Second})
		if err == nil || !strings.Contains(err.Error(), "no refresh token") {
			t.Fatalf("Login = %v, want no refresh token", err)
		}
	})
	t.Run("rejected code", func(t *testing.T) {
		oauth := newFakeOAuth(t)
		f := oauth.gwsFilter(t, t.TempDir())
		oauth.code = "other"
		browser := func(authURL string) error {
			u, _ := url.Parse(authURL)
			go func() {
				resp, err := http.Get(u.Query().Get("redirect_uri") + "?code=wrong&state=" + u.Query().Get("state")) // #nosec G107 -- test URL.
				if err == nil {
					_ = resp.Body.Close()
				}
			}()
			return nil
		}
		_, err := Login(context.Background(), LoginRequest{Filter: f, Client: Client{ClientID: "c"},
			Scopes: []string{"s"}, OpenBrowser: browser, HTTPClient: oauth.server.Client(), Timeout: 10 * time.Second})
		if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
			t.Fatalf("Login = %v, want invalid_grant", err)
		}
	})
}

// The wait timeout ends the wait for the redirect only: an approval just
// before it still gets its code exchanged.
func TestLoginExchangeOutlivesTheWait(t *testing.T) {
	oauth := newFakeOAuth(t)
	f := oauth.gwsFilter(t, t.TempDir())
	oauth.tokenDelay = 600 * time.Millisecond
	browser := func(authURL string) error {
		go func() {
			time.Sleep(200 * time.Millisecond)
			_ = oauth.browser(authURL)
		}()
		return nil
	}
	g, err := Login(context.Background(), LoginRequest{
		Filter: f, Client: Client{ClientID: "c"}, Scopes: []string{scopePrefix + "gmail.readonly"}, GrantID: "x",
		OpenBrowser: browser, HTTPClient: oauth.server.Client(), Timeout: 500 * time.Millisecond,
	})
	if err != nil || g.RefreshToken != "rt-new" {
		t.Fatalf("Login = %+v, %v", g, err)
	}
}

func TestImportRefreshesOnce(t *testing.T) {
	oauth := newFakeOAuth(t)
	f := oauth.gwsFilter(t, t.TempDir())
	oauth.scope = scopePrefix + "gmail.readonly"
	doc := []byte(`{"type":"authorized_user","client_id":"client-2","client_secret":"secret-2","refresh_token":"rt-imported"}`)

	g, err := Import(context.Background(), ImportRequest{Filter: f, Data: doc, HTTPClient: oauth.server.Client()})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if g.ClientID != "client-2" || g.RefreshToken != "rt-imported" || !slices.Equal(g.Scopes, []string{scopePrefix + "gmail.readonly"}) ||
		!strings.HasPrefix(g.ID, "custom-") || !g.Imported() {
		t.Fatalf("grant = %+v %v", g, g.Scopes)
	}
	forms := oauth.forms()
	if len(forms) != 1 || forms[0]["grant_type"] != "refresh_token" || forms[0]["client_secret"] != "secret-2" {
		t.Fatalf("token requests = %v", forms)
	}

	// Without a scope in the response, --scopes supplies them.
	oauth.scope = ""
	g, err = Import(context.Background(), ImportRequest{Filter: f, Data: doc, GrantID: "mine",
		Scopes: []string{scopePrefix + "drive"}, HTTPClient: oauth.server.Client()})
	if err != nil || g.ID != "mine" || !slices.Equal(g.Scopes, []string{scopePrefix + "drive"}) {
		t.Fatalf("Import with --scopes = %+v, %v", g, err)
	}
	if _, err := Import(context.Background(), ImportRequest{Filter: f, Data: doc, HTTPClient: oauth.server.Client()}); err == nil ||
		!strings.Contains(err.Error(), "--scopes") {
		t.Fatalf("Import without any scopes = %v", err)
	}

	bad := []byte(`{"client_id":"c","refresh_token":"rt-wrong"}`)
	_, err = Import(context.Background(), ImportRequest{Filter: f, Data: bad, HTTPClient: oauth.server.Client()})
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "rt-") {
		t.Fatalf("Import(bad) = %v, want invalid_grant without the token", err)
	}
	if _, err := Import(context.Background(), ImportRequest{Filter: f, Data: []byte(`{"client_id":"c"}`)}); err == nil {
		t.Fatal("Import accepted a document without a refresh token")
	}
}

// Login and import accept only the tokens the gateway accepts at refresh:
// printable ASCII without spaces.
func TestImportRejectsNonASCIITokens(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r\u00e9","scope":"s"}`)
	}))
	t.Cleanup(server.Close)
	f := &spec.Filter{Name: "gws", Credential: spec.Credential{OAuth2: &spec.OAuth2{TokenURL: server.URL + "/token"}}}
	_, err := Import(context.Background(), ImportRequest{Filter: f, Data: []byte(`{"client_id":"c","refresh_token":"rt"}`), HTTPClient: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "unusable refresh_token") {
		t.Fatalf("Import with a non-ASCII rotated token = %v", err)
	}
	_, err = Import(context.Background(), ImportRequest{Filter: f, Data: []byte(`{"client_id":"c","refresh_token":"rt\u00e9"}`), HTTPClient: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "invalid credential values") {
		t.Fatalf("Import of a non-ASCII token = %v", err)
	}
}

func TestRevoke(t *testing.T) {
	oauth := newFakeOAuth(t)
	f := oauth.gwsFilter(t, t.TempDir())
	ok, err := Revoke(context.Background(), oauth.server.Client(), f, testGrant("readonly", "a"))
	if err != nil || !ok || !slices.Equal(oauth.revoked, []string{"rt-readonly"}) {
		t.Fatalf("Revoke = %v, %v, revoked %v", ok, err, oauth.revoked)
	}
	oauth.revokeCode = http.StatusBadRequest
	if ok, err := Revoke(context.Background(), oauth.server.Client(), f, testGrant("x", "a")); ok || err == nil || strings.Contains(err.Error(), "rt-x") {
		t.Fatalf("Revoke failure = %v, %v", ok, err)
	}
	f.Credential.OAuth2.RevokeURL = ""
	if ok, err := Revoke(context.Background(), oauth.server.Client(), f, testGrant("y", "a")); ok || err != nil {
		t.Fatalf("Revoke without revokeUrl = %v, %v", ok, err)
	}
}

func TestShowFilterAndPreset(t *testing.T) {
	oauth := newFakeOAuth(t)
	f := oauth.gwsFilter(t, t.TempDir())
	home := t.TempDir()
	writeFile(t, UserPresetsDir(home, "gws")+"/mine.yaml", "description: Mine.\nextends: readonly\n")
	var out bytes.Buffer
	if err := Show(&out, f, home, "send-eclipsesource", nil); err != nil {
		t.Fatalf("Show: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"gws (Google Workspace (test fixture))",
		"hosts:", "gmail.googleapis.com",
		"confine:", "googleapis.com",
		"oauth2, injected as authorization: Bearer <credential>",
		"discovery", "public",
		"files.read", "drive.readonly", "resources", "resource, resolver driveParents, default *",
		"mail.read", "Messages whose content was read (messages)",
		"readonly (default)", "mine", "user",
		"preset send-eclipsesource (shipped",
		"recipients: @eclipsesource.com",
		"mail.sent", "20",
		scopePrefix + "gmail.send",
		"grant: none, run: enclave filters login gws --preset send-eclipsesource",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("show output does not contain %q:\n%s", want, text)
		}
	}
	store := NewStore(home, "gws")
	if err := store.SaveGrant(testGrant("all", scopePrefix+"gmail.modify", scopePrefix+"drive")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Show(&out, f, home, "send-eclipsesource", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "grant: all") {
		t.Errorf("show does not name the covering grant:\n%s", out.String())
	}

	// The extends chain names presets and where they come from.
	writeFile(t, UserPresetsDir(home, "gws")+"/readonly.yaml", "extends: readonly\n")
	writeFile(t, UserPresetsDir(home, "gws")+"/team.yaml", "extends: send-eclipsesource\n")
	out.Reset()
	if err := Show(&out, f, home, "team", nil); err != nil {
		t.Fatal(err)
	}
	if want := "  extends: send-eclipsesource (shipped) -> readonly (user) -> readonly (shipped)\n"; !strings.Contains(out.String(), want) {
		t.Errorf("show output does not contain %q:\n%s", want, out.String())
	}

	// A preset that does not resolve prints nothing but the error.
	writeFile(t, UserPresetsDir(home, "gws")+"/broken.yaml", "allow:\n  nosuch: {}\n")
	for _, name := range []string{"broken", "nosuch"} {
		out.Reset()
		if err := Show(&out, f, home, name, nil); err == nil || out.Len() != 0 {
			t.Fatalf("Show(%s) = %v, output %q", name, err, out.String())
		}
	}
}
