// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

const githubRealAlice = "gho_real_alice_01234567890123456789012345"
const githubRealBob = "github_pat_real_bob_01234567890123456789"

func fixtureGitHubConfig(t *testing.T, hosts, preferences string) GitHubConfig {
	t.Helper()
	root := t.TempDir()
	c := GitHubConfig{StoreDir: filepath.Join(root, "grant"), RuntimeDir: filepath.Join(root, "runtime"), ViewDir: filepath.Join(root, "view"), LockPath: filepath.Join(root, "identity.lock"), Identity: "github-feature", Session: "fixture-session", Incarnation: "first"}
	for _, dir := range []string{c.StoreDir, c.RuntimeDir, c.ViewDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(c.LockPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"hosts.yml": hosts, "config.yml": preferences} {
		if err := os.WriteFile(filepath.Join(c.StoreDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func fixtureGitHub(t *testing.T, hosts string) *Broker {
	t.Helper()
	b := fixtureBroker(t)
	c := fixtureGitHubConfig(t, hosts, "version: '1'\ngit_protocol: https\naliases:\n  co: pr checkout\n")
	b.config.GitHub = &c
	combined, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	return combined
}

func githubNativeFixture() string {
	return "github.com:\n  user: alice\n  oauth_token: " + githubRealAlice + "\n  git_protocol: https\n  users:\n    alice:\n      oauth_token: " + githubRealAlice + "\n    bob:\n      oauth_token: " + githubRealBob + "\n"
}

func githubRequest(t *testing.T, path, code string) *http.Request {
	t.Helper()
	form := url.Values{"client_id": {githubClientID}}
	if path == githubDevicePath {
		form.Set("scope", "repo read:org gist")
	} else {
		form.Set("grant_type", githubDeviceGrant)
		digest := sha256.Sum256([]byte(code))
		form.Set("device_code", hex.EncodeToString(digest[:20]))
	}
	req, err := http.NewRequest(http.MethodPost, "https://github.com"+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func githubSafeBody(t *testing.T, response *http.Response) string {
	t.Helper()
	if response == nil {
		t.Fatal("missing response")
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), githubRealAlice) || strings.Contains(string(data), githubRealBob) {
		t.Fatal("provider token escaped response")
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.ContentLength != int64(len(data)) {
		t.Fatal("invalid projected framing/cache policy")
	}
	return string(data)
}

func githubSuccessTransport(t *testing.T, b *Broker, count *atomic.Int32, user, token string) fixtureTransport {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		count.Add(1)
		if req.URL.Path == "/user" {
			if req.URL.String() != "https://api.github.com/user" || req.Header.Get("Authorization") != "Bearer "+token {
				t.Error("wrong trusted account lookup")
				return nil, ErrResponse
			}
			return fixtureResponse(map[string]any{"login": user, "id": 42, "irrelevant": "discarded"}), nil
		}
		if req.URL.Path != githubTokenPath {
			t.Errorf("unexpected upstream URL %s", req.URL)
			return nil, ErrResponse
		}
		g, err := b.github.readGrant()
		if err != nil {
			t.Error(err)
			return nil, err
		}
		pending := false
		for _, value := range g.Devices {
			pending = pending || value.User == ""
		}
		if !pending {
			t.Error("exchange dispatched before durable intent")
			return nil, ErrState
		}
		if req.GetBody != nil || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" || req.Header.Get("Accept-Encoding") != "identity" {
			t.Error("unsafe exchange headers or retry body")
			return nil, ErrDenied
		}
		return fixtureResponse(map[string]string{"access_token": token, "token_type": "bearer", "scope": "repo,read:org,gist"}), nil
	}
}

func TestGitHubNativeImportProjectionReloadAndNewSession(t *testing.T) {
	b := fixtureGitHub(t, githubNativeFixture())
	if err := b.InitializeView(fixtureViewDirectory(t)); err != nil {
		t.Fatal(err)
	}
	view := b.github.config.ViewDir
	for _, name := range []string{"hosts.yml", "config.yml"} {
		data, err := os.ReadFile(filepath.Join(view, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), githubRealAlice) || strings.Contains(string(data), githubRealBob) {
			t.Fatal("native view contains real token")
		}
	}
	data, err := os.ReadFile(filepath.Join(view, "hosts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var hosts map[string]githubNativeHost
	if yaml.UnmarshalStrict(data, &hosts) != nil {
		t.Fatal("projection is not native YAML")
	}
	if hosts["github.com"].OAuthToken != b.github.registry.Handles["alice"] || hosts["github.com"].Users["bob"].OAuthToken != b.github.registry.Handles["bob"] {
		t.Fatal("incorrect account handle projection")
	}
	if b.github.registry.Handles["alice"] == b.github.registry.Handles["bob"] {
		t.Fatal("accounts share handle")
	}
	if err := os.Remove(filepath.Join(view, "hosts.yml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "config.yml"), []byte("client settings\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.github.config.StoreDir, "hosts.yml"), []byte("not a supported native schema"), 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.github.initializeView(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(view, "hosts.yml")); !os.IsNotExist(err) {
		t.Fatal("reload reversed local logout")
	}
	if reloaded.github.registry.Handles["alice"] != b.github.registry.Handles["alice"] {
		t.Fatal("reload rotated handle")
	}
	next := *b.config.GitHub
	next.RuntimeDir = fixtureViewDirectory(t)
	next.ViewDir = fixtureViewDirectory(t)
	next.Incarnation = "next"
	nextBroker, err := openGitHub(next)
	if err != nil {
		t.Fatal(err)
	}
	if nextBroker.registry.Handles["alice"] == b.github.registry.Handles["alice"] {
		t.Fatal("new session reused handle")
	}
}

func TestGitHubNativeImportRejectsUnsupportedOrAmbiguousCredentials(t *testing.T) {
	for name, body := range map[string]string{
		"unknown host":            strings.ReplaceAll(githubNativeFixture(), "github.com:", "enterprise.example:"),
		"duplicate host":          githubNativeFixture() + githubNativeFixture(),
		"credential field case":   strings.ReplaceAll(githubNativeFixture(), "oauth_token:", "OAuth_Token:"),
		"unknown credential":      githubNativeFixture() + "  refresh_token: secret\n",
		"mismatched active":       strings.Replace(githubNativeFixture(), "  oauth_token: "+githubRealAlice, "  oauth_token: "+githubRealBob, 1),
		"keyring only":            "github.com:\n  user: alice\n  users:\n    alice: {}\n",
		"multiple documents":      githubNativeFixture() + "---\n{}\n",
		"null":                    "null\n",
		"handle instead of token": strings.ReplaceAll(githubNativeFixture(), githubRealAlice, githubHandlePrefix+strings.Repeat("a", 64)),
	} {
		t.Run(name, func(t *testing.T) {
			c := fixtureGitHubConfig(t, body, "{}")
			if _, err := openGitHub(c); !errors.Is(err, ErrState) {
				t.Fatalf("accepted unsupported native file: %v", err)
			}
		})
	}
	for name, body := range map[string]string{"unknown field": "oauth_token: hidden", "unix socket": "http_unix_socket: /somewhere", "token in preference": "editor: " + githubRealAlice, "null": "null", "multiple documents": "{}\n---\n{}"} {
		t.Run("config-"+name, func(t *testing.T) {
			c := fixtureGitHubConfig(t, githubNativeFixture(), body)
			if _, err := openGitHub(c); !errors.Is(err, ErrState) {
				t.Fatalf("accepted unsafe configuration: %v", err)
			}
		})
	}
}

func TestGitHubNativeDefaultPreferences(t *testing.T) {
	// cli/cli v2.87.3 internal/config/config.go defaultConfigStr, without its
	// descriptive comments. Blank optional values are native YAML null scalars.
	preferences := `version: 1
git_protocol: https
editor:
prompt: enabled
prefer_editor_prompt: disabled
pager:
aliases:
  co: pr checkout
http_unix_socket:
browser:
color_labels: disabled
accessible_colors: disabled
accessible_prompter: disabled
spinner: enabled
`
	c := fixtureGitHubConfig(t, githubNativeFixture(), preferences)
	b, err := openGitHub(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.initializeView(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(c.ViewDir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := decodeGitHubNative(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"editor", "pager", "browser", "http_unix_socket"} {
		if value, exists := config[key]; !exists || value != "" {
			t.Fatalf("native empty preference %s changed: %#v", key, value)
		}
	}
	if _, err := openGitHub(c); err != nil {
		t.Fatalf("canonical preferences did not survive reload: %v", err)
	}
}

func TestGitHubViewRefusesExistingFilesAndWorkloadLock(t *testing.T) {
	for _, name := range []string{"hosts.yml", "config.yml"} {
		t.Run(name, func(t *testing.T) {
			b := fixtureGitHub(t, githubNativeFixture())
			if err := os.Symlink(filepath.Join(b.github.config.StoreDir, "hosts.yml"), filepath.Join(b.github.config.ViewDir, name)); err != nil {
				t.Fatal(err)
			}
			if err := b.github.initializeView(); !errors.Is(err, ErrState) {
				t.Fatal("existing view symlink accepted")
			}
		})
	}
	c := fixtureGitHubConfig(t, githubNativeFixture(), "{}")
	c.LockPath = filepath.Join(c.ViewDir, "identity.lock")
	if err := os.WriteFile(c.LockPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openGitHub(c); !errors.Is(err, ErrState) {
		t.Fatal("workload-accessible broker lock accepted")
	}
}

func TestGitHubAPIAndGitReleaseOnlyApprovedCredentialFields(t *testing.T) {
	b := fixtureGitHub(t, githubNativeFixture())
	for _, tc := range []struct {
		name, method, target, user string
		ok                         bool
	}{
		{"REST", http.MethodGet, "https://api.github.com/user", "", true},
		{"GraphQL", http.MethodPost, "https://api.github.com/graphql", "", true},
		{"git fetch refs", http.MethodGet, "https://github.com/org/repo.git/info/refs?service=git-upload-pack", "alice", true},
		{"git push", http.MethodPost, "https://github.com/org/repo.git/git-receive-pack", "x-access-token", true},
		{"git fetch refs without suffix", http.MethodGet, "https://github.com/org/repo/info/refs?service=git-upload-pack", "alice", true},
		{"git fetch without suffix", http.MethodPost, "https://github.com/org/repo/git-upload-pack", "alice", true},
		{"git push without suffix", http.MethodPost, "https://github.com/org/repo/git-receive-pack", "alice", true},
		{"git web path", http.MethodGet, "https://github.com/org/repo/settings", "alice", false},
		{"git refs wrong method", http.MethodPost, "https://github.com/org/repo/info/refs?service=git-upload-pack", "alice", false},
		{"git pack wrong method", http.MethodGet, "https://github.com/org/repo/git-upload-pack", "alice", false},
		{"git nested path", http.MethodPost, "https://github.com/org/repo/extra/git-upload-pack", "alice", false},
		{"git dot repository", http.MethodPost, "https://github.com/org/../git-upload-pack", "alice", false},
		{"git owner only", http.MethodPost, "https://github.com/org/git-upload-pack", "alice", false},
		{"other origin", http.MethodGet, "https://reflect.example/", "", false},
		{"subdomain", http.MethodGet, "https://evil.api.github.com/user", "", false},
		{"HTTP", http.MethodGet, "http://api.github.com/user", "", false},
		{"other port", http.MethodGet, "https://api.github.com:444/user", "", false},
		{"TRACE", http.MethodTrace, "https://api.github.com/user", "", false},
		{"basic API", http.MethodGet, "https://api.github.com/user", "alice", false},
		{"wrong git user", http.MethodGet, "https://github.com/org/repo.git/info/refs?service=git-upload-pack", "bob", false},
		{"git arbitrary path", http.MethodGet, "https://github.com/reflect", "alice", false},
		{"git query injection", http.MethodGet, "https://github.com/org/repo.git/info/refs?service=git-upload-pack&x=1", "alice", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			handle := b.github.registry.Handles["alice"]
			req.Header.Set("Authorization", "token "+handle)
			if tc.user != "" {
				req.SetBasicAuth(tc.user, handle)
			}
			called := false
			resp, err := fixtureRoundTrip(b, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
				called = true
				if tc.user == "" {
					if out.Header.Get("Authorization") != "Bearer "+githubRealAlice {
						t.Fatal("wrong API token")
					}
				} else {
					user, token, ok := out.BasicAuth()
					if !ok || user != tc.user || token != githubRealAlice {
						t.Fatal("wrong Git token")
					}
				}
				return fixtureResponse(map[string]bool{"ok": true}), nil
			}))
			if resp != nil {
				_ = resp.Body.Close()
			}
			if tc.ok {
				if err != nil || !called {
					t.Fatalf("valid request denied: %v", err)
				}
			} else if !errors.Is(err, ErrDenied) || called {
				t.Fatalf("unapproved request released credentials: %v", err)
			}
		})
	}
	for _, value := range []string{b.registry.Access, githubHandlePrefix + strings.Repeat("f", 64), b.github.registry.Handles["alice"]} {
		req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
		req.Header.Set("Authorization", "Bearer "+b.github.registry.Handles["alice"])
		req.Header.Set("X-Reflect", value)
		_, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) { t.Fatal("extra handle reached upstream"); return nil, nil }))
		if !errors.Is(err, ErrDenied) {
			t.Fatal("handle in arbitrary header accepted")
		}
	}
}

func TestGitHubCaptureCommitsBeforeResponseAndReplaysAfterReload(t *testing.T) {
	b := fixtureGitHub(t, "")
	var calls atomic.Int32
	transport := githubSuccessTransport(t, b, &calls, "alice", githubRealAlice)
	response, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "device-one"), transport)
	if err != nil {
		t.Fatal(err)
	}
	body := githubSafeBody(t, response)
	if !strings.Contains(body, b.github.registry.Handles["alice"]) {
		t.Fatal("captured response missing handle")
	}
	g, err := b.github.readGrant()
	if err != nil || g.Accounts["alice"].Token != githubRealAlice {
		t.Fatal("grant was not durably committed")
	}
	reloaded, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := fixtureRoundTrip(reloaded, githubRequest(t, githubTokenPath, "device-one"), transport)
	if err != nil {
		t.Fatal(err)
	}
	if githubSafeBody(t, replayed) != body || calls.Load() != 2 {
		t.Fatal("lost response replay redispatched or changed handle")
	}
	response, err = fixtureRoundTrip(reloaded, githubRequest(t, githubTokenPath, "device-two"), githubSuccessTransport(t, reloaded, &calls, "alice", githubRealBob))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(githubSafeBody(t, response), b.github.registry.Handles["alice"]) {
		t.Fatal("reauthorization changed account handle")
	}
	g, err = reloaded.github.readGrant()
	if err != nil || g.Accounts["alice"].Token != githubRealBob {
		t.Fatal("reauthorization did not replace account token")
	}
}

func TestGitHubConcurrentDevicePollsOnlyDispatchOnce(t *testing.T) {
	b := fixtureGitHub(t, "")
	var calls atomic.Int32
	transport := githubSuccessTransport(t, b, &calls, "alice", githubRealAlice)
	var wg sync.WaitGroup
	responses := make([]*http.Response, 12)
	for i := range responses {
		req := githubRequest(t, githubTokenPath, "same-device")
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := fixtureRoundTrip(b, req, transport)
			if err != nil {
				t.Error(err)
				return
			}
			responses[i] = response
		}()
	}
	wg.Wait()
	for _, response := range responses {
		_ = githubSafeBody(t, response)
	}
	if calls.Load() != 2 {
		t.Fatalf("duplicate token/user dispatches: %d", calls.Load())
	}
}

func TestGitHubDeviceBootstrapAndPendingErrorsAreSanitized(t *testing.T) {
	b := fixtureGitHub(t, "")
	device := map[string]any{"device_code": strings.Repeat("a", 40), "user_code": "ABCD-EFGH", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5}
	response, err := fixtureRoundTrip(b, githubRequest(t, githubDevicePath, ""), fixtureTransport(func(*http.Request) (*http.Response, error) {
		resp := fixtureResponse(device)
		resp.Header.Set("Set-Cookie", githubRealAlice)
		return resp, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Set-Cookie") != "" || !strings.Contains(githubSafeBody(t, response), "ABCD-EFGH") {
		t.Fatal("invalid sanitized bootstrap")
	}
	var calls int
	for _, code := range []string{"authorization_pending", "slow_down"} {
		response, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "poll-device"), fixtureTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return fixtureResponse(map[string]any{"error": code, "error_description": githubRealAlice, "error_uri": "https://example/" + githubRealBob, "interval": 10}), nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(githubSafeBody(t, response), "error_description") {
			t.Fatal("provider error detail escaped")
		}
	}
	if calls != 2 {
		t.Fatal("pending poll was incorrectly fenced")
	}
}

func TestGitHubUncertainAndMalformedRepliesNeverExposeOrRedispatch(t *testing.T) {
	for name, reply := range map[string]fixtureTransport{
		"lost upstream reply": func(*http.Request) (*http.Response, error) { return nil, errors.New("network lost") },
		"unknown response": func(*http.Request) (*http.Response, error) {
			return fixtureResponse(map[string]string{"access_token": githubRealAlice, "token_type": "bearer", "scope": "repo", "extra": githubRealBob}), nil
		},
		"refresh unsupported": func(*http.Request) (*http.Response, error) {
			return fixtureResponse(map[string]string{"access_token": githubRealAlice, "token_type": "bearer", "scope": "repo", "refresh_token": githubRealBob}), nil
		},
		"expired unsupported": func(*http.Request) (*http.Response, error) {
			return fixtureResponse(map[string]any{"access_token": githubRealAlice, "token_type": "bearer", "scope": "repo", "expires_in": 3600}), nil
		},
		"token reflected in scope": func(*http.Request) (*http.Response, error) {
			return fixtureResponse(map[string]string{"access_token": githubRealAlice, "token_type": "bearer", "scope": githubRealAlice}), nil
		},
		"unknown scope": func(*http.Request) (*http.Response, error) {
			return fixtureResponse(map[string]string{"access_token": githubRealAlice, "token_type": "bearer", "scope": "repo,new_unverified_scope"}), nil
		},
		"account lookup failed": func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/user" {
				return nil, errors.New("account lookup lost")
			}
			return fixtureResponse(map[string]string{"access_token": githubRealAlice, "token_type": "bearer", "scope": "repo"}), nil
		},
		"token reflected in account": func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/user" {
				return fixtureResponse(map[string]string{"login": "opaque-token-can-look-like-a-login"}), nil
			}
			return fixtureResponse(map[string]string{"access_token": "opaque-token-can-look-like-a-login", "token_type": "bearer", "scope": "repo"}), nil
		},
		"duplicate fields": func(*http.Request) (*http.Response, error) {
			return rawFixtureResponse([]byte(`{"access_token":"first","access_token":"second","token_type":"bearer","scope":"repo"}`)), nil
		},
		"HTML": func(*http.Request) (*http.Response, error) {
			resp := fixtureResponse(map[string]string{"access_token": githubRealAlice})
			resp.Header.Set("Content-Type", "text/html")
			return resp, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := fixtureGitHub(t, "")
			resp, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "ambiguous-device"), reply)
			if err == nil || resp != nil {
				t.Fatal("malformed exchange reached client")
			}
			reloaded, err := Open(b.config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixtureRoundTrip(reloaded, githubRequest(t, githubTokenPath, "ambiguous-device"), fixtureTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("uncertain exchange redispatched")
				return nil, nil
			}))
			if !errors.Is(err, ErrUncertain) {
				t.Fatalf("uncertain state lost: %v", err)
			}
		})
	}
}

func TestGitHubRejectsUnknownExchangeAndOldSessionHandles(t *testing.T) {
	b := fixtureGitHub(t, githubNativeFixture())
	old := b.github.registry.Handles["alice"]
	next := *b.config.GitHub
	next.RuntimeDir = fixtureViewDirectory(t)
	next.ViewDir = fixtureViewDirectory(t)
	next.Incarnation = "new"
	b.config.GitHub = &next
	newBroker, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	requests := []*http.Request{}
	for _, target := range []string{"https://api.github.com/user", "https://unrelated.example/"} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		req.SetBasicAuth("alice", old)
		requests = append(requests, req)
	}
	for _, body := range []string{"client_id=" + githubClientID + "&scope=repo+offline_access", "client_id=" + githubClientID + "&scope=repo&scope=gist"} {
		req, _ := http.NewRequest(http.MethodPost, "https://github.com"+githubDevicePath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		requests = append(requests, req)
	}
	wrong := githubRequest(t, githubTokenPath, "code")
	wrong.URL.RawQuery = "x=1"
	requests = append(requests, wrong)
	for _, req := range requests {
		_, err := fixtureRoundTrip(newBroker, req, fixtureTransport(func(*http.Request) (*http.Response, error) { t.Fatal("invalid request dispatched"); return nil, nil }))
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid request accepted: %v", err)
		}
	}
}

func TestGitHubAmbiguousOAuthPathsNeverDispatchUncaptured(t *testing.T) {
	b := fixtureGitHub(t, "")
	for _, path := range []string{
		"/login//oauth/access_token", "/x/../login/oauth/access_token", "//login/oauth/access_token", "/LOGIN/OAUTH/access_token",
		"/login/%6fauth/access_token", "/login/oauth/access_token?extra=1", "/login/oauth/authorize",
		"/login//device/code", "/x/../login/device/code", "//login/device/code", "/LOGIN/DEVICE/CODE",
	} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "https://github.com"+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("ambiguous token endpoint dispatched without capture")
				return nil, nil
			}))
			if !errors.Is(err, ErrDenied) {
				t.Fatalf("ambiguous endpoint accepted: %v", err)
			}
		})
	}
}

func TestGitHubCommitFailureAndRecoveryFence(t *testing.T) {
	b := fixtureGitHub(t, "")
	var calls atomic.Int32
	writes := 0
	b.github.write = func(path string, value any) error {
		writes++
		if writes == 3 {
			if err := durableJSON(path, value); err != nil {
				return err
			}
			return errors.New("directory sync uncertain")
		}
		return durableJSON(path, value)
	}
	response, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "committed-device"), githubSuccessTransport(t, b, &calls, "alice", githubRealAlice))
	if !errors.Is(err, ErrState) || response != nil {
		t.Fatalf("commit failure exposed response: %v", err)
	}
	reloaded, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	response, err = fixtureRoundTrip(reloaded, githubRequest(t, githubTokenPath, "committed-device"), fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("durably recovered token redispatched")
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var token map[string]string
	if json.Unmarshal([]byte(githubSafeBody(t, response)), &token) != nil || token["access_token"] != reloaded.github.registry.Handles["alice"] {
		t.Fatal("recovered grant did not preserve handle")
	}
}

func TestGitHubDeviceReplayCannotCrossLogicalSession(t *testing.T) {
	b := fixtureGitHub(t, "")
	var calls atomic.Int32
	response, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "old-device"), githubSuccessTransport(t, b, &calls, "alice", githubRealAlice))
	if err != nil {
		t.Fatal(err)
	}
	_ = githubSafeBody(t, response)
	next := *b.config.GitHub
	next.RuntimeDir, next.ViewDir, next.Incarnation = fixtureViewDirectory(t), fixtureViewDirectory(t), "new-logical-session"
	b.config.GitHub = &next
	fresh, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixtureRoundTrip(fresh, githubRequest(t, githubTokenPath, "old-device"), fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("consumed previous-session code was redispatched")
		return nil, nil
	}))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("prior-session device replay returned a fresh handle: %v", err)
	}
}

func TestGitHubBasicAuthDenialIsScoped(t *testing.T) {
	b := fixtureGitHub(t, githubNativeFixture())
	for _, tc := range []struct {
		name, target, user, password string
		allowed                      bool
	}{
		{"unrelated-empty-user", "https://unrelated.example/api", "", "synthetic-key", true},
		{"unrelated-user", "https://unrelated.example/api", "user", "synthetic-key", true},
		{"foreign-handle-empty-user", "https://unrelated.example/api", "", b.github.registry.Handles["alice"], false},
		{"foreign-handle-user", "https://unrelated.example/api", "alice", b.github.registry.Handles["alice"], false},
		{"foreign-claude-handle", "https://unrelated.example/api", "", b.registry.Access, false},
		{"github-raw-key", "https://api.github.com/user", "", "synthetic-key", false},
		{"github-handle-empty-user", "https://api.github.com/user", "", b.github.registry.Handles["alice"], false},
		{"git-handle-empty-user", "https://github.com/alice/repo.git/info/refs?service=git-upload-pack", "", b.github.registry.Handles["alice"], false},
		{"git-handle", "https://github.com/alice/repo.git/info/refs?service=git-upload-pack", "alice", b.github.registry.Handles["alice"], true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.SetBasicAuth(tc.user, tc.password)
			calls := 0
			response, err := fixtureRoundTrip(b, req, fixtureTransport(func(outgoing *http.Request) (*http.Response, error) {
				calls++
				if outgoing.URL.Hostname() == "unrelated.example" && outgoing.Header.Get("Authorization") != req.Header.Get("Authorization") {
					t.Error("unrelated credentials changed")
				}
				return rawFixtureResponse(nil), nil
			}))
			if response != nil {
				_ = response.Body.Close()
			}
			if tc.allowed {
				if err != nil || calls != 1 {
					t.Fatalf("unrelated or supported auth denied: %v, calls=%d", err, calls)
				}
			} else if !errors.Is(err, ErrDenied) || calls != 0 {
				t.Fatalf("protected credential routing escaped: %v, calls=%d", err, calls)
			}
		})
	}
}

func TestGitHubPendingCommitFailureReturnsNoResponse(t *testing.T) {
	b := fixtureGitHub(t, githubNativeFixture())
	b.github.write = func(path string, value any) error {
		if g, ok := value.(*githubGrant); ok && len(g.Devices) == 0 {
			return ErrState
		}
		return durableJSON(path, value)
	}
	calls := 0
	transport := fixtureTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return fixtureResponse(map[string]string{"error": "authorization_pending"}), nil
	})
	response, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "pending"), transport)
	if response != nil || !errors.Is(err, ErrState) {
		t.Fatalf("failed persistence returned a response: %v, %v", response, err)
	}
	reloaded, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	response, err = fixtureRoundTrip(reloaded, githubRequest(t, githubTokenPath, "pending"), transport)
	if response != nil || !errors.Is(err, ErrUncertain) || calls != 1 {
		t.Fatalf("failed persistence lost its replay fence: %v, calls=%d", err, calls)
	}
}

// A workload that drops its poll after dispatch must not leave a pending
// record behind; otherwise random codes could fill the bounded history.
func TestGitHubPollOutlivesClientDisconnect(t *testing.T) {
	b := fixtureGitHub(t, githubNativeFixture())
	for i := range githubMaxDevices + 1 {
		ctx, cancel := context.WithCancel(context.Background())
		req := githubRequest(t, githubTokenPath, fmt.Sprintf("random-%d", i)).WithContext(ctx)
		_, _ = fixtureRoundTrip(b, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
			cancel()
			// Like a real transport, a cancelled request fails.
			if err := out.Context().Err(); err != nil {
				return nil, err
			}
			return fixtureResponse(map[string]string{"error": "bad_verification_code"}), nil
		}))
		cancel()
	}
	g, err := b.github.readGrant()
	if err != nil || len(g.Devices) != 0 {
		t.Fatalf("pending records after dropped polls: %d, %v", len(g.Devices), err)
	}
}

// Logging in to another account from one session must not switch every
// later session in every project to it.
func TestGitHubLoginKeepsTheActiveAccount(t *testing.T) {
	b := fixtureGitHub(t, "")
	var calls atomic.Int32
	for _, login := range []struct{ user, token, code string }{
		{"alice", githubRealAlice, "first"},
		{"bob", githubRealBob, "second"},
	} {
		response, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, login.code), githubSuccessTransport(t, b, &calls, login.user, login.token))
		if err != nil {
			t.Fatalf("%s login: %v", login.user, err)
		}
		_ = response.Body.Close()
	}
	g, err := b.github.readGrant()
	if err != nil || g.Active != "alice" || g.Accounts["bob"].Token != githubRealBob {
		t.Fatalf("active = %q, accounts %v, %v", g.Active, len(g.Accounts), err)
	}
}

func githubDeviceKey(code string) string {
	digest := sha256.Sum256([]byte(code))
	key := sha256.Sum256([]byte(hex.EncodeToString(digest[:20])))
	return hex.EncodeToString(key[:])
}

func seedGitHubDevices(t *testing.T, b *Broker, devices map[string]githubDevice) {
	t.Helper()
	if err := b.github.locked(func() error {
		g, err := b.github.readGrant()
		if err != nil {
			return err
		}
		g.Devices = devices
		return b.github.saveGrant(g)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubDeviceRecordsRetireAfterCodesExpire(t *testing.T) {
	for _, tc := range []struct {
		name    string
		created func(now time.Time) int64
	}{
		{"recent", func(now time.Time) int64 { return now.Add(-time.Minute).Unix() }},
		{"unstamped", func(time.Time) int64 { return 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := fixtureGitHub(t, "")
			now := time.Unix(1_800_000_000, 0)
			b.github.now = func() time.Time { return now }
			devices := map[string]githubDevice{}
			for i := range githubMaxDevices {
				devices[githubDeviceKey(fmt.Sprintf("old-%d", i))] = githubDevice{Session: "other", Incarnation: "other", Created: tc.created(now)}
			}
			seedGitHubDevices(t, b, devices)
			var calls atomic.Int32
			transport := githubSuccessTransport(t, b, &calls, "alice", githubRealAlice)
			if _, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "new-device"), transport); !errors.Is(err, ErrState) || calls.Load() != 0 {
				t.Fatalf("login within retention exceeded the bound: %v, calls=%d", err, calls.Load())
			}
			g, err := b.github.readGrant()
			if err != nil {
				t.Fatal(err)
			}
			for _, device := range g.Devices {
				if device.Created == 0 {
					t.Fatal("unstamped record did not start its retention clock")
				}
			}

			now = now.Add(githubDeviceRetention + time.Second)
			response, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "new-device"), transport)
			if err != nil {
				t.Fatal(err)
			}
			_ = githubSafeBody(t, response)
			g, err = b.github.readGrant()
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Devices) != 1 || g.Devices[githubDeviceKey("new-device")].User != "alice" {
				t.Fatalf("expired records were not retired: %d remain", len(g.Devices))
			}
		})
	}
}

func TestGitHubRetiredDeviceRecordsRedispatchExpiredCodes(t *testing.T) {
	b := fixtureGitHub(t, "")
	now := time.Unix(1_800_000_000, 0)
	b.github.now = func() time.Time { return now }
	seedGitHubDevices(t, b, map[string]githubDevice{
		githubDeviceKey("uncertain"): {Session: b.github.config.Session, Incarnation: b.github.config.Incarnation, Created: now.Unix()},
	})
	var calls atomic.Int32
	expired := fixtureTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		response := fixtureResponse(map[string]string{"error": "expired_token"})
		response.StatusCode = http.StatusBadRequest
		return response, nil
	})
	if _, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "uncertain"), expired); !errors.Is(err, ErrUncertain) || calls.Load() != 0 {
		t.Fatalf("uncertain code redispatched before expiry: %v, calls=%d", err, calls.Load())
	}
	now = now.Add(githubDeviceRetention + time.Second)
	response, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, "uncertain"), expired)
	if err != nil || calls.Load() != 1 {
		t.Fatalf("expired code was not redispatched: %v, calls=%d", err, calls.Load())
	}
	if body := githubSafeBody(t, response); !strings.Contains(body, "expired_token") {
		t.Fatalf("provider expiry not projected: %s", body)
	}
}
