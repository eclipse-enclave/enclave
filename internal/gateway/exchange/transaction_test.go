// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enclave/internal/model"
	"enclave/internal/placeholderstore"
	"enclave/internal/util"
)

const oldAccess = "synthetic-old-native-access"
const oldRefresh = "synthetic-old-native-refresh"
const newAccess = "synthetic-rotated-access"
const newRefresh = "synthetic-rotated-refresh"

func testConfig() Config {
	return Config{Provider: model.ProviderConfig{Name: "anthropic", AuthSecrets: []model.AuthFileSecret{{File: ".credentials.json", Codec: "claude-credentials", Fields: []model.AuthSecretField{
		{Pointer: "/claudeAiOauth/accessToken", Kind: "access", Release: "oauth"},
		{Pointer: "/claudeAiOauth/refreshToken", Kind: "refresh"},
	}}}, TokenEndpoint: &model.TokenEndpoint{Host: "platform.claude.com", Path: "/v1/oauth/token", Body: "either", Grants: []string{"authorization_code", "refresh_token"}, Fields: map[string]string{"access": "access_token", "refresh": "refresh_token", "expiresIn": "expires_in", "scope": "scope"}, Deny: []model.AuthEndpoint{{Host: "api.anthropic.com", Path: "/api/oauth/claude_cli/create_api_key"}}}}, Secrets: map[string]model.SecretConfig{"oauth": {Release: &model.SecretReleaseConfig{HTTP: &model.HTTPSecretReleaseConfig{Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer %s"}}}}}
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
	view, err := placeholderstore.Project(dir, t.TempDir(), "initial", model.Profile{Providers: []model.ProviderConfig{s.config.Provider}, Secrets: s.config.Secrets}, nil)
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

func invoke(t *testing.T, s *Store, grant, refresh string) (*http.Response, map[string]any) {
	t.Helper()
	values := map[string]string{"grant_type": grant}
	if grant == "refresh_token" {
		values["refresh_token"] = refresh
	} else {
		values["code"] = "synthetic-one-use-code"
	}
	req := httptest.NewRequest(http.MethodPost, "https://platform.claude.com/v1/oauth/token", strings.NewReader(string(mustJSON(values))))
	req.Header.Set("Content-Type", "application/json")
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

func TestLoginCommitReplayAndExternalWriterInvalidation(t *testing.T) {
	s, _ := fixture(t, false)
	var calls int
	s.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		bindings, err := placeholderstore.LoadBindings(s.dir)
		if err != nil || bindings.Providers["anthropic"].Inflight == nil {
			t.Fatal("dispatch preceded durable intent")
		}
		if req.GetBody != nil || req.Header.Get("Accept-Encoding") != "identity" || req.URL.String() != "https://platform.claude.com/v1/oauth/token" {
			t.Fatal("unbounded/replayable dispatch")
		}
		return tokenResponse(`,"expires_in":600`), nil
	})
	resp, object := invoke(t, s, "authorization_code", "")
	if resp.StatusCode != 200 || object["expires_in"] != float64(600) || object["extra"] != nil || resp.Header.Get("Set-Cookie") != "" || len(resp.Trailer) != 0 {
		t.Fatal("login response not rebuilt from allowlist")
	}
	refresh := object["refresh_token"].(string)
	native, err := s.native()
	if err != nil || native.Values["/claudeAiOauth/refreshToken"] != newRefresh {
		t.Fatal("rotated grant not committed")
	}
	later := s.now().Add(40 * time.Second)
	s.now = func() time.Time { return later }
	resp, replayed := invoke(t, s, "refresh_token", refresh)
	if resp.StatusCode != 200 || calls != 1 || replayed["expires_in"] != float64(560) || replayed["refresh_token"] != refresh {
		t.Fatal("same-generation replay not authoritative")
	}
	// A native writer changes only metadata. The content-keyed replay still drops.
	native.Document["externalMetadata"] = true
	if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(native.Document)); err != nil {
		t.Fatal(err)
	}
	if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 200 || calls != 2 {
		t.Fatal("external write failed to invalidate replay")
	}
	now := later.Add(601 * time.Second)
	s.now = func() time.Time { return now }
	if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 200 || calls != 3 {
		t.Fatal("expired grant replayed")
	}
}

func TestConcurrentGatewaysDispatchOnlyOnce(t *testing.T) {
	first, refresh := fixture(t, true)
	second, err := New(first.config, first.dir, first.lockPath, "second-gateway", nil)
	if err != nil {
		t.Fatal(err)
	}
	second.now = first.now
	var calls atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		data, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(data), oldRefresh) || strings.Contains(string(data), refresh) {
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
	if calls.Load() != 1 {
		t.Fatal("parallel gateways consumed the grant twice")
	}
}

func TestRefreshFailureClassificationAndLoginRecovery(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		uncertain bool
	}{
		{"nonconsuming", 400, `{"error":"invalid_request"}`, false},
		{"consumed", 400, `{"error":"invalid_grant"}`, true},
		{"server", 503, `{"error":"temporarily_unavailable"}`, true},
		{"pending", 200, `{"error":"authorization_pending"}`, false},
		{"pending-with-token", 200, `{"error":"authorization_pending","access_token":"synthetic-new-unexpected"}`, true},
		{"unknown-success", 200, `{"something":"else"}`, true},
		{"malformed", 200, `not-json`, true},
		{"duplicate", 200, `{"access_token":"one","access_token":"two"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, refresh := fixture(t, true)
			calls := 0
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(tc.status, http.Header{}, []byte(tc.body)), nil
			})
			invoke(t, s, "refresh_token", refresh)
			b, err := placeholderstore.LoadBindings(s.dir)
			if err != nil {
				t.Fatal(err)
			}
			state := b.Providers["anthropic"]
			if state.Uncertain != tc.uncertain || state.Inflight != nil {
				t.Fatal("incorrect durable failure state")
			}
			native, _ := s.native()
			if native.Values["/claudeAiOauth/refreshToken"] != oldRefresh {
				t.Fatal("failure overwrote canonical grant")
			}
			if tc.uncertain {
				resp, _ := invoke(t, s, "refresh_token", refresh)
				if resp.StatusCode != 503 || calls != 1 {
					t.Fatal("uncertain refresh dispatched again")
				}
			}
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return tokenResponse(`,"expires_in":600`), nil })
			if resp, _ := invoke(t, s, "authorization_code", ""); resp.StatusCode != 200 {
				t.Fatal("fresh login did not recover")
			}
			b, _ = placeholderstore.LoadBindings(s.dir)
			if b.Providers["anthropic"].Uncertain {
				t.Fatal("login left uncertainty")
			}
		})
	}
}

func TestInvalidExpiryCommitsWithoutReplay(t *testing.T) {
	for _, ttl := range []string{"", `,"expires_in":null`, `,"expires_in":"600"`, `,"expires_in":0`, `,"expires_in":-1`, `,"expires_in":1.2`} {
		t.Run(ttl, func(t *testing.T) {
			s, refresh := fixture(t, true)
			calls := 0
			s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return tokenResponse(ttl), nil })
			resp, object := invoke(t, s, "refresh_token", refresh)
			if resp.StatusCode != 200 || object["expires_in"] != nil || object["token_type"] != "Bearer" {
				t.Fatal("invalid expiry lost tokens or leaked TTL")
			}
			b, _ := placeholderstore.LoadBindings(s.dir)
			state := b.Providers["anthropic"]
			native, _ := s.native()
			if state.Replay != nil || state.ExpiresAt != nil || native.Values["/claudeAiOauth/refreshToken"] != newRefresh {
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
				b, _ := placeholderstore.LoadBindings(s.dir)
				if !b.Providers["anthropic"].Uncertain {
					t.Fatal("ambiguous exchange not uncertain")
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

func TestDurableIntentSurvivesProcessDeath(t *testing.T) {
	if dir := os.Getenv("ENCLAVE_EXCHANGE_CRASH_FIXTURE"); dir != "" {
		s, err := New(testConfig(), dir, filepath.Join(dir, "test.lock"), "crashing-gateway", nil)
		if err != nil {
			os.Exit(71)
		}
		s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			b, e := placeholderstore.LoadBindings(dir)
			if e != nil || b.Providers["anthropic"].Inflight == nil {
				os.Exit(72)
			}
			os.Exit(73)
			return nil, errors.New("unreachable")
		})
		invoke(t, s, "authorization_code", "")
		os.Exit(74)
	}
	s, refresh := fixture(t, true)
	cmd := exec.Command(os.Args[0], "-test.run=^TestDurableIntentSurvivesProcessDeath$") // #nosec G204 -- own test binary, fixture only.
	cmd.Env = append(os.Environ(), "ENCLAVE_EXCHANGE_CRASH_FIXTURE="+s.dir)
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("crash barrier not reached: %v", err)
	}
	// The fixture's canonical lock is shared with the killed gateway.
	s.lockPath = filepath.Join(s.dir, "test.lock")
	s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("recovered refresh dispatched")
		return nil, errors.New("unexpected")
	})
	if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 503 {
		t.Fatal("orphan intent did not fail closed")
	}
	b, _ := placeholderstore.LoadBindings(s.dir)
	if !b.Providers["anthropic"].Uncertain || b.Providers["anthropic"].Inflight != nil {
		t.Fatal("orphan recovery not durable")
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
	rules, err := s.Releases(context.Background(), headers)
	if err != nil || len(rules) != 1 || rules[0].Value != oldAccess {
		t.Fatal("native access not released")
	}
	native, _ := s.native()
	native.Document["claudeAiOauth"].(map[string]any)["accessToken"] = newAccess
	if err := placeholderstore.WriteNative(s.dir, s.file.File, mustJSON(native.Document)); err != nil {
		t.Fatal(err)
	}
	rules, err = s.Releases(context.Background(), headers)
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
	rules, err = s.Releases(context.Background(), headers)
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
	savedIntent := filepath.Join(s.dir, "saved-intent")
	s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		if err := os.Rename(bindingPath, savedIntent); err != nil {
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
	if err := os.Rename(savedIntent, bindingPath); err != nil {
		t.Fatal(err)
	}
	s.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("partially committed exchange retried")
		return nil, errors.New("unexpected")
	})
	if resp, _ := invoke(t, s, "refresh_token", refresh); resp.StatusCode != 503 {
		t.Fatal("native-first interruption did not recover uncertain")
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
