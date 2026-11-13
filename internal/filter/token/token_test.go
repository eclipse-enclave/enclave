// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package token

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enclave/internal/filter"
)

const (
	testClientSecret = "client-secret-abc"
	testRefreshToken = "refresh-token-def"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type tokenServer struct {
	*httptest.Server
	calls   atomic.Int32
	release chan struct{}
	mu      sync.Mutex
	handler func(w http.ResponseWriter, r *http.Request, n int32)
	forms   []map[string]string
}

func newTokenServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int32)) *tokenServer {
	t.Helper()
	ts := &tokenServer{handler: handler}
	ts.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := ts.calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		form := map[string]string{"method": r.Method, "content-type": r.Header.Get("Content-Type")}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		ts.mu.Lock()
		ts.forms = append(ts.forms, form)
		ts.mu.Unlock()
		if ts.release != nil {
			<-ts.release
		}
		ts.handler(w, r, n)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func okToken(expiresIn int) func(http.ResponseWriter, *http.Request, int32) {
	return func(w http.ResponseWriter, _ *http.Request, n int32) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"access-%d","expires_in":%d,"token_type":"Bearer"}`, n, expiresIn)
	}
}

func newSource(ts *tokenServer, clock *fakeClock) *OAuth2 {
	return NewOAuth2(OAuth2Config{
		TokenURL:     ts.URL + "/token",
		ClientID:     "client-id",
		ClientSecret: testClientSecret,
		RefreshToken: testRefreshToken,
		Client:       ts.Client(),
		Now:          clock.Now,
	})
}

func TestStaticSource(t *testing.T) {
	src := NewStatic("value")
	got, err := src.Token(context.Background())
	if err != nil || got != "value" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	src.Invalidate("value")
	if got, _ := src.Token(context.Background()); got != "value" {
		t.Fatalf("Invalidate changed a static token: %q", got)
	}
}

func TestNewFromCredential(t *testing.T) {
	src, err := New(filter.NewStaticCredential("gws", "v"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*Static); !ok {
		t.Fatalf("New(static) = %T", src)
	}
	src, err = New(filter.NewOAuth2Credential("gws", filter.OAuth2Credential{TokenURL: "https://x/token", ClientID: "c", RefreshToken: "r"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*OAuth2); !ok {
		t.Fatalf("New(oauth2) = %T", src)
	}
	if _, err := New(nil, nil); err == nil {
		t.Fatal("New(nil) succeeded")
	}
	if _, err := New(&filter.Credential{}, nil); err == nil {
		t.Fatal("New(invalid) succeeded")
	}
}

func TestOAuth2RefreshRequestAndCaching(t *testing.T) {
	ts := newTokenServer(t, okToken(3600))
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	src := newSource(ts, clock)
	got, err := src.Token(context.Background())
	if err != nil || got != "access-1" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	form := ts.forms[0]
	want := map[string]string{
		"method": "POST", "content-type": "application/x-www-form-urlencoded",
		"grant_type": "refresh_token", "refresh_token": testRefreshToken, "client_id": "client-id", "client_secret": testClientSecret,
	}
	for k, v := range want {
		if form[k] != v {
			t.Errorf("refresh %s = %q, want %q", k, form[k], v)
		}
	}
	// Cached until expires_in minus the margin.
	clock.Advance(3600*time.Second - ExpiryMargin - time.Second)
	if got, _ := src.Token(context.Background()); got != "access-1" || ts.calls.Load() != 1 {
		t.Fatalf("Token before expiry = %q after %d calls", got, ts.calls.Load())
	}
	clock.Advance(time.Second)
	if got, _ := src.Token(context.Background()); got != "access-2" || ts.calls.Load() != 2 {
		t.Fatalf("Token at expiry minus margin = %q after %d calls", got, ts.calls.Load())
	}
}

func TestOAuth2ShortAndMissingLifetimes(t *testing.T) {
	for _, tt := range []struct {
		name      string
		expiresIn int
		fresh     time.Duration
		stale     time.Duration
	}{
		{"short", 30, 14 * time.Second, 15 * time.Second},
		{"missing", 0, defaultLifetime - ExpiryMargin - time.Second, defaultLifetime - ExpiryMargin},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTokenServer(t, okToken(tt.expiresIn))
			clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
			src := newSource(ts, clock)
			_, _ = src.Token(context.Background())
			clock.Advance(tt.fresh)
			_, _ = src.Token(context.Background())
			if ts.calls.Load() != 1 {
				t.Fatalf("refreshed early: %d calls", ts.calls.Load())
			}
			clock.Advance(tt.stale - tt.fresh)
			_, _ = src.Token(context.Background())
			if ts.calls.Load() != 2 {
				t.Fatalf("did not refresh: %d calls", ts.calls.Load())
			}
		})
	}
}

func TestOAuth2SingleFlight(t *testing.T) {
	ts := newTokenServer(t, okToken(3600))
	ts.release = make(chan struct{})
	src := newSource(ts, &fakeClock{now: time.Unix(1_800_000_000, 0)})
	const callers = 50
	var wg sync.WaitGroup
	results := make(chan string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := src.Token(context.Background())
			if err != nil {
				t.Errorf("Token: %v", err)
			}
			results <- got
		}()
	}
	// Let every caller queue behind the one refresh before it completes.
	deadline := time.Now().Add(5 * time.Second)
	for ts.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(ts.release)
	wg.Wait()
	close(results)
	for got := range results {
		if got != "access-1" {
			t.Fatalf("caller got %q", got)
		}
	}
	if n := ts.calls.Load(); n != 1 {
		t.Fatalf("token endpoint called %d times, want 1", n)
	}
}

func TestOAuth2Invalidate(t *testing.T) {
	ts := newTokenServer(t, okToken(3600))
	src := newSource(ts, &fakeClock{now: time.Unix(1_800_000_000, 0)})
	first, _ := src.Token(context.Background())
	src.Invalidate("some-older-token")
	if got, _ := src.Token(context.Background()); got != first || ts.calls.Load() != 1 {
		t.Fatalf("stale Invalidate dropped the token: %q, %d calls", got, ts.calls.Load())
	}
	src.Invalidate(first)
	if got, _ := src.Token(context.Background()); got != "access-2" || ts.calls.Load() != 2 {
		t.Fatalf("after Invalidate = %q, %d calls", got, ts.calls.Load())
	}
}

func TestOAuth2RotatedRefreshTokenKeptInMemory(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, _ *http.Request, n int32) {
		_, _ = fmt.Fprintf(w, `{"access_token":"access-%d","expires_in":3600,"refresh_token":"rotated-%d"}`, n, n)
	})
	src := newSource(ts, &fakeClock{now: time.Unix(1_800_000_000, 0)})
	first, _ := src.Token(context.Background())
	src.Invalidate(first)
	_, _ = src.Token(context.Background())
	if got := ts.forms[1]["refresh_token"]; got != "rotated-1" {
		t.Fatalf("second refresh used %q", got)
	}
}

// I6 and I12: refresh failures are errors that carry no response body and no
// secret.
func TestOAuth2RefreshFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request, int32)
		want    string
		cause   string
	}{
		{"invalid grant", func(w http.ResponseWriter, _ *http.Request, _ int32) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"invalid_grant","error_description":"echo %s"}`, testRefreshToken)
		}, "HTTP 400, invalid_grant", "invalid_grant"},
		{"hostile error code", func(w http.ResponseWriter, _ *http.Request, _ int32) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"error":"bad\n%s"}`, testClientSecret)
		}, "HTTP 401", "http-401"},
		{"server error", func(w http.ResponseWriter, _ *http.Request, _ int32) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}, "HTTP 503", "http-503"},
		{"not json", func(w http.ResponseWriter, _ *http.Request, _ int32) {
			_, _ = fmt.Fprint(w, "<html>"+testRefreshToken)
		}, "not JSON", "bad-response"},
		{"no access token", func(w http.ResponseWriter, _ *http.Request, _ int32) {
			_, _ = fmt.Fprint(w, `{"expires_in":3600}`)
		}, "no usable access_token", "bad-response"},
		{"header injection", func(w http.ResponseWriter, _ *http.Request, _ int32) {
			_, _ = fmt.Fprint(w, `{"access_token":"a\r\nX-Evil: 1","expires_in":3600}`)
		}, "no usable access_token", "bad-response"},
		{"non-ASCII access token", func(w http.ResponseWriter, _ *http.Request, _ int32) {
			_, _ = fmt.Fprint(w, `{"access_token":"a\u00e9","expires_in":3600}`)
		}, "no usable access_token", "bad-response"},
		{"non-ASCII refresh token", func(w http.ResponseWriter, _ *http.Request, _ int32) {
			_, _ = fmt.Fprint(w, "{\"access_token\":\"a\",\"refresh_token\":\"r\x80\"}")
		}, "unusable refresh_token", "bad-response"},
		{"too large", func(w http.ResponseWriter, _ *http.Request, _ int32) {
			_, _ = fmt.Fprint(w, `{"access_token":"`+strings.Repeat("a", MaxResponseBytes)+`"}`)
		}, "unreadable", "bad-response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTokenServer(t, tt.handler)
			src := newSource(ts, &fakeClock{now: time.Unix(1_800_000_000, 0)})
			got, err := src.Token(context.Background())
			if err == nil || got != "" {
				t.Fatalf("Token = %q, %v", got, err)
			}
			var refreshErr *RefreshError
			if !errors.As(err, &refreshErr) {
				t.Fatalf("error type %T", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
			if got := Cause(err); got != tt.cause {
				t.Fatalf("Cause = %q, want %q", got, tt.cause)
			}
			if IsGrantError(err) != (tt.cause == "invalid_grant") {
				t.Fatalf("IsGrantError = %v", IsGrantError(err))
			}
			for _, secret := range []string{testRefreshToken, testClientSecret} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaks a secret: %q", err)
				}
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		src := NewOAuth2(OAuth2Config{TokenURL: "https://127.0.0.1:1/token", ClientID: "c", RefreshToken: testRefreshToken})
		if _, err := src.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "unreachable") || Cause(err) != "unreachable" {
			t.Fatalf("Token = %v", err)
		}
	})
	if got := Cause(context.Canceled); got != "canceled" {
		t.Fatalf("Cause(context.Canceled) = %q", got)
	}
}

func TestOAuth2FailureBackoff(t *testing.T) {
	fail := atomic.Bool{}
	fail.Store(true)
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request, n int32) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		okToken(3600)(w, r, n)
	})
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	src := newSource(ts, clock)
	for i := 0; i < 5; i++ {
		if _, err := src.Token(context.Background()); err == nil {
			t.Fatal("Token succeeded during an outage")
		}
	}
	if ts.calls.Load() != 1 {
		t.Fatalf("failed refresh retried %d times within the backoff", ts.calls.Load())
	}
	fail.Store(false)
	clock.Advance(failureBackoff)
	if got, err := src.Token(context.Background()); err != nil || got != "access-2" {
		t.Fatalf("Token after backoff = %q, %v", got, err)
	}
}

func TestOAuth2CallerCancellation(t *testing.T) {
	ts := newTokenServer(t, okToken(3600))
	ts.release = make(chan struct{})
	src := newSource(ts, &fakeClock{now: time.Unix(1_800_000_000, 0)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := src.Token(ctx)
		done <- err
	}()
	for ts.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller got %v", err)
	}
	// The refresh itself continues and serves the next caller.
	close(ts.release)
	if got, err := src.Token(context.Background()); err != nil || got != "access-1" {
		t.Fatalf("Token = %q, %v", got, err)
	}
}

func TestExchangeReturnsScopeAndRefusesRedirects(t *testing.T) {
	sink := newTokenServer(t, okToken(3600))
	redirect := newTokenServer(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	})
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {testRefreshToken}}
	_, err := Exchange(context.Background(), redirect.Client(), redirect.URL, form)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") || sink.calls.Load() != 0 {
		t.Fatalf("Exchange via redirect = %v, sink calls %d", err, sink.calls.Load())
	}

	scoped := newTokenServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		_, _ = fmt.Fprint(w, `{"access_token":"a","expires_in":60,"refresh_token":"r","scope":"s1 s2"}`)
	})
	resp, err := Exchange(context.Background(), scoped.Client(), scoped.URL, form)
	if err != nil || resp.Scope != "s1 s2" || resp.RefreshToken != "r" || resp.ExpiresIn != 60 {
		t.Fatalf("Exchange = %+v, %v", resp, err)
	}
}

func TestHasUnsafe(t *testing.T) {
	for value, want := range map[string]bool{
		"ya29.a0-_~+/=": false, "": false, "a b": true, "a\x7f": true, "a\u00e9": true, "a\x80": true, "a\n": true,
	} {
		if got := HasUnsafe(value); got != want {
			t.Errorf("HasUnsafe(%q) = %v, want %v", value, got, want)
		}
	}
}
