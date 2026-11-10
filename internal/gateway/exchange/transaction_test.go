// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/placeholderstore"
	"enclave/internal/util"
)

const oldAccess = "synthetic-old-native-access"
const oldRefresh = "synthetic-old-native-refresh"
const newAccess = "synthetic-rotated-access"
const newRefresh = "synthetic-rotated-refresh"

func testConfig() Config {
	return Config{Providers: []model.ProviderConfig{{Name: "anthropic", AuthSecrets: []model.AuthFileSecret{{File: ".credentials.json", Codec: "claude-credentials", Fields: []model.AuthSecretField{
		{Pointer: "/claudeAiOauth/accessToken", Kind: "access", Release: "oauth"},
		{Pointer: "/claudeAiOauth/refreshToken", Kind: "refresh"},
	}}}, TokenEndpoint: &model.TokenEndpoint{Host: "platform.claude.com", Path: "/v1/oauth/token", Body: "json", Grants: []string{"authorization_code", "refresh_token"}, Fields: map[string]string{"access": "access_token", "refresh": "refresh_token", "expiresIn": "expires_in", "scope": "scope"}, Deny: []model.AuthEndpoint{{Host: "api.anthropic.com", Path: "/api/oauth/claude_cli/create_api_key"}}}}}, Secrets: map[string]model.SecretConfig{"oauth": {Release: &model.SecretReleaseConfig{HTTP: &model.HTTPSecretReleaseConfig{Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer %s"}}}}}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T, existing bool) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(testConfig(), dir, filepath.Join(t.TempDir(), "canonical.lock"), "test-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }
	if !existing {
		return s, ""
	}
	data := `{"claudeAiOauth":{"accessToken":"` + oldAccess + `","refreshToken":"` + oldRefresh + `","futureNative":{"keep":true}}}`
	if err := placeholderstore.WriteNative(dir, s.file.File, []byte(data)); err != nil {
		t.Fatal(err)
	}
	view, err := placeholderstore.Project(dir, t.TempDir(), "initial", model.Profile{Providers: []model.ProviderConfig{s.provider}, Secrets: s.config.Secrets}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range view.Bindings.Bindings {
		if b.Kind == "refresh" {
			return s, b.Placeholder
		}
	}
	t.Fatal("no refresh placeholder")
	return nil, ""
}

func invoke(t *testing.T, s *Store, grant, refresh string, form ...bool) (*http.Response, map[string]any) {
	t.Helper()
	values := map[string]string{"grant_type": grant}
	if grant == "refresh_token" {
		values["refresh_token"] = refresh
	} else {
		values["code"] = "synthetic-one-use-code"
	}
	body, media := string(mustJSON(values)), "application/json"
	if len(form) > 0 && form[0] {
		fields := url.Values{}
		for key, value := range values {
			fields.Set(key, value)
		}
		body, media = fields.Encode(), "application/x-www-form-urlencoded"
	}
	endpoint := s.provider.TokenEndpoint
	req := httptest.NewRequest(http.MethodPost, "https://"+endpoint.Host+endpoint.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", media)
	resp, handled := s.Intercept(req, "https")
	if !handled {
		t.Fatal("endpoint bypassed exchange")
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	var object map[string]any
	if json.Unmarshal(data, &object) != nil {
		t.Fatal("invalid projected response")
	}
	for _, raw := range []string{oldAccess, oldRefresh, newAccess, newRefresh} {
		if strings.Contains(string(data), raw) {
			t.Fatal("native credential leaked in response")
		}
	}
	return resp, object
}

func tokenResponse(ttl string) *http.Response {
	data := `{"access_token":"` + newAccess + `","refresh_token":"` + newRefresh + `","token_type":"Bearer","scope":"user:inference","extra":{"credential":"` + newRefresh + `"}` + ttl + `}`
	resp := response(http.StatusOK, http.Header{"Cache-Control": []string{"no-store"}, "Set-Cookie": []string{"secret=" + newAccess}, "Location": []string{"https://invalid/" + newRefresh}}, []byte(data))
	resp.Trailer = http.Header{"X-Secret": []string{newAccess}}
	return resp
}

func TestFirstLoginCreatesNativeGrantAndStableBindings(t *testing.T) {
	s, _ := fixture(t, false)
	s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return tokenResponse(`,"expires_in":600`), nil })
	resp, object := invoke(t, s, "authorization_code", "")
	if resp.StatusCode != 200 || object["extra"] != nil || resp.Header.Get("Set-Cookie") != "" || len(resp.Trailer) != 0 {
		t.Fatal("first login response not sanitized")
	}
	for _, field := range []string{"access_token", "refresh_token"} {
		value, ok := object[field].(string)
		if !ok || !strings.Contains(value, model.SecretPlaceholderMarker) {
			t.Fatal("first login did not return bound placeholders")
		}
	}
	native, err := s.native()
	if err != nil || native.Values["/claudeAiOauth/refreshToken"] != newRefresh {
		t.Fatal("first consumed grant not committed")
	}
	bindings, err := placeholderstore.LoadBindings(s.dir)
	if err != nil || len(bindings.Bindings) != 2 {
		t.Fatal("first login bindings not committed after native")
	}
}

func TestSiblingRefreshUsesCurrentNativeGrant(t *testing.T) {
	first, refresh := fixture(t, true)
	sibling, err := New(first.config, first.dir, first.lockPath, "sibling", nil)
	if err != nil {
		t.Fatal(err)
	}
	sibling.now = first.now
	calls := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.GetBody != nil || req.Header.Get("Accept-Encoding") != "identity" || req.URL.String() != "https://platform.claude.com/v1/oauth/token" {
			t.Fatal("replayable or wrong dispatch")
		}
		return tokenResponse(`,"expires_in":600`), nil
	})
	first.transport = transport
	sibling.transport = transport
	resp, object := invoke(t, first, "refresh_token", refresh)
	if resp.StatusCode != 200 || object["expires_in"] != float64(600) || object["extra"] != nil || resp.Header.Get("Set-Cookie") != "" || len(resp.Trailer) != 0 {
		t.Fatal("token response not rebuilt from allowlist")
	}
	later := first.now().Add(40 * time.Second)
	sibling.now = func() time.Time { return later }
	resp, object = invoke(t, sibling, "refresh_token", refresh)
	if resp.StatusCode != 200 || calls != 2 || object["expires_in"] != float64(600) {
		t.Fatal("sibling did not refresh the current native grant")
	}
}

func TestConcurrentGatewaysRotateTheCurrentGrant(t *testing.T) {
	first, refresh := fixture(t, true)
	second, err := New(first.config, first.dir, first.lockPath, "second-gateway", nil)
	if err != nil {
		t.Fatal(err)
	}
	second.now = first.now
	var calls atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		expected := oldRefresh
		if call == 2 {
			expected = newRefresh
		}
		data, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(data), expected) || strings.Contains(string(data), refresh) {
			t.Error("refresh not substituted from authoritative store")
		}
		time.Sleep(35 * time.Millisecond)
		return tokenResponse(`,"expires_in":600`), nil
	})
	first.transport = transport
	second.transport = transport
	var wg sync.WaitGroup
	for _, s := range []*Store{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := invoke(t, s, "refresh_token", refresh)
			if resp.StatusCode != 200 {
				t.Error("concurrent refresh failed")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatal("parallel gateways did not each rotate the current grant")
	}
}

func TestRefreshErrorsAreProjectedWithoutPersistentRecoveryState(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"invalid-request", 400, `{"error":"invalid_request"}`, 400},
		{"invalid-grant", 400, `{"error":"invalid_grant"}`, 400},
		{"server", 503, `{"error":"temporarily_unavailable"}`, 503},
		{"pending-not-Claude", 200, `{"error":"authorization_pending"}`, 502},
		{"pending-with-token", 200, `{"error":"authorization_pending","access_token":"synthetic-unexpected"}`, 502},
		{"unknown-success", 200, `{"something":"else"}`, 502},
		{"malformed", 200, `not-json`, 502},
		{"duplicate", 200, `{"access_token":"one","access_token":"two"}`, 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, refresh := fixture(t, true)
			calls := 0
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(tc.status, http.Header{}, []byte(tc.body)), nil
			})
			if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != tc.want {
				t.Fatal("wrong sanitized provider result")
			}
			native, err := s.native()
			if err != nil || native.Values["/claudeAiOauth/refreshToken"] != oldRefresh {
				t.Fatal("failed exchange overwrote grant")
			}
			invoke(t, s, "refresh_token", refresh)
			if calls != 2 {
				t.Fatal("provider error was replaced by a persistent gateway fence")
			}
		})
	}
}

func TestInvalidExpiryCommitsWithoutExpiry(t *testing.T) {
	for _, ttl := range []string{"", `,"expires_in":null`, `,"expires_in":"600"`, `,"expires_in":0`, `,"expires_in":-1`, `,"expires_in":1.2`} {
		t.Run(ttl, func(t *testing.T) {
			s, refresh := fixture(t, true)
			calls := 0
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return tokenResponse(ttl), nil })
			resp, object := invoke(t, s, "refresh_token", refresh)
			if resp.StatusCode != 200 || object["expires_in"] != nil || object["token_type"] != "Bearer" {
				t.Fatal("invalid expiry lost tokens or leaked TTL")
			}
			native, _ := s.native()
			_, hasExpiry := native.Document["claudeAiOauth"].(map[string]any)["expiresAt"]
			if hasExpiry || native.Values["/claudeAiOauth/refreshToken"] != newRefresh {
				t.Fatal("invalid TTL cached or grant lost")
			}
			invoke(t, s, "refresh_token", refresh)
			if calls != 2 {
				t.Fatal("invalid TTL replayed")
			}
		})
	}
}

func TestContainmentEncodingAndTimeoutFailClosed(t *testing.T) {
	for _, mode := range []string{"escaped-body", "header", "encoding", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			s, refresh := fixture(t, true)
			s.timeout = 40 * time.Millisecond
			s.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch mode {
				case "escaped-body":
					return response(400, http.Header{}, []byte(`{"error":"invalid_scope","error_description":"synthetic-old-native-\u0072efresh"}`)), nil
				case "header":
					resp := tokenResponse(`,"expires_in":600`)
					resp.Header["pRaGmA"] = []string{oldRefresh}
					return resp, nil
				case "encoding":
					resp := tokenResponse(`,"expires_in":600`)
					resp.Header.Set("Content-Encoding", "gzip")
					return resp, nil
				default:
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
			})
			resp, _ := invoke(t, s, "refresh_token", refresh)
			if resp.StatusCode != 502 {
				t.Fatal("unsafe response reached client")
			}
			native, _ := s.native()
			if mode == "encoding" || mode == "timeout" {
				if native.Values["/claudeAiOauth/refreshToken"] != oldRefresh {
					t.Fatal("unparseable exchange wrote native grant")
				}

			}
		})
	}
}

func TestNoRedirectRetryOrExternalNativeOverwrite(t *testing.T) {
	s, refresh := fixture(t, true)
	calls := 0
	s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		native, _ := s.native()
		native.Document["externalWinner"] = true
		if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(native.Document)); err != nil {
			t.Fatal(err)
		}
		return tokenResponse(`,"expires_in":600`), nil
	})
	if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 502 || calls != 1 {
		t.Fatal("racing native writer overwritten")
	}
	native, _ := s.native()
	if native.Document["externalWinner"] != true || native.Values["/claudeAiOauth/accessToken"] != oldAccess {
		t.Fatal("external native write lost")
	}
	// A redirect is one ambiguous exchange, never a second request.
	s, refresh = fixture(t, true)
	calls = 0
	s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(302, http.Header{"Location": []string{"https://elsewhere.invalid"}}, []byte(`{}`)), nil
	})
	resp, _ := invoke(t, s, "refresh_token", refresh)
	if calls != 1 || resp.Header.Get("Location") != "" {
		t.Fatal("redirect followed or exposed")
	}
}

func TestRefreshLockSurvivesRealProcessKill(t *testing.T) {
	if dir := os.Getenv("ENCLAVE_EXCHANGE_CRASH_FIXTURE"); dir != "" {
		s, err := New(testConfig(), dir, filepath.Join(dir, "test.lock"), "crashing-gateway", nil)
		if err != nil {
			os.Exit(71)
		}
		bindings, err := placeholderstore.LoadBindings(dir)
		if err != nil {
			os.Exit(72)
		}
		refresh := ""
		for _, b := range bindings.Bindings {
			if b.Kind == "refresh" {
				refresh = b.Placeholder
			}
		}
		s.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if err := os.WriteFile(filepath.Join(dir, "dispatched"), nil, 0o600); err != nil {
				os.Exit(73)
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		invoke(t, s, os.Getenv("ENCLAVE_EXCHANGE_CRASH_GRANT"), refresh)
		os.Exit(74)
	}
	for _, grant := range []string{"refresh_token", "authorization_code"} {
		t.Run(grant, func(t *testing.T) {
			s, refresh := fixture(t, true)
			s.lockPath = filepath.Join(s.dir, "test.lock")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRefreshLockSurvivesRealProcessKill$") // #nosec G204 -- own test binary, synthetic fixture only.
			cmd.Env = append(os.Environ(), "ENCLAVE_EXCHANGE_CRASH_FIXTURE="+s.dir, "ENCLAVE_EXCHANGE_CRASH_GRANT="+grant)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			for {
				if _, err := os.Stat(filepath.Join(s.dir, "dispatched")); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("child missed dispatch barrier")
				case <-time.After(5 * time.Millisecond):
				}
			}
			if release, ok, err := util.TryAcquireFileLock(s.lockPath); err != nil || ok {
				if release != nil {
					release()
				}
				t.Fatal("child did not hold canonical lock across dispatch")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("child did not die at the dispatch barrier")
			}
			calls := 0
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if grant == "refresh_token" {
					return response(400, http.Header{}, []byte(`{"error":"invalid_grant"}`)), nil
				}
				return tokenResponse(`,"expires_in":600`), nil
			})
			resp, _ := invoke(t, s, "refresh_token", refresh)
			want := 200
			if grant == "refresh_token" {
				want = 400
			}
			if resp.StatusCode != want || calls != 1 {
				t.Fatal("killed gateway left a lock or poisoned an unchanged login grant")
			}
		})
	}
}

func TestReleaseReloadsNativeAndResetDeniesBindings(t *testing.T) {
	s, _ := fixture(t, true)
	bindings, _ := placeholderstore.LoadBindings(s.dir)
	var access, refresh string
	for _, b := range bindings.Bindings {
		if b.Kind == "access" {
			access = b.Placeholder
		} else {
			refresh = b.Placeholder
		}
	}
	headers := http.Header{"Authorization": []string{"Bearer " + access}}
	rules, err := s.Releases(&http.Request{Header: headers, URL: &url.URL{Scheme: "https", Host: "api.anthropic.com"}})
	if err != nil || len(rules) != 1 || rules[0].Value != oldAccess {
		t.Fatal("native access not released")
	}
	native, _ := s.native()
	native.Document["claudeAiOauth"].(map[string]any)["accessToken"] = newAccess
	if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(native.Document)); err != nil {
		t.Fatal(err)
	}
	rules, err = s.Releases(&http.Request{Header: headers, URL: &url.URL{Scheme: "https", Host: "api.anthropic.com"}})
	if err != nil || len(rules) != 1 || rules[0].Value != newAccess {
		t.Fatal("stale access snapshot")
	}
	for _, rule := range rules {
		if rule.Placeholder == refresh {
			t.Fatal("refresh token released to API")
		}
	}
	if err := os.Remove(filepath.Join(s.dir, placeholderstore.BindingsFile)); err != nil {
		t.Fatal(err)
	}
	rules, err = s.Releases(&http.Request{Header: headers, URL: &url.URL{Scheme: "https", Host: "api.anthropic.com"}})
	if err != nil || len(rules) != 0 {
		t.Fatal("reset placeholders still bound")
	}
}

func TestCanonicalLockDeadlinePreventsDispatch(t *testing.T) {
	s, refresh := fixture(t, true)
	s.timeout = 30 * time.Millisecond
	release, err := util.AcquireFileLock(s.lockPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("dispatched without canonical lock")
		return nil, errors.New("unexpected")
	})
	if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 503 {
		t.Fatal("unbounded lock wait")
	}
}

func TestNativeCommitPrecedesBindingCommitFailure(t *testing.T) {
	s, refresh := fixture(t, true)
	bindingPath := filepath.Join(s.dir, placeholderstore.BindingsFile)
	savedBindings := filepath.Join(s.dir, "saved-bindings")
	s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		if err := os.Rename(bindingPath, savedBindings); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(bindingPath, 0o700); err != nil {
			t.Fatal(err)
		}
		return tokenResponse(`,"expires_in":600`), nil
	})
	if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 502 {
		t.Fatal("binding failure reported success")
	}
	native, err := s.native()
	if err != nil || native.Values["/claudeAiOauth/refreshToken"] != newRefresh {
		t.Fatal("consumed grant lost before binding commit")
	}
	if err := os.Remove(bindingPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(savedBindings, bindingPath); err != nil {
		t.Fatal(err)
	}
	s.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(data), newRefresh) || strings.Contains(string(data), oldRefresh) {
			t.Fatal("retry did not use the durably committed native grant")
		}
		return tokenResponse(`,"expires_in":600`), nil
	})
	if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 200 {
		t.Fatal("native-first interruption lost recoverable native grant")
	}
}

func TestReflectedMetadataNeverBecomesReplayCredentialStorage(t *testing.T) {
	for _, field := range []string{"scope", "token_type"} {
		t.Run(field, func(t *testing.T) {
			s, refresh := fixture(t, true)
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				object := map[string]any{"access_token": newAccess, "refresh_token": newRefresh, "expires_in": 600, field: newRefresh}
				return response(200, http.Header{}, mustJSON(object)), nil
			})
			if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 502 {
				t.Fatal("reflected metadata response not refused")
			}
			data, err := os.ReadFile(filepath.Join(s.dir, placeholderstore.BindingsFile))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), newRefresh) || strings.Contains(string(data), newAccess) {
				t.Fatal("native grant was cached as replay metadata")
			}
			native, err := s.native()
			if err != nil || native.Values["/claudeAiOauth/refreshToken"] != newRefresh {
				t.Fatal("consumed refresh grant lost")
			}
		})
	}
}

func TestConcurrentRequestsSerializeNativeRotations(t *testing.T) {
	s, refresh := fixture(t, true)
	s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return tokenResponse(`,"expires_in":600`), nil })
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 200 {
				t.Error("concurrent gateway request failed")
			}
		}()
	}
	wg.Wait()
}

func syntheticJWT(exp int64, signature string, accounts ...string) string {
	account := "synthetic-account"
	if len(accounts) > 0 {
		account = accounts[0]
	}
	payload := mustJSON(map[string]any{"exp": exp, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account}})
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString([]byte(signature))
}

func nativeFixture(t *testing.T, tool string) (*Store, model.Profile) {
	t.Helper()
	profile, err := config.LoadProfile(model.Paths{ToolsDir: filepath.Join("..", "..", "..", "extensions", "tools")}, tool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(Config{Providers: profile.Providers, Secrets: profile.Secrets}, t.TempDir(), filepath.Join(t.TempDir(), "canonical.lock"), "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }
	return store, profile
}

func codexFixture(t *testing.T) (*Store, model.Profile) { return nativeFixture(t, "codex") }

func TestCodexLoginRefreshAndCachedJWTRelease(t *testing.T) {
	s, profile := codexFixture(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	var calls int
	var currentAccess, currentID string
	s.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.GetBody != nil {
			t.Fatal("exchange body can be replayed")
		}
		if calls == 1 && req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatal("form login was not preserved")
		}
		if calls > 1 {
			var fields map[string]string
			data, _ := io.ReadAll(req.Body)
			if json.Unmarshal(data, &fields) != nil || fields["refresh_token"] != newRefresh {
				t.Fatal("refresh did not use native grant")
			}
		}
		currentAccess = syntheticJWT(now.Unix()+600+int64(calls), "access-signature")
		currentID = syntheticJWT(now.Unix()+1200+int64(calls), "id-signature")
		tokens := map[string]any{"access_token": currentAccess, "refresh_token": newRefresh, "id_token": currentID, "token_type": "Bearer", "scope": "openid profile"}
		return response(200, http.Header{}, mustJSON(tokens)), nil
	})
	resp, first := invoke(t, s, "authorization_code", "", true)
	if resp.StatusCode != 200 || first["expires_in"] != float64(601) {
		t.Fatalf("form login failed: %v", first)
	}
	cached := first["access_token"].(string)
	for _, kind := range []string{"access_token", "id_token"} {
		claims, err := placeholderstore.JWTClaims(first[kind].(string))
		if err != nil || claims["https://api.openai.com/auth"] == nil || !strings.Contains(first[kind].(string), model.SecretPlaceholderMarker) {
			t.Fatal("JWT claims or signature missing")
		}
	}
	// A legacy native file may carry a configured API key alongside OAuth.
	api := model.SecretReleaseEntry{SecretID: "openai-api-key", Value: "synthetic-preserved-api-key", Placeholder: "ENCLAVE_SECRET_0123456789abcdef0123456789abcdef0123456789abcdef"}
	existing, err := s.native()
	if err != nil {
		t.Fatal(err)
	}
	existing.Document["OPENAI_API_KEY"] = api.Value
	delete(existing.Document, "auth_mode")
	if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(existing.Document)); err != nil {
		t.Fatal(err)
	}
	if _, err := placeholderstore.Project(s.dir, t.TempDir(), "api-alias", profile, []model.SecretReleaseEntry{api}); err != nil {
		t.Fatal("configured mixed identity not admitted", err)
	}
	resp, second := invoke(t, s, "refresh_token", first["refresh_token"].(string))
	if resp.StatusCode != 200 || calls != 2 || second["access_token"] == cached {
		t.Fatal("refresh did not update readable claims")
	}
	native, err := s.native()
	if err != nil || native.Values["/tokens/access_token"] != currentAccess || native.Values["/tokens/id_token"] != currentID || native.Document["last_refresh"] != now.Format(time.RFC3339) || native.Document["tokens"].(map[string]any)["account_id"] != "synthetic-account" {
		t.Fatal("native Codex layout not committed")
	}
	if native.Document["OPENAI_API_KEY"] != api.Value || native.Document["auth_mode"] != nil {
		t.Fatal("refresh changed canonical API key or legacy auth mode")
	}
	for _, token := range []string{cached, second["access_token"].(string)} {
		rules, err := s.Releases(&http.Request{Header: http.Header{"Authorization": []string{"Bearer " + token}}, URL: &url.URL{Scheme: "https", Host: "chatgpt.com"}})
		if err != nil || len(rules) != 1 || rules[0].Placeholder != token || rules[0].Value != currentAccess {
			t.Fatal("cached JWT signature did not release current access token")
		}
	}
	if rules, err := s.Releases(&http.Request{Header: http.Header{"Authorization": []string{"Bearer " + second["id_token"].(string)}}, URL: &url.URL{Scheme: "https", Host: "chatgpt.com"}}); err != nil || len(rules) != 0 {
		t.Fatal("id token became an API release")
	}
	req := httptest.NewRequest("POST", "https://auth.openai.com/oauth/token", strings.NewReader(`{"grant_type":"urn:ietf:params:oauth:grant-type:token-exchange","subject_token":"`+second["id_token"].(string)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if denied, handled := s.Intercept(req, "https"); !handled || denied.StatusCode != 403 || calls != 2 {
		t.Fatal("API-key token exchange escaped mediation")
	}
	view, err := placeholderstore.Project(s.dir, t.TempDir(), "fresh", profile, []model.SecretReleaseEntry{api})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(view.Dir, "auth.json"))
	if err != nil || !strings.Contains(string(data), second["access_token"].(string)) || strings.Contains(string(data), currentAccess) || strings.Contains(string(data), currentID) {
		t.Fatal("reprojection lost refreshed claims or leaked native tokens")
	}
	for _, replacement := range []string{cached, currentAccess, "e30.e30.ENCLAVE_SECRET_foreign"} {
		configDir := t.TempDir()
		candidate := strings.ReplaceAll(string(data), second["access_token"].(string), replacement)
		if err := os.WriteFile(filepath.Join(configDir, "auth.json"), []byte(candidate), 0o600); err != nil {
			t.Fatal(err)
		}
		ops, err := placeholderstore.ClassifyNative(configDir, "/auth", profile, view.Bindings, nil, false)
		if replacement == cached {
			if err != nil || len(ops) != 1 || ops[0].Verb != "keep" {
				t.Fatal("changed claims lost bound sibling ownership", err)
			}
		} else if err == nil {
			t.Fatal("raw or foreign JWT was kept")
		}
	}
	for _, errorReply := range []bool{false, true} {
		s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if errorReply {
				return response(400, http.Header{}, mustJSON(map[string]string{"error": "invalid_grant", "error_description": currentID})), nil
			}
			return response(200, http.Header{}, mustJSON(map[string]string{"access_token": currentAccess, "refresh_token": newRefresh, "id_token": currentID, "scope": currentID})), nil
		})
		if denied, _ := invoke(t, s, "refresh_token", first["refresh_token"].(string)); denied.StatusCode != 502 {
			t.Fatal("reflected ID token escaped containment")
		}
	}
	beforeReset := calls
	if err := os.Remove(filepath.Join(s.dir, placeholderstore.BindingsFile)); err != nil {
		t.Fatal(err)
	}
	if denied, _ := invoke(t, s, "refresh_token", first["refresh_token"].(string)); denied.StatusCode != 403 || calls != beforeReset {
		t.Fatal("reset binding still dispatched")
	}
}

func TestFormExchangeRejectsAmbiguousFields(t *testing.T) {
	s, _ := fixture(t, false)
	s.provider.TokenEndpoint.Body = "either"
	for _, body := range []string{"grant_type=authorization_code&code=one&code=two", "grant_type=authorization_code&code=%zz", "grant_type=authorization_code&code=ENCLAVE_SECRET_wrong"} {
		req := httptest.NewRequest("POST", "https://platform.claude.com/v1/oauth/token", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		resp, handled := s.Intercept(req, "https")
		if !handled || resp.StatusCode != 403 {
			t.Fatal("ambiguous form forwarded")
		}
	}
}

func TestCodexHarvestPreservesGatewayOwnedAuthState(t *testing.T) {
	for _, apiMode := range []bool{false, true} {
		t.Run(fmt.Sprint(apiMode), func(t *testing.T) {
			s, profile := codexFixture(t)
			old := map[string]any{"auth_mode": "chatgpt", "last_refresh": "2026-01-01T00:00:00Z", "tokens": map[string]any{"access_token": syntheticJWT(4102444800, "old-access", "old-account"), "id_token": syntheticJWT(4102444800, "old-id", "old-account"), "refresh_token": oldRefresh, "account_id": "old-account"}}
			var configured []model.SecretReleaseEntry
			if apiMode {
				old = map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "synthetic-native-key", "last_refresh": "2026-01-01T00:00:00Z"}
				placeholder, err := model.NewSecretPlaceholder(nil)
				if err != nil {
					t.Fatal(err)
				}
				configured = []model.SecretReleaseEntry{{SecretID: "openai-api-key", Value: "synthetic-native-key", Placeholder: placeholder}}
			}
			if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(old)); err != nil {
				t.Fatal(err)
			}
			view, err := placeholderstore.Project(s.dir, t.TempDir(), "older", profile, configured)
			if err != nil {
				t.Fatal(err)
			}
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(200, http.Header{}, mustJSON(map[string]string{"access_token": syntheticJWT(4102444800, "new-access"), "id_token": syntheticJWT(4102444800, "new-id"), "refresh_token": newRefresh})), nil
			})
			if resp, _ := invoke(t, s, "authorization_code", "", true); resp.StatusCode != 200 {
				t.Fatal("login control failed")
			}
			before, err := s.native()
			if err != nil {
				t.Fatal(err)
			}
			release, err := s.lock(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = placeholderstore.Harvest(s.dir, view.Dir, t.TempDir(), profile)
			release()
			if err != nil {
				t.Fatal(err)
			}
			after, err := s.native()
			if err != nil || after.Fingerprint != before.Fingerprint {
				t.Fatal("stale view changed canonical mode, account, refresh time or grant")
			}
		})
	}
}

func TestCodexExternalAPIKeyDoesNotLoseConsumedGrant(t *testing.T) {
	for _, grant := range []string{"authorization_code", "refresh_token"} {
		t.Run(grant, func(t *testing.T) {
			s, profile := codexFixture(t)
			if grant == "refresh_token" {
				doc := map[string]any{"tokens": map[string]string{"access_token": syntheticJWT(4102444800, "old-access"), "id_token": syntheticJWT(4102444800, "old-id"), "refresh_token": oldRefresh}}
				if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(doc)); err != nil {
					t.Fatal(err)
				}
			}
			view, err := placeholderstore.Project(s.dir, t.TempDir(), "initial", profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			refresh := ""
			for _, binding := range view.Bindings.Bindings {
				if binding.Kind == "refresh" {
					refresh = binding.Placeholder
				}
			}
			external, err := s.native()
			if err != nil {
				t.Fatal(err)
			}
			external.Document["OPENAI_API_KEY"] = "synthetic-external-api"
			if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(external.Document)); err != nil {
				t.Fatal(err)
			}
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(200, http.Header{}, mustJSON(map[string]string{"access_token": syntheticJWT(4102444800, "new-access"), "id_token": syntheticJWT(4102444800, "new-id"), "refresh_token": newRefresh})), nil
			})
			resp, _ := invoke(t, s, grant, refresh, true)
			native, err := s.native()
			if err != nil || resp.StatusCode != 200 || native.Values["/tokens/refresh_token"] != newRefresh {
				t.Fatal("unbound external API key lost a consumed grant")
			}
			if grant == "refresh_token" && native.Document["OPENAI_API_KEY"] != "synthetic-external-api" {
				t.Fatal("refresh erased external API key")
			}
		})
	}
}

func TestCodexReplyIgnoresForeignCodecMetadata(t *testing.T) {
	for _, numeric := range []bool{false, true} {
		t.Run(fmt.Sprint(numeric), func(t *testing.T) {
			s, profile := codexFixture(t)
			scopes := []any{"synthetic-foreign-native-credential"}
			if numeric {
				scopes = []any{123}
			}
			doc := map[string]any{"claudeAiOauth": map[string]any{"scopes": scopes}}
			if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(doc)); err != nil {
				t.Fatal(err)
			}
			view, err := placeholderstore.Project(s.dir, t.TempDir(), "initial", profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(view.Dir, s.file.File))
			if err != nil || strings.Contains(string(data), "claudeAiOauth") {
				t.Fatal("foreign metadata reached projection")
			}
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(200, http.Header{}, mustJSON(map[string]string{"access_token": syntheticJWT(4102444800, "new-access"), "id_token": syntheticJWT(4102444800, "new-id"), "refresh_token": newRefresh})), nil
			})
			resp, reply := invoke(t, s, "authorization_code", "", true)
			if resp.StatusCode != 200 || reply["scope"] != nil {
				t.Fatal("foreign codec metadata entered token reply")
			}
			native, err := s.native()
			if err != nil || native.Document["claudeAiOauth"] == nil {
				t.Fatal("unknown canonical metadata was deleted")
			}
		})
	}
}

func TestProviderMapClientsShareProjectionAndExchange(t *testing.T) {
	for _, tool := range []string{"pi", "opencode"} {
		t.Run(tool, func(t *testing.T) {
			s, profile := nativeFixture(t, tool)
			empty, err := placeholderstore.Project(s.dir, t.TempDir(), "empty", profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(filepath.Dir(filepath.Join(empty.Dir, s.file.File))); err != nil || !info.IsDir() {
				t.Fatal("fresh nested view parent missing")
			}
			apiType := "api"
			if tool == "pi" {
				apiType = "api_key"
			}
			placeholder, err := model.NewSecretPlaceholder(nil)
			if err != nil {
				t.Fatal(err)
			}
			api := model.SecretReleaseEntry{SecretID: "anthropic-api-key", Value: "synthetic-native-api", Placeholder: placeholder}
			source := map[string]any{"anthropic": map[string]any{"type": apiType, "key": api.Value, "unknown": "strip-me"}}
			if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(source)); err != nil {
				t.Fatal(err)
			}
			calls := 0
			current := ""
			s.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					data, _ := io.ReadAll(req.Body)
					fields, err := url.ParseQuery(string(data))
					if err != nil || fields.Get("refresh_token") != newRefresh {
						t.Fatal("map refresh did not use native grant")
					}
				}
				current = syntheticJWT(s.now().Unix()+600+int64(calls), "signature")
				return response(200, http.Header{}, mustJSON(map[string]any{"access_token": current, "refresh_token": newRefresh, "id_token": syntheticJWT(4102444800, "id-signature"), "expires_in": 600})), nil
			})
			resp, login := invoke(t, s, "authorization_code", "", true)
			if resp.StatusCode != 200 || login["expires_in"] != float64(600) || login["id_token"] != nil {
				t.Fatal("map login response does not fit native format")
			}
			native, err := s.native()
			if err != nil {
				t.Fatal(err)
			}
			entry := native.Document[s.provider.Name].(map[string]any)
			if entry["type"] != "oauth" || entry["accountId"] != "synthetic-account" || entry["expires"] != json.Number(fmt.Sprint(s.now().UnixMilli()+600000)) || native.Document["anthropic"].(map[string]any)["key"] != api.Value {
				t.Fatal("map commit metadata or foreign API key changed")
			}
			view, err := placeholderstore.Project(s.dir, t.TempDir(), "live", profile, []model.SecretReleaseEntry{api})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(view.Dir, s.file.File))
			if err != nil || strings.Contains(string(data), api.Value) || strings.Contains(string(data), current) || strings.Contains(string(data), "strip-me") {
				t.Fatal("unsafe map projection")
			}
			configDir := t.TempDir()
			target := filepath.Join(configDir, s.file.File)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if ops, err := placeholderstore.ClassifyNative(configDir, "/auth", profile, view.Bindings, nil, false); err != nil || len(ops) != 1 || ops[0].Verb != "keep" {
				t.Fatal("map binding namespace failed sibling ownership", err)
			}
			if next, _ := invoke(t, s, "refresh_token", login["refresh_token"].(string), true); next.StatusCode != 200 || calls != 2 {
				t.Fatal("map refresh failed")
			}
			rules, err := s.Releases(&http.Request{Header: http.Header{"Authorization": []string{"Bearer " + login["access_token"].(string)}}, URL: &url.URL{Scheme: "https", Host: "chatgpt.com"}})
			if err != nil || len(rules) != 1 || rules[0].Value != current || len(rules[0].Hosts) != 1 || rules[0].Hosts[0] != "chatgpt.com" {
				t.Fatal("cached map JWT did not release current grant at its declared host")
			}
			before, err := s.native()
			if err != nil {
				t.Fatal(err)
			}
			release, err := s.lock(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = placeholderstore.Harvest(s.dir, view.Dir, configDir, profile)
			release()
			if err != nil {
				t.Fatal(err)
			}
			after, err := s.native()
			if err != nil || after.Fingerprint != before.Fingerprint {
				t.Fatal("stale map view changed canonical grant metadata")
			}
			reflectedID := syntheticJWT(4102444800, "unpersisted-id")
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(200, http.Header{}, mustJSON(map[string]any{"access_token": current, "refresh_token": newRefresh, "id_token": reflectedID, "scope": reflectedID, "expires_in": 600})), nil
			})
			if denied, _ := invoke(t, s, "refresh_token", login["refresh_token"].(string), true); denied.StatusCode != 502 {
				t.Fatal("an unpersisted ID token escaped map containment")
			}
			for _, foreign := range []map[string]any{{"unknown": map[string]string{"type": apiType, "key": "synthetic-unknown-key"}}, {"anthropic": map[string]string{"type": "oauth", "access": "synthetic-foreign-access", "refresh": "synthetic-foreign-refresh"}}} {
				if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(foreign)); err != nil {
					t.Fatal(err)
				}
				if _, err := placeholderstore.Project(s.dir, t.TempDir(), "foreign", profile, []model.SecretReleaseEntry{api}); err == nil {
					t.Fatal("undeclared foreign provider credentials accepted")
				}
			}
		})
	}
}

func TestProviderMapInactiveCredentialsCannotHideInMetadata(t *testing.T) {
	for _, inactive := range []string{"key", "access", "refresh"} {
		t.Run(inactive, func(t *testing.T) {
			s, profile := nativeFixture(t, "opencode")
			const raw = "synthetic-inactive-native-credential"
			entry := map[string]any{"type": "oauth", "access": syntheticJWT(4102444800, "fixture"), "refresh": oldRefresh}
			if inactive != "key" {
				entry = map[string]any{"type": "api", "key": "synthetic-native-api"}
			}
			entry[inactive], entry["accountId"] = raw, raw
			profile.Providers[0].AuthSecrets = append(profile.Providers[0].AuthSecrets, model.AuthFileSecret{File: "config.json", Codec: "claude-config", Fields: []model.AuthSecretField{
				{Pointer: "/primaryApiKey", Kind: "apiKey", Release: "openai-api-key"},
				{Pointer: "/anthropicApiKey", Kind: "apiKey", Release: "openai-api-key"},
			}})
			if err := placeholderstore.WriteNative(s.dir, "config.json", mustJSON(map[string]any{"oauthAccount": map[string]string{"displayName": raw}})); err != nil {
				t.Fatal(err)
			}
			if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(map[string]any{"openai": entry})); err != nil {
				t.Fatal(err)
			}
			native, err := s.native()
			if err != nil || native.Values["/openai/"+inactive] != raw {
				t.Fatal("inactive field is not a declared known credential", err)
			}
			placeholder, err := model.NewSecretPlaceholder(nil)
			if err != nil {
				t.Fatal(err)
			}
			configured := []model.SecretReleaseEntry{{SecretID: "openai-api-key", Value: "synthetic-configured-api", Placeholder: placeholder}}
			view, err := placeholderstore.Project(s.dir, t.TempDir(), "inactive", profile, configured)
			if err == nil {
				for _, file := range []string{s.file.File, "config.json"} {
					data, err := os.ReadFile(filepath.Join(view.Dir, file))
					if err != nil || strings.Contains(string(data), raw) {
						t.Fatal("inactive declared credential escaped through type-filtered metadata", err)
					}
				}
			} else if strings.Contains(err.Error(), raw) {
				t.Fatal("refusal echoed the inactive credential")
			}
			if after, err := s.native(); err != nil || after.Fingerprint != native.Fingerprint {
				t.Fatal("projection changed the canonical source", err)
			}
		})
	}
}

func devicePoll(t *testing.T, router *Store, form bool) (*http.Response, map[string]any) {
	t.Helper()
	values := map[string]string{"grant_type": "urn:ietf:params:oauth:grant-type:device_code", "device_code": "synthetic-device-code", "client_id": "synthetic-client"}
	body, media := mustJSON(values), "application/json"
	if form {
		encoded := url.Values{}
		for key, value := range values {
			encoded.Set(key, value)
		}
		body, media = []byte(encoded.Encode()), "application/x-www-form-urlencoded"
	}
	req := httptest.NewRequest(http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", media)
	resp, handled := router.Intercept(req, "https")
	if !handled {
		t.Fatal("device exchange escaped routing")
	}
	defer func() { _ = resp.Body.Close() }()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return resp, result
}

func TestCopilotNativeDeviceFlow(t *testing.T) {
	for _, tool := range []string{"copilot", "opencode"} {
		t.Run(tool, func(t *testing.T) {
			router, profile := nativeFixture(t, tool)
			var s *Store
			for _, route := range router.routes {
				if route.provider.TokenEndpoint.Host == "github.com" {
					s = route
				}
			}
			if s == nil {
				t.Fatal("missing declared GitHub route")
			}
			openai := map[string]any{"type": "oauth", "access": syntheticJWT(4102444800, "retained"), "refresh": "synthetic-openai-refresh", "expires": 0}
			if tool == "opencode" {
				if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(map[string]any{"openai": openai})); err != nil {
					t.Fatal(err)
				}
			}
			if tool == "copilot" {
				if err := placeholderstore.WriteNative(s.dir, s.file.File, []byte(`{"authTokens":{"https://github.com:older":{"token":"gho_synthetic-prior"}},"loggedInUsers":[{"host":"https://github.com","login":"older","kind":"githubDotCom"}]}`)); err != nil {
					t.Fatal(err)
				}
			}
			currentGrant := "gho_synthetic-current"
			polls, users := 0, 0
			reply := map[string]any{"error": "authorization_pending"}
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "api.github.com" {
					users++
					if req.URL.Path != "/copilot_internal/user" || req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer "+currentGrant {
						t.Fatal("unexpected account lookup")
					}
					return response(200, http.Header{}, []byte(`{"login":"fixture-user","unknown":"strip"}`)), nil
				}
				if req.URL.Host != "github.com" || req.URL.Path != "/login/oauth/access_token" || req.Header.Get("Accept") != "application/json" {
					t.Fatal("unexpected external request")
				}
				polls++
				return response(200, http.Header{}, mustJSON(reply)), nil
			})
			for _, route := range router.routes {
				route.transport = transport
			}
			for _, errorCode := range []string{"authorization_pending", "slow_down"} {
				reply = map[string]any{"error": errorCode, "interval": 7, "unknown": "strip"}
				resp, result := devicePoll(t, router, false)
				if resp.StatusCode != 200 || result["error"] != errorCode || result["interval"] != float64(7) || result["unknown"] != nil || users != 0 {
					t.Fatal("device poll did not preserve the tokenless 200 error")
				}
			}
			for _, interval := range []any{"bad", -1, 4000} {
				reply = map[string]any{"error": "slow_down", "interval": interval}
				if resp, _ := devicePoll(t, router, true); resp.StatusCode != 502 {
					t.Fatal("invalid poll interval accepted")
				}
			}
			reply = map[string]any{"access_token": "gho_synthetic-current", "token_type": "bearer", "scope": "read:user", "extra": "gho_synthetic-current"}
			resp, result := devicePoll(t, router, true)
			placeholder, _ := result["access_token"].(string)
			if resp.StatusCode != 200 || !strings.HasPrefix(placeholder, "gho_") || !strings.Contains(placeholder, model.SecretPlaceholderMarker) || result["extra"] != nil || strings.Contains(string(mustJSON(result)), "gho_synthetic-current") {
				t.Fatal("device success did not substitute its grant", result)
			}
			if users != map[string]int{"copilot": 1, "opencode": 0}[tool] {
				t.Fatal("account lookup did not follow the native layout")
			}
			native, err := s.native()
			if err != nil {
				t.Fatal(err)
			}
			view, err := placeholderstore.Project(s.dir, t.TempDir(), "copilot", profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(view.Dir, s.file.File))
			if err != nil || strings.Contains(string(data), "gho_synthetic-current") {
				t.Fatal("raw grant reached native view", err)
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if tool == "copilot" {
				tokens := doc["authTokens"].(map[string]any)
				for _, suffix := range []string{"", ":github"} {
					if tokens["https://github.com:fixture-user"+suffix].(map[string]any)["token"] != placeholder {
						t.Fatal("native account alias lost the stable placeholder")
					}
				}
				if native.Document["storeTokenPlaintext"] != true || native.Document["lastLoggedInUser"].(map[string]any)["login"] != "fixture-user" {
					t.Fatal("native account metadata incomplete")
				}
			} else {
				entry := doc["github-copilot"].(map[string]any)
				if entry["access"] != placeholder || entry["refresh"] != placeholder || entry["expires"] != float64(0) || string(mustJSON(native.Document["openai"])) != string(mustJSON(openai)) {
					t.Fatal("OpenCode alias or other provider grant changed")
				}
			}
			if ops, err := placeholderstore.ClassifyNative(view.Dir, "/auth", profile, view.Bindings, nil, false); err != nil || len(ops) != 1 {
				t.Fatal("shared native file did not have exactly one operation", err)
			}
			if tool == "copilot" && len(native.Document["loggedInUsers"].([]any)) != 2 {
				t.Fatal("login discarded the older native account")
			}
			reflected := "gho_synthetic-prior"
			if tool == "opencode" {
				reflected = "synthetic-openai-refresh"
			}
			currentGrant = "gho_synthetic-rotated"
			reply = map[string]any{"access_token": currentGrant, "scope": reflected}
			if denied, _ := devicePoll(t, router, false); denied.StatusCode != 502 {
				t.Fatal("credential from another native slot escaped in scope")
			}
			current, err := s.native()
			if err != nil {
				t.Fatal(err)
			}
			committed := false
			for _, value := range current.Values {
				committed = committed || value == currentGrant
			}
			if !committed {
				t.Fatal("contained scope lost the valid consumed grant")
			}
			for _, target := range []string{"https://api.github.com/copilot_internal/user", "https://api.github.com/user", "https://api.github.com/copilot_internal/v2/token", "https://api.github.com/copilot_internal/user?extra=1", "https://api.github.com/copilot_internal/managed_settings?client_id=github%2Fcli", "https://api.github.com/copilot_internal/team_customizations", "https://api.github.com/copilot_internal/user/"} {
				req := httptest.NewRequest(http.MethodGet, target, nil)
				req.Header.Set("Authorization", "Bearer "+placeholder)
				rules, err := router.Releases(req)
				if err != nil {
					t.Fatal(err)
				}
				matched := false
				for _, rule := range rules {
					matched = matched || (rule.Placeholder == placeholder && rule.Value == currentGrant)
				}
				allowed := req.URL.Path == "/copilot_internal/user" || req.URL.Path == "/copilot_internal/managed_settings" || req.URL.Path == "/copilot_internal/team_customizations"
				if matched != allowed {
					t.Fatal("native GitHub API path guard changed", target)
				}
			}
			before := polls
			for _, target := range []string{"https://github.com/LOGIN/OAUTH/ACCESS_TOKEN", "https://github.com/login/oauth/access_token/"} {
				req := httptest.NewRequest(http.MethodPost, target, nil)
				if denied, handled := router.Intercept(req, "https"); !handled || denied.StatusCode != 403 || polls != before {
					t.Fatal("alternate OAuth path reached upstream")
				}
			}
			if err := os.Remove(filepath.Join(s.dir, placeholderstore.BindingsFile)); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/user", nil)
			req.Header.Set("Authorization", "Bearer "+placeholder)
			if rules, err := router.Releases(req); err != nil || len(rules) != 0 {
				t.Fatal("reset native alias still releases a grant", err)
			}
		})
	}
}
