// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureJWT(_ *testing.T, claims map[string]any, signature string) string {
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + signature
}

type openaiFixture struct {
	t              *testing.T
	b              *Broker
	access, id, rt string
	account        string
	calls          int
	lastForm       url.Values
	lastJSON       map[string]string
	respond        func(*http.Request) (*http.Response, error)
}

func newOpenAIFixture(t *testing.T, tool string) *openaiFixture {
	t.Helper()
	b := fixtureToolBroker(t, tool)
	f := &openaiFixture{t: t, b: b, account: "acct-1", rt: "synthetic-openai-refresh-token-01"}
	f.access = fixtureJWT(t, map[string]any{"exp": b.now().Unix() + 3600, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": f.account}}, "real-access-signature")
	f.id = fixtureJWT(t, map[string]any{"email": "fixture@example.test", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": f.account, "chatgpt_plan_type": "pro"}}, "real-id-signature")
	return f
}

func (f *openaiFixture) transport() fixtureTransport {
	return func(req *http.Request) (*http.Response, error) {
		f.calls++
		data, _ := io.ReadAll(req.Body)
		f.lastForm, f.lastJSON = nil, nil
		if strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
			_ = json.Unmarshal(data, &f.lastJSON)
		} else {
			f.lastForm, _ = url.ParseQuery(string(data))
		}
		if f.respond != nil {
			return f.respond(req)
		}
		return fixtureResponse(map[string]any{"access_token": f.access, "refresh_token": f.rt, "id_token": f.id, "expires_in": 3600}), nil
	}
}

func (f *openaiFixture) request(json bool, fields map[string]string) *http.Request {
	f.t.Helper()
	var body, contentType string
	if json {
		data, _ := jsonMarshal(fields)
		body, contentType = string(data), "application/json"
	} else {
		values := url.Values{}
		for key, value := range fields {
			values.Set(key, value)
		}
		body, contentType = values.Encode(), "application/x-www-form-urlencoded"
	}
	req, err := http.NewRequest(http.MethodPost, "https://auth.openai.com/oauth/token", strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	return req
}

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }

func (f *openaiFixture) login() map[string]any {
	f.t.Helper()
	response, err := fixtureRoundTrip(f.b, f.request(false, map[string]string{"grant_type": "authorization_code", "client_id": openaiClientID, "code": "synthetic-code", "code_verifier": "synthetic-verifier", "redirect_uri": "http://127.0.0.1:1455/auth/callback"}), f.transport())
	if err != nil {
		f.t.Fatal(err)
	}
	return f.body(response)
}

func (f *openaiFixture) body(response *http.Response) map[string]any {
	f.t.Helper()
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, real := range []string{f.access, f.id, f.rt, "real-access-signature", "real-id-signature"} {
		if strings.Contains(string(data), real) {
			f.t.Fatalf("projected response carries a real token: %s", data)
		}
	}
	var projected map[string]any
	if err := json.Unmarshal(data, &projected); err != nil {
		f.t.Fatal(err)
	}
	return projected
}

func TestOpenAILoginProjectsJWTShapedHandles(t *testing.T) {
	f := newOpenAIFixture(t, "codex")
	projected := f.login()
	if f.lastForm.Get("code") != "synthetic-code" || f.lastForm.Get("client_id") != openaiClientID {
		t.Fatalf("exchange not forwarded as a form: %v", f.lastForm)
	}
	access, _ := projected["access_token"].(string)
	id, _ := projected["id_token"].(string)
	if !strings.HasSuffix(access, "."+f.b.openai.Access) || openaiAccount(access) != f.account || openaiExpiry(access) != openaiExpiry(f.access) {
		t.Fatalf("access handle does not keep the real claims: %q", access)
	}
	if !strings.HasSuffix(id, "."+f.b.openai.ID) || openaiAccount(id) != f.account {
		t.Fatalf("id handle does not keep the real claims: %q", id)
	}
	if projected["refresh_token"] != f.b.openai.Refresh || projected["expires_in"] == nil {
		t.Fatalf("projection = %v", projected)
	}
	g, err := f.b.readOpenAIGrant()
	if err != nil || g.Token.Access != f.access || g.Token.Refresh != f.rt || g.AccountID != f.account {
		t.Fatalf("grant not committed: %+v, %v", g, err)
	}
}

func TestOpenAIReleasesAccessHandleOnlyToChatGPT(t *testing.T) {
	f := newOpenAIFixture(t, "codex")
	access := f.login()["access_token"].(string)
	for _, tc := range []struct {
		url      string
		released bool
	}{
		{"https://chatgpt.com/backend-api/codex/responses", true},
		{"https://chatgpt.com/backend-api/wham/usage", true},
		{"https://api.openai.com/v1/responses", false},
		{"https://reflect.example.test/", false},
	} {
		req, err := http.NewRequest(http.MethodPost, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("ChatGPT-Account-ID", f.account)
		var seen string
		response, err := fixtureRoundTrip(f.b, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
			seen = out.Header.Get("Authorization")
			return rawFixtureResponse([]byte(`{}`)), nil
		}))
		if tc.released {
			if err != nil || seen != "Bearer "+f.access {
				t.Fatalf("%s: release = %v, upstream saw %q", tc.url, err, seen)
			}
			_ = response.Body.Close()
		} else if !errors.Is(err, ErrDenied) || seen != "" {
			t.Fatalf("%s: handle escaped: %v, %q", tc.url, err, seen)
		}
	}
}

func TestOpenAIRefreshRotatesBehindStableHandles(t *testing.T) {
	for _, encoding := range []string{"json", "form"} {
		t.Run(encoding, func(t *testing.T) {
			f := newOpenAIFixture(t, "codex")
			first := f.login()
			f.b.now = func() time.Time { return time.Unix(1800000000+3500, 0) }
			rotated := fixtureJWT(t, map[string]any{"exp": int64(1800000000 + 7200), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": f.account}}, "rotated-access-signature")
			f.respond = func(*http.Request) (*http.Response, error) {
				// The provider may omit replacements; the saved refresh token stays.
				return fixtureResponse(map[string]any{"access_token": rotated, "expires_in": 3600}), nil
			}
			response, err := fixtureRoundTrip(f.b, f.request(encoding == "json", map[string]string{"grant_type": "refresh_token", "client_id": openaiClientID, "refresh_token": f.b.openai.Refresh}), f.transport())
			if err != nil {
				t.Fatal(err)
			}
			projected := f.body(response)
			sent := f.lastForm.Get("refresh_token")
			if encoding == "json" {
				sent = f.lastJSON["refresh_token"]
			}
			if sent != f.rt {
				t.Fatalf("upstream received %q, want the real refresh token", sent)
			}
			access := projected["access_token"].(string)
			if access == first["access_token"] || !strings.HasSuffix(access, "."+f.b.openai.Access) || projected["refresh_token"] != f.b.openai.Refresh || projected["id_token"] != first["id_token"] {
				t.Fatalf("refresh projection = %v", projected)
			}
			g, _ := f.b.readOpenAIGrant()
			if g.Token.Access != rotated || g.Token.Refresh != f.rt || g.Generation != 2 {
				t.Fatalf("rotation not committed: %+v", g)
			}
			calls := f.calls
			if _, err := fixtureRoundTrip(f.b, f.request(encoding == "json", map[string]string{"grant_type": "refresh_token", "client_id": openaiClientID, "refresh_token": f.b.openai.Refresh}), f.transport()); err != nil || f.calls != calls {
				t.Fatalf("duplicate refresh redispatched: %v, calls %d -> %d", err, calls, f.calls)
			}
		})
	}
}

func TestOpenAIRefusesUncapturedGrantsAndForeignHandles(t *testing.T) {
	f := newOpenAIFixture(t, "codex")
	f.login()
	for name, fields := range map[string]map[string]string{
		"api-key exchange": {"grant_type": "urn:ietf:params:oauth:grant-type:token-exchange", "client_id": openaiClientID, "requested_token": "openai-api-key", "subject_token": "x"},
		"wrong refresh":    {"grant_type": "refresh_token", "client_id": openaiClientID, "refresh_token": openaiHandleMarker + "R" + strings.Repeat("0", 64)},
		"other client":     {"grant_type": "authorization_code", "client_id": "other", "code": "c", "code_verifier": "v", "redirect_uri": "r"},
		"relogin":          {"grant_type": "authorization_code", "client_id": openaiClientID, "code": "c", "code_verifier": "v", "redirect_uri": "r"},
	} {
		calls := f.calls
		if _, err := fixtureRoundTrip(f.b, f.request(false, fields), f.transport()); !errors.Is(err, ErrDenied) || f.calls != calls {
			t.Fatalf("%s: dispatched or accepted: %v", name, err)
		}
	}
}

func TestOpenAIRefreshFailuresFailClosed(t *testing.T) {
	for name, respond := range map[string]func(*http.Request) (*http.Response, error){
		"network": func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF },
		"account switch": func(*http.Request) (*http.Response, error) {
			other := fixtureJWT(nil, map[string]any{"exp": int64(1800000000 + 7200), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-2"}}, "s")
			return fixtureResponse(map[string]any{"access_token": other, "expires_in": 3600}), nil
		},
		"unknown field": func(*http.Request) (*http.Response, error) {
			same := fixtureJWT(nil, map[string]any{"exp": int64(1800000000 + 7200), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"}}, "s")
			return fixtureResponse(map[string]any{"access_token": same, "expires_in": 3600, "surprise": true}), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOpenAIFixture(t, "codex")
			f.login()
			f.b.now = func() time.Time { return time.Unix(1800000000+3500, 0) }
			f.respond = respond
			if _, err := fixtureRoundTrip(f.b, f.request(true, map[string]string{"grant_type": "refresh_token", "client_id": openaiClientID, "refresh_token": f.b.openai.Refresh}), f.transport()); err == nil {
				t.Fatal("failed refresh projected tokens")
			}
			g, err := f.b.readOpenAIGrant()
			if err != nil || g.Status != "uncertain" || g.Token.Refresh != f.rt {
				t.Fatalf("failed refresh state = %+v, %v", g, err)
			}
		})
	}
}

func TestOpenAINativeViews(t *testing.T) {
	for _, tc := range []struct {
		tool  string
		check func(t *testing.T, f *openaiFixture, data map[string]any)
	}{
		{"codex", func(t *testing.T, f *openaiFixture, data map[string]any) {
			tokens := data["tokens"].(map[string]any)
			if data["auth_mode"] != "chatgpt" || data["last_refresh"] == nil || tokens["refresh_token"] != f.b.openai.Refresh || tokens["account_id"] != f.account || !strings.HasSuffix(tokens["id_token"].(string), f.b.openai.ID) {
				t.Fatalf("codex auth.json = %v", data)
			}
		}},
		{"opencode", func(t *testing.T, f *openaiFixture, data map[string]any) {
			entry := data["openai"].(map[string]any)
			if entry["type"] != "oauth" || entry["refresh"] != f.b.openai.Refresh || entry["accountId"] != f.account || entry["expires"].(float64) <= 0 {
				t.Fatalf("opencode auth.json = %v", data)
			}
		}},
		{"pi", func(t *testing.T, f *openaiFixture, data map[string]any) {
			entry := data["openai-codex"].(map[string]any)
			if entry["type"] != "oauth" || !strings.HasSuffix(entry["access"].(string), f.b.openai.Access) || entry["accountId"] != f.account {
				t.Fatalf("pi auth.json = %v", data)
			}
		}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			f := newOpenAIFixture(t, tc.tool)
			f.login()
			next := reopenAsTool(t, f.b, tc.tool)
			view := filepath.Join(t.TempDir(), "view")
			if err := os.Mkdir(view, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := next.InitializeView(view); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(view, "auth.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), f.rt) || strings.Contains(string(raw), "real-access-signature") {
				t.Fatal("view carries real tokens")
			}
			var data map[string]any
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			f.b = next
			tc.check(t, f, data)
		})
	}
}
