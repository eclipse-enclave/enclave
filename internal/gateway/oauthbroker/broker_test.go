// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureAccess = "synthetic-provider-access-token-01"
const fixtureRefresh = "synthetic-provider-refresh-token-01"
const rotatedAccess = "synthetic-provider-access-token-02"
const rotatedRefresh = "synthetic-provider-refresh-token-02"

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// Fixtures perform no network I/O, so the same stub can serve both transports.
func fixtureRoundTrip(b *Broker, req *http.Request, transport http.RoundTripper) (*http.Response, error) {
	return b.RoundTrip(req, transport, transport)
}

func fixtureBroker(t *testing.T) *Broker {
	t.Helper()
	root := t.TempDir()
	config := Config{
		Tool:     "claude",
		StoreDir: filepath.Join(root, "identity"), RuntimeDir: filepath.Join(root, "runtime"),
		LockPath: filepath.Join(root, "owner.lock"), Identity: "fresh", Session: "same-container-name", Incarnation: "first",
	}
	for _, dir := range []string{config.StoreDir, config.RuntimeDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(config.LockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	b.now = func() time.Time { return time.Unix(1800000000, 0) }
	return b
}

func fixtureToken() tokenResponse {
	return tokenResponse{
		Access: fixtureAccess, Refresh: fixtureRefresh, Type: "Bearer", Expires: 3600, Scope: "user:profile user:inference",
		Account:      &account{UUID: "11111111-1111-4111-8111-111111111111", Email: "fixture@example.test"},
		Organization: &organization{UUID: "22222222-2222-4222-8222-222222222222"},
	}
}

func TestOpenRequiresExistingPrivateHostLock(t *testing.T) {
	b := fixtureBroker(t)
	config := b.config
	config.LockPath = filepath.Join(t.TempDir(), "not-mounted.lock")
	if _, err := Open(config); !errors.Is(err, ErrState) {
		t.Fatalf("missing shared lock was accepted: %v", err)
	}
	if _, err := os.Lstat(config.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("gateway created a private substitute for the missing host lock")
	}
	if err := os.Symlink(b.config.LockPath, config.LockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(config); !errors.Is(err, ErrState) {
		t.Fatalf("symlinked shared lock was accepted: %v", err)
	}
	if err := os.Chmod(b.config.LockPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(b.config); !errors.Is(err, ErrState) {
		t.Fatalf("non-private shared lock was accepted: %v", err)
	}
}

func fixtureResponse(value any) *http.Response {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return rawFixtureResponse(data)
}

func rawFixtureResponse(data []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data)),
	}
}

func fixtureRequest(t *testing.T, input tokenRequest) *http.Request {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://"+tokenHost+tokenPath, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func loginRequest(t *testing.T) *http.Request {
	t.Helper()
	return fixtureRequest(t, tokenRequest{GrantType: "authorization_code", ClientID: clientID, Code: "synthetic-code", Verifier: "synthetic-verifier", RedirectURI: "https://claude.ai/oauth/code/callback"})
}

func refreshRequest(t *testing.T, b *Broker) *http.Request {
	t.Helper()
	return fixtureRequest(t, tokenRequest{GrantType: "refresh_token", ClientID: clientID, Refresh: b.registry.Refresh})
}

func responseToken(t *testing.T, response *http.Response) tokenResponse {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "synthetic-provider-") {
		t.Fatalf("provider credential leaked: %s", data)
	}
	var token tokenResponse
	if err := json.Unmarshal(data, &token); err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != response.ContentLength || response.Header.Get("Content-Length") != strconv.Itoa(len(data)) {
		t.Fatal("incorrect sanitized response length")
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("sanitized response is cacheable")
	}
	return token
}

func enroll(t *testing.T, b *Broker) tokenResponse {
	t.Helper()
	response, err := fixtureRoundTrip(b, loginRequest(t), fixtureTransport(func(*http.Request) (*http.Response, error) {
		var registered registry
		if err := readPrivateJSON(filepath.Join(b.config.RuntimeDir, registryFile), &registered); err != nil || registered.Access != b.registry.Access {
			t.Fatalf("exchange dispatched before durable registration: %+v, %v", registered, err)
		}
		pending, err := b.readGrant()
		if err != nil || pending == nil || pending.Status != "uncertain" {
			t.Fatalf("exchange dispatched before durable intent: %+v, %v", pending, err)
		}
		return fixtureResponse(fixtureToken()), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return responseToken(t, response)
}

func advanceToRefresh(b *Broker) {
	now := b.now().Add(time.Hour - time.Minute)
	b.now = func() time.Time { return now }
}

func TestCaptureKeepsRealTokensInDurableGrantAndProjectsNativeView(t *testing.T) {
	b := fixtureBroker(t)
	if !b.RequiresMITM(tokenHost) {
		t.Fatal("capture unavailable before enrollment")
	}
	view, err := b.NativeView()
	if err != nil || len(view) != 0 {
		t.Fatalf("fresh view = %q, %v", view, err)
	}
	projected := enroll(t, b)
	if projected.Access != b.registry.Access || projected.Refresh != b.registry.Refresh || projected.Access == projected.Refresh {
		t.Fatal("missing distinct registered handles")
	}
	stored, err := b.readGrant()
	if err != nil || stored.Token.Access != fixtureAccess || stored.Token.Refresh != fixtureRefresh || stored.Generation != 1 {
		t.Fatalf("stored grant = %+v, %v", stored, err)
	}
	for _, path := range []string{filepath.Join(b.config.StoreDir, grantFile), filepath.Join(b.config.RuntimeDir, registryFile)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private record permissions: %v, %v", info, err)
		}
	}
	view, err = b.NativeView()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(view), "synthetic-provider-") || !bytes.Contains(view, []byte(projected.Access)) || !bytes.Contains(view, []byte(projected.Refresh)) {
		t.Fatalf("invalid native view: %s", view)
	}
	var native struct {
		OAuth struct {
			Expires int64    `json:"expiresAt"`
			Scopes  []string `json:"scopes"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(view, &native); err != nil {
		t.Fatal(err)
	}
	if native.OAuth.Expires != stored.ExpiresAt*1000 || len(native.OAuth.Scopes) != 2 {
		t.Fatalf("native expiry/scopes = %+v", native)
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"fixture"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+projected.Access)
	response, err := fixtureRoundTrip(b, req, fixtureTransport(func(outgoing *http.Request) (*http.Response, error) {
		if got := outgoing.Header.Get("Authorization"); got != "Bearer "+fixtureAccess {
			t.Errorf("released authorization = %q", got)
		}
		return rawFixtureResponse([]byte("unrelated streamed body")), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if req.Header.Get("Authorization") != "Bearer "+projected.Access {
		t.Fatal("input request mutated with real token")
	}
}

func TestRefreshSerializesDuplicatesAndRecoversLostReplyAcrossReload(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(strconv.FormatBool(rotate), func(t *testing.T) {
			b := fixtureBroker(t)
			initial := enroll(t, b)
			advanceToRefresh(b)
			otherProcess, err := Open(b.config)
			if err != nil {
				t.Fatal(err)
			}
			otherProcess.now = b.now
			var exchanges atomic.Int32
			transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
				exchanges.Add(1)
				var input tokenRequest
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					return nil, err
				}
				if input.Refresh != fixtureRefresh {
					t.Errorf("refresh token = %q", input.Refresh)
				}
				token := fixtureToken()
				token.Access = rotatedAccess
				token.Refresh = ""
				if rotate {
					token.Refresh = rotatedRefresh
				}
				token.Account, token.Organization = nil, nil
				return fixtureResponse(token), nil
			})
			var wg sync.WaitGroup
			for i := range 12 {
				owner := b
				if i%2 != 0 {
					owner = otherProcess
				}
				req := refreshRequest(t, owner)
				wg.Add(1)
				go func() {
					defer wg.Done()
					response, err := fixtureRoundTrip(owner, req, transport)
					if err != nil {
						t.Errorf("refresh: %v", err)
						return
					}
					_ = response.Body.Close() // The client never processes this reply.
				}()
			}
			wg.Wait()
			if got := exchanges.Load(); got != 1 {
				t.Fatalf("provider refresh calls = %d, want one", got)
			}
			reloaded, err := Open(b.config)
			if err != nil {
				t.Fatal(err)
			}
			now := b.now().Add(time.Minute)
			reloaded.now = func() time.Time { return now }
			response, err := fixtureRoundTrip(reloaded, refreshRequest(t, reloaded), transport)
			if err != nil {
				t.Fatal(err)
			}
			projected := responseToken(t, response)
			if projected.Access != initial.Access || projected.Refresh != initial.Refresh || projected.Expires != 3540 {
				t.Fatalf("reload projection = %+v", projected)
			}
			if exchanges.Load() != 1 {
				t.Fatal("lost-reply retry reused consumed refresh token")
			}
			stored, err := reloaded.readGrant()
			if err != nil {
				t.Fatal(err)
			}
			wantRefresh := fixtureRefresh
			if rotate {
				wantRefresh = rotatedRefresh
			}
			if stored.Generation != 2 || stored.Token.Refresh != wantRefresh || stored.Token.Access != rotatedAccess {
				t.Fatalf("committed replacement = %+v", stored)
			}
			req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+initial.Access)
			response, err = fixtureRoundTrip(reloaded, req, fixtureTransport(func(outgoing *http.Request) (*http.Response, error) {
				if outgoing.Header.Get("Authorization") != "Bearer "+rotatedAccess {
					t.Error("stable handle resolved to stale access token")
				}
				return rawFixtureResponse(nil), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
		})
	}
}

func TestRefreshCommitBoundaryFailuresNeverReplayConsumedToken(t *testing.T) {
	for _, failAt := range []string{"before-intent", "after-intent", "before-commit", "after-commit"} {
		t.Run(failAt, func(t *testing.T) {
			b := fixtureBroker(t)
			enroll(t, b)
			advanceToRefresh(b)
			b.write = func(path string, value any) error {
				g := value.(*grant)
				boundary := "intent"
				if g.Status == "ready" {
					boundary = "commit"
				}
				if failAt == "before-"+boundary {
					return errors.New("injected persistence failure")
				}
				if err := durableJSON(path, value); err != nil {
					return err
				}
				if failAt == "after-"+boundary {
					return errors.New("injected lost commit acknowledgement")
				}
				return nil
			}
			calls := 0
			transport := fixtureTransport(func(*http.Request) (*http.Response, error) {
				calls++
				token := fixtureToken()
				token.Access, token.Refresh = rotatedAccess, rotatedRefresh
				return fixtureResponse(token), nil
			})
			response, err := fixtureRoundTrip(b, refreshRequest(t, b), transport)
			if response != nil || !errors.Is(err, ErrState) {
				t.Fatalf("failed persistence = %v, %v", response, err)
			}
			wantCalls := 0
			if strings.HasSuffix(failAt, "commit") {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("provider calls before failure = %d, want %d", calls, wantCalls)
			}
			response, err = fixtureRoundTrip(b, refreshRequest(t, b), transport)
			if response != nil || !errors.Is(err, ErrState) || calls != wantCalls {
				t.Fatalf("failed broker reused unfenced state: %v, %v, calls %d", response, err, calls)
			}
			if _, err := b.NativeView(); !errors.Is(err, ErrState) {
				t.Fatalf("failed broker exposed native view: %v", err)
			}
			reloaded, err := Open(b.config)
			if err != nil {
				t.Fatal(err)
			}
			reloaded.now = b.now
			response, err = fixtureRoundTrip(reloaded, refreshRequest(t, reloaded), transport)
			switch failAt {
			case "before-intent":
				if err != nil || calls != 1 {
					t.Fatalf("safe pre-dispatch retry = %v, calls %d", err, calls)
				}
				_ = response.Body.Close()
			case "after-commit":
				if err != nil || calls != 1 {
					t.Fatalf("committed retry = %v, calls %d", err, calls)
				}
				responseToken(t, response)
			default:
				if !errors.Is(err, ErrUncertain) || response != nil || calls != wantCalls {
					t.Fatalf("uncertain retry = %v, calls %d", err, calls)
				}
				if _, err := reloaded.NativeView(); !errors.Is(err, ErrUncertain) {
					t.Fatalf("uncertain native view error = %v", err)
				}
			}
		})
	}
}

func TestRefreshNetworkFailureRecoversUncertainAndAllowsReauthentication(t *testing.T) {
	b := fixtureBroker(t)
	enroll(t, b)
	advanceToRefresh(b)
	response, err := fixtureRoundTrip(b, refreshRequest(t, b), fixtureTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("upstream lost after rotating synthetic-token")
	}))
	if response != nil || !errors.Is(err, ErrUncertain) || strings.Contains(err.Error(), "synthetic-token") {
		t.Fatalf("network failure = %v, %v", response, err)
	}
	reloaded, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = b.now
	response, err = fixtureRoundTrip(reloaded, refreshRequest(t, reloaded), fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Error("uncertain refresh dispatched")
		return nil, io.EOF
	}))
	if response != nil || !errors.Is(err, ErrUncertain) {
		t.Fatalf("uncertain reload = %v, %v", response, err)
	}
	enroll(t, reloaded)
}

func TestNewSessionRotatesHandlesAndRejectsOldSession(t *testing.T) {
	b := fixtureBroker(t)
	old := enroll(t, b)
	config := b.config
	config.Incarnation = "second"
	if _, err := Open(config); !errors.Is(err, ErrState) {
		t.Fatalf("reused runtime directory accepted new incarnation: %v", err)
	}
	config.RuntimeDir = filepath.Join(t.TempDir(), "new-runtime")
	if err := os.Mkdir(config.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	newSession, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	newSession.now = b.now
	if newSession.registry.Access == old.Access || newSession.registry.Refresh == old.Refresh {
		t.Fatal("new session reused previous handles")
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+old.Access)
	response, err := fixtureRoundTrip(newSession, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Error("old-session request dispatched")
		return nil, io.EOF
	}))
	if response != nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("old handle = %v, %v", response, err)
	}
	config.Identity = "other"
	if _, err := Open(config); !errors.Is(err, ErrState) {
		t.Fatalf("wrong identity accepted registry: %v", err)
	}
}

func TestRejectsHandleReleaseOutsideDeclaredFieldAndDestination(t *testing.T) {
	for _, variant := range []string{"plaintext", "wrong-host", "wrong-port", "credential-path", "wrong-header", "duplicate-authorization", "refresh-as-access", "extra-handle", "userinfo", "wrong-host-header", "wrong-sni", "first-party-non-release-host"} {
		t.Run(variant, func(t *testing.T) {
			b := fixtureBroker(t)
			enroll(t, b)
			req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+b.registry.Access)
			switch variant {
			case "plaintext":
				req.URL.Scheme = "http"
			case "wrong-host":
				req.URL.Host = "unrelated.example"
				req.Host = req.URL.Host
			case "wrong-port":
				req.URL.Host += ":444"
				req.Host = req.URL.Host
			case "credential-path":
				req.URL.Path = "/api/oauth/claude_cli/create_api_key"
			case "first-party-non-release-host":
				req.URL.Host = "claude.ai"
				req.Host = req.URL.Host
			case "wrong-header":
				req.Header.Del("Authorization")
				req.Header.Set("X-Api-Key", b.registry.Access)
			case "duplicate-authorization":
				req.Header.Add("Authorization", "Bearer "+b.registry.Access)
			case "refresh-as-access":
				req.Header.Set("Authorization", "Bearer "+b.registry.Refresh)
			case "extra-handle":
				req.Header.Set("X-Extra", b.registry.Refresh)
			case "userinfo":
				req.URL.User = url.User("ignored")
			case "wrong-host-header":
				req.Host = "other.example"
			case "wrong-sni":
				req.TLS = &tls.ConnectionState{ServerName: "other.example"}
			}
			response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) { t.Error("denied release dispatched"); return nil, io.EOF }))
			if response != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("invalid release = %v, %v", response, err)
			}
		})
	}
}

func TestTokenOriginMismatchesNeverFallBackToRawPassthrough(t *testing.T) {
	for _, variant := range []string{"plaintext", "port", "uppercase", "trailing-dot", "opaque", "path", "query", "empty-query", "encoded-path", "sni"} {
		t.Run(variant, func(t *testing.T) {
			b := fixtureBroker(t)
			req := loginRequest(t)
			switch variant {
			case "plaintext":
				req.URL.Scheme = "http"
			case "port":
				req.URL.Host += ":444"
				req.Host = req.URL.Host
			case "uppercase":
				req.URL.Host = strings.ToUpper(req.URL.Host)
				req.Host = req.URL.Host
			case "trailing-dot":
				req.URL.Host += "."
				req.Host = req.URL.Host
			case "opaque":
				req.URL.Opaque = "//" + tokenHost + tokenPath
			case "path":
				req.URL.Path += "/"
			case "query":
				req.URL.RawQuery = "ignored=true"
			case "empty-query":
				req.URL.ForceQuery = true
			case "encoded-path":
				req.URL.RawPath = "/v1/oauth/%74oken"
			case "sni":
				req.TLS = &tls.ConnectionState{ServerName: "other.example"}
			}
			response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
				t.Error("unsupported token origin fell back to raw passthrough")
				return fixtureResponse(fixtureToken()), nil
			}))
			if response != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("unsupported token origin = %v, %v", response, err)
			}
		})
	}
}

func TestCaptureStripsClientRoutingAndRetryHeaders(t *testing.T) {
	b := fixtureBroker(t)
	req := loginRequest(t)
	for _, name := range []string{"Authorization", "Cookie", "Idempotency-Key", "X-Routing-Override"} {
		req.Header.Set(name, "workload-controlled-value")
	}
	// A forwarded request commonly uses explicit :443 while its Host header
	// retains the client's equivalent default-port spelling.
	req.URL.Host += ":443"
	response, err := fixtureRoundTrip(b, req, fixtureTransport(func(outgoing *http.Request) (*http.Response, error) {
		for _, name := range []string{"Authorization", "Cookie", "Idempotency-Key", "X-Routing-Override"} {
			if outgoing.Header.Get(name) != "" {
				t.Errorf("provider received client-controlled %s", name)
			}
		}
		if outgoing.GetBody != nil {
			t.Error("token exchange is replayable")
		}
		if outgoing.Header.Get("Accept-Encoding") != "identity" {
			t.Error("token exchange permits uncontrolled decompression")
		}
		return fixtureResponse(fixtureToken()), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	responseToken(t, response)
}

func TestCaptureRejectsMalformedAndUnexpectedProviderResponses(t *testing.T) {
	valid, err := json.Marshal(fixtureToken())
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"unknown", "nested-unknown", "duplicate", "escaped-duplicate", "case-key", "null", "truncated", "length-mismatch", "oversized", "encoded-oversized", "gzip-decoded-oversized", "unsupported-encoding", "unknown-type", "multiple-types", "invalid-gzip", "redirect", "invalid-account", "invalid-token", "unknown-error"} {
		t.Run(variant, func(t *testing.T) {
			b := fixtureBroker(t)
			body := string(valid)
			switch variant {
			case "unknown":
				body = strings.TrimSuffix(body, "}") + `,"new_credential":"must-not-pass"}`
			case "nested-unknown":
				body = strings.Replace(body, `"email_address":`, `"credential":"must-not-pass","email_address":`, 1)
			case "duplicate":
				body = strings.TrimSuffix(body, "}") + `,"access_token":"duplicate-value"}`
			case "escaped-duplicate":
				body = strings.TrimSuffix(body, "}") + `,"access_\u0074oken":"duplicate-value"}`
			case "case-key":
				body = strings.Replace(body, `"access_token"`, `"Access_Token"`, 1)
			case "null":
				body = strings.Replace(body, `"token_type":"Bearer"`, `"token_type":null`, 1)
			case "oversized", "gzip-decoded-oversized":
				// Otherwise-valid JSON isolates the size bound from schema rejection.
				body = strings.Repeat(" ", maxDocument+1-len(body)) + body
				if variant == "gzip-decoded-oversized" {
					body = string(gzipFixture(t, []byte(body)))
				}
			case "invalid-account":
				body = strings.Replace(body, "11111111-1111-4111-8111-111111111111", "not-a-uuid", 1)
			case "invalid-token":
				body = strings.Replace(body, fixtureAccess, "short", 1)
			case "unknown-error":
				body = `{"error":"provider-secret-value"}`
			}
			// Derive framing from the final payload so schema and decompression
			// cases cannot pass because an earlier length check rejected them.
			response := rawFixtureResponse([]byte(body))
			switch variant {
			case "truncated":
				response.Body = &failingBody{Reader: strings.NewReader(body[:len(body)-1])}
			case "length-mismatch":
				response.ContentLength++
			case "oversized":
				response.ContentLength = -1 // Exercise the stream bound, not the declared-size check.
			case "encoded-oversized":
				response.ContentLength = maxDocument + 1
			case "gzip-decoded-oversized", "invalid-gzip":
				response.Header.Set("Content-Encoding", "gzip")
			case "unsupported-encoding":
				response.Header.Set("Content-Encoding", "br")
			case "unknown-type":
				response.Header.Set("Content-Type", "text/plain")
			case "multiple-types":
				response.Header.Add("Content-Type", "application/json")
			case "redirect":
				response.StatusCode = http.StatusTemporaryRedirect
				response.Header.Set("Location", "https://unrelated.example/capture")
			case "unknown-error":
				response.StatusCode = http.StatusBadRequest
			}
			got, err := fixtureRoundTrip(b, loginRequest(t), fixtureTransport(func(*http.Request) (*http.Response, error) { return response, nil }))
			if got != nil || !errors.Is(err, ErrResponse) {
				t.Fatalf("unsafe capture response = %v, %v", got, err)
			}
			stored, err := b.readGrant()
			if err != nil || stored.Status != "uncertain" {
				t.Fatalf("failed capture state = %+v, %v", stored, err)
			}
		})
	}
}

type failingBody struct{ io.Reader }

func (r *failingBody) Read(buf []byte) (int, error) {
	n, err := r.Reader.Read(buf)
	if errors.Is(err, io.EOF) {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}
func (*failingBody) Close() error { return nil }

func gzipFixture(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestResponseBodyEnforcesEncodedAndDecodedLimits(t *testing.T) {
	valid, err := json.Marshal(fixtureToken())
	if err != nil {
		t.Fatal(err)
	}
	for _, encoding := range []string{"identity", "gzip"} {
		for _, size := range []int{maxDocument, maxDocument + 1} {
			t.Run(encoding+"/"+strconv.Itoa(size), func(t *testing.T) {
				body := append(bytes.Repeat([]byte(" "), size-len(valid)), valid...)
				encoded := body
				if encoding == "gzip" {
					encoded = gzipFixture(t, body)
				}
				response := rawFixtureResponse(encoded)
				response.Header.Set("Content-Encoding", encoding)
				if encoding == "identity" {
					response.ContentLength = -1
				}
				got, err := responseBody(response)
				if size > maxDocument {
					if got != nil || !errors.Is(err, ErrResponse) {
						t.Fatalf("oversized %s body = %d bytes, %v", encoding, len(got), err)
					}
				} else if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("at-limit %s body = %d bytes, %v", encoding, len(got), err)
				}
			})
		}
	}
}

func TestCaptureSanitizesEncodingHeadersTrailersAndOAuthErrors(t *testing.T) {
	t.Run("gzip", func(t *testing.T) {
		b := fixtureBroker(t)
		data, err := json.Marshal(fixtureToken())
		if err != nil {
			t.Fatal(err)
		}
		upstream := rawFixtureResponse(gzipFixture(t, data))
		upstream.Header.Set("Content-Encoding", "gzip")
		upstream.Header.Set("Set-Cookie", fixtureAccess)
		upstream.Header.Set("X-Diagnostic", fixtureRefresh)
		upstream.Trailer = http.Header{"X-Credential": {fixtureRefresh}}
		response, err := fixtureRoundTrip(b, loginRequest(t), fixtureTransport(func(*http.Request) (*http.Response, error) { return upstream, nil }))
		if err != nil {
			t.Fatal(err)
		}
		responseToken(t, response)
		if len(response.Header) != 4 || len(response.Trailer) != 0 {
			t.Fatalf("unvetted headers/trailers = %v, %v", response.Header, response.Trailer)
		}
	})
	for _, code := range []string{"authorization_pending", "slow_down", "invalid_grant"} {
		t.Run(code, func(t *testing.T) {
			b := fixtureBroker(t)
			upstream := fixtureResponse(map[string]string{"error": code, "error_description": fixtureAccess, "error_uri": "https://example.test/" + fixtureRefresh})
			upstream.StatusCode = http.StatusBadRequest
			upstream.Header.Set("X-Credential", fixtureAccess)
			response, err := fixtureRoundTrip(b, loginRequest(t), fixtureTransport(func(*http.Request) (*http.Response, error) { return upstream, nil }))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			data, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != `{"error":"`+code+`"}` || len(response.Header) != 4 {
				t.Fatalf("sanitized OAuth error = %s, %v", data, response.Header)
			}
		})
	}
}

func TestInvalidRefreshRequestsNeverReachProvider(t *testing.T) {
	for _, variant := range []string{"wrong-handle", "access-handle", "mixed-grant", "wrong-client", "wrong-method", "duplicate-field", "unknown-field", "wrong-content", "cancelled"} {
		t.Run(variant, func(t *testing.T) {
			b := fixtureBroker(t)
			enroll(t, b)
			advanceToRefresh(b)
			input := tokenRequest{GrantType: "refresh_token", ClientID: clientID, Refresh: b.registry.Refresh}
			switch variant {
			case "wrong-handle":
				input.Refresh = fixtureRefresh
			case "access-handle":
				input.Refresh = b.registry.Access
			case "mixed-grant":
				input.Code = "bootstrap-code"
			case "wrong-client":
				input.ClientID = "another-client"
			}
			req := fixtureRequest(t, input)
			switch variant {
			case "wrong-method":
				req.Method = http.MethodGet
			case "duplicate-field", "unknown-field":
				data, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				extra := `,"refresh_token":"duplicate"}`
				if variant == "unknown-field" {
					extra = `,"extra_secret":"unknown"}`
				}
				body := strings.TrimSuffix(string(data), "}") + extra
				req.Body = io.NopCloser(strings.NewReader(body))
				req.ContentLength = int64(len(body))
				req.GetBody = nil
			case "wrong-content":
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			case "cancelled":
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) { t.Error("invalid refresh dispatched"); return nil, io.EOF }))
			if response != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("invalid refresh result = %v, %v", response, err)
			}
			stored, err := b.readGrant()
			if err != nil || stored.Generation != 1 || stored.Status != "ready" {
				t.Fatalf("rejected request mutated grant: %+v, %v", stored, err)
			}
		})
	}
}

func TestShortLivedRefreshDuplicatesShareCommittedGeneration(t *testing.T) {
	b := fixtureBroker(t)
	enroll(t, b)
	advanceToRefresh(b)
	var calls atomic.Int32
	transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		var input tokenRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			return nil, err
		}
		wantRefresh := fixtureRefresh
		if call > 1 {
			wantRefresh = rotatedRefresh
		}
		if input.Refresh != wantRefresh {
			t.Errorf("refresh used %q, want current token %q", input.Refresh, wantRefresh)
		}
		token := fixtureToken()
		token.Access, token.Refresh, token.Expires = rotatedAccess, rotatedRefresh, 60
		return fixtureResponse(token), nil
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := fixtureRoundTrip(b, refreshRequest(t, b), transport)
			if err != nil {
				t.Errorf("short-lived refresh: %v", err)
				return
			}
			_ = response.Body.Close()
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("short-lived duplicate refresh calls = %d", calls.Load())
	}
	reloaded, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = b.now
	response, err := fixtureRoundTrip(reloaded, refreshRequest(t, reloaded), transport)
	if err != nil {
		t.Fatal(err)
	}
	if token := responseToken(t, response); token.Expires != 60 || calls.Load() != 1 {
		t.Fatalf("short-lived retry = %+v, calls %d", token, calls.Load())
	}
	// The retry grace period must not prevent an early refresh of generation 2.
	now := b.now().Add(refreshReplayWindow + time.Second)
	reloaded.now = func() time.Time { return now }
	response, err = fixtureRoundTrip(reloaded, refreshRequest(t, reloaded), transport)
	if err != nil {
		t.Fatal(err)
	}
	responseToken(t, response)
	if calls.Load() != 2 {
		t.Fatalf("early short-lived token refresh calls = %d", calls.Load())
	}
}

func TestRefreshWindowAppliesToEveryGeneration(t *testing.T) {
	b := fixtureBroker(t)
	initial := enroll(t, b)
	for generation := uint64(2); generation <= 4; generation++ {
		previous, err := b.readGrant()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(previous.ExpiresAt, 0).Add(-refreshWindow + time.Second)
		b.now = func() time.Time { return now }
		calls := 0
		transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			var input tokenRequest
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				return nil, err
			}
			if input.Refresh != previous.Token.Refresh {
				t.Fatal("refresh used a consumed token")
			}
			token := fixtureToken()
			token.Access = fixtureAccess + strconv.FormatUint(generation, 10)
			token.Refresh = fixtureRefresh + strconv.FormatUint(generation, 10)
			return fixtureResponse(token), nil
		})
		response, err := fixtureRoundTrip(b, refreshRequest(t, b), transport)
		if err != nil {
			t.Fatal(err)
		}
		projected := responseToken(t, response)
		if calls != 1 || projected.Access != initial.Access || projected.Refresh != initial.Refresh {
			t.Fatal("early refresh did not preserve handles and rotate exactly once")
		}
		committed, err := b.readGrant()
		if err != nil {
			t.Fatal(err)
		}
		if committed.Generation != generation || !committed.CommittedAt.Equal(now) || previous.ExpiresAt <= now.Unix() {
			t.Fatal("refresh did not commit before the previous token expired")
		}
		// Reload within the grace must project the durable generation, not rotate.
		reloaded, err := Open(b.config)
		if err != nil {
			t.Fatal(err)
		}
		reloaded.now = b.now
		response, err = fixtureRoundTrip(reloaded, refreshRequest(t, reloaded), transport)
		if err != nil {
			t.Fatal(err)
		}
		responseToken(t, response)
		if calls != 1 {
			t.Fatal("immediate reload retry rotated again")
		}
	}
}

func TestShortGrantGraceAllowsRefreshBeforeExpiry(t *testing.T) {
	b := fixtureBroker(t)
	response, err := fixtureRoundTrip(b, loginRequest(t), fixtureTransport(func(*http.Request) (*http.Response, error) {
		token := fixtureToken()
		token.Expires = 10
		return fixtureResponse(token), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	responseToken(t, response)
	calls := 0
	transport := fixtureTransport(func(*http.Request) (*http.Response, error) {
		calls++
		token := fixtureToken()
		token.Expires = 10
		return fixtureResponse(token), nil
	})
	response, err = fixtureRoundTrip(b, refreshRequest(t, b), transport)
	if err != nil {
		t.Fatal(err)
	}
	responseToken(t, response)
	if calls != 0 {
		t.Fatal("fresh grant was not deduplicated")
	}
	now := b.now().Add(6 * time.Second)
	b.now = func() time.Time { return now }
	response, err = fixtureRoundTrip(b, refreshRequest(t, b), transport)
	if err != nil {
		t.Fatal(err)
	}
	responseToken(t, response)
	if calls != 1 {
		t.Fatal("half-lifetime grace blocked refresh before expiry")
	}
}

func TestBrokerUsesSeparateExchangeTransport(t *testing.T) {
	b := fixtureBroker(t)
	apiCalls, exchangeCalls := 0, 0
	api := fixtureTransport(func(req *http.Request) (*http.Response, error) {
		apiCalls++
		if req.URL.Hostname() != accessHost {
			t.Fatal("token exchange reached pooled API transport")
		}
		return rawFixtureResponse(nil), nil
	})
	exchange := fixtureTransport(func(req *http.Request) (*http.Response, error) {
		exchangeCalls++
		if req.URL.Hostname() != tokenHost {
			t.Fatal("unrelated request reached exchange transport")
		}
		return fixtureResponse(fixtureToken()), nil
	})
	response, err := b.RoundTrip(loginRequest(t), api, exchange)
	if err != nil {
		t.Fatal(err)
	}
	projected := responseToken(t, response)
	request, err := http.NewRequest(http.MethodPost, "https://"+accessHost+"/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+projected.Access)
	response, err = b.RoundTrip(request, api, exchange)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if apiCalls != 1 || exchangeCalls != 1 {
		t.Fatalf("transport calls: API=%d exchange=%d", apiCalls, exchangeCalls)
	}
}

func TestSubsecondCommitTimeSurvivesReloadAndDeduplicatesShortGrants(t *testing.T) {
	b := fixtureBroker(t)
	now := b.now().Add(900 * time.Millisecond)
	b.now = func() time.Time { return now }
	calls := 0
	transport := fixtureTransport(func(*http.Request) (*http.Response, error) {
		calls++
		token := fixtureToken()
		token.Expires = 1
		return fixtureResponse(token), nil
	})
	response, err := fixtureRoundTrip(b, loginRequest(t), transport)
	if err != nil {
		t.Fatal(err)
	}
	responseToken(t, response)
	reloaded, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = b.now
	stored, err := reloaded.readGrant()
	if err != nil || !stored.CommittedAt.Equal(now) {
		t.Fatal("commit time lost subsecond precision")
	}
	response, err = fixtureRoundTrip(reloaded, refreshRequest(t, reloaded), transport)
	if err != nil {
		t.Fatal(err)
	}
	responseToken(t, response)
	if calls != 1 {
		t.Fatal("immediate retry of a short grant rotated again")
	}
}

func TestAccessReleasesAcrossFirstPartyAPIOrigins(t *testing.T) {
	b := fixtureBroker(t)
	enroll(t, b)
	for _, target := range []struct{ method, url string }{
		{http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true"},
		{http.MethodPost, "https://api.anthropic.com/v1/messages/count_tokens"},
		{http.MethodGet, "https://api.anthropic.com/api/oauth/usage"},
		{http.MethodDelete, "https://api.anthropic.com/api/oauth/files/file_01"},
		{http.MethodPost, "https://mcp-proxy.anthropic.com/v1/mcp/server_01"},
	} {
		t.Run(target.method+" "+target.url, func(t *testing.T) {
			req, err := http.NewRequest(target.method, target.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+b.registry.Access)
			req.Header.Set("Accept-Encoding", "gzip, br")
			called := false
			response, err := fixtureRoundTrip(b, req, fixtureTransport(func(outgoing *http.Request) (*http.Response, error) {
				called = true
				if outgoing.Header.Get("Authorization") != "Bearer "+fixtureAccess || outgoing.Header.Get("Accept-Encoding") != "identity" {
					t.Error("released request lacks the real token or uncompressed scanning")
				}
				return rawFixtureResponse([]byte(`{"ok":true}`)), nil
			}))
			if err != nil || !called {
				t.Fatalf("first-party release = %v, called %v", err, called)
			}
			_ = response.Body.Close()
		})
	}
}

func TestFirstPartyCredentialPathsNeverPassThrough(t *testing.T) {
	b := fixtureBroker(t)
	for _, target := range []string{
		"https://api.anthropic.com/v1/oauth/token",
		"https://console.anthropic.com/v1/oauth/token",
		"https://claude.ai/oauth/token",
		"https://platform.claude.com/v1/oauth/token/revoke",
		"https://api.anthropic.com/api/oauth/claude_cli/create_api_key",
		"https://api.anthropic.com/v1/organizations/api_keys",
	} {
		req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(`{"grant_type":"refresh_token"}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
			t.Errorf("credential endpoint %s dispatched", target)
			return nil, io.EOF
		}))
		if response != nil || !errors.Is(err, ErrDenied) {
			t.Fatalf("%s = %v, %v", target, response, err)
		}
	}
}

func readAllChunks(body io.Reader) (string, error) {
	var out strings.Builder
	buffer := make([]byte, 7)
	for {
		n, err := body.Read(buffer)
		out.Write(buffer[:n])
		if errors.Is(err, io.EOF) {
			return out.String(), nil
		}
		if err != nil {
			return out.String(), err
		}
	}
}

func TestFirstPartyResponsesNeverCarryCredentials(t *testing.T) {
	b := fixtureBroker(t)
	enroll(t, b)
	for _, tc := range []struct {
		name, url, contentType, body string
		header                       http.Header
		released                     bool
		wantErr                      bool
	}{
		{name: "reflected access token", url: "https://api.anthropic.com/v1/messages", contentType: "text/event-stream", body: "data: " + fixtureAccess + "\n\n", released: true, wantErr: true},
		{name: "reflected refresh token", url: "https://api.anthropic.com/api/oauth/profile", contentType: "application/json", body: `{"echo":"` + fixtureRefresh + `"}`, released: true, wantErr: true},
		{name: "reflected header", url: "https://api.anthropic.com/api/oauth/profile", contentType: "application/json", body: `{}`, header: http.Header{"X-Echo": {"Bearer " + fixtureAccess}}, released: true, wantErr: true},
		{name: "minted key", url: "https://api.anthropic.com/api/oauth/claude_cli/client_data", contentType: "application/json", body: `{"key":"sk-ant-api03-minted"}`, released: true, wantErr: true},
		{name: "unexpected token response", url: "https://claude.ai/api/session", contentType: "application/json", body: `{"access_token":"new-format"}`, wantErr: true},
		{name: "compressed structured response", url: "https://api.anthropic.com/api/oauth/usage", contentType: "application/json", body: `{}`, header: http.Header{"Content-Encoding": {"gzip"}}, released: true, wantErr: true},
		{name: "model output", url: "https://api.anthropic.com/v1/messages", contentType: "text/event-stream", body: `data: {"text":"use sk-ant-api03-example and \"access_token\""}` + "\n\n", released: true},
		{name: "web page", url: "https://code.claude.com/docs/en/overview", contentType: "text/html", body: "<code>sk-ant-api03-example</code>"},
		{name: "account data", url: "https://api.anthropic.com/api/oauth/profile", contentType: "application/json", body: `{"account":{"email":"fixture@example.test"}}`, released: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.released {
				req.Header.Set("Authorization", "Bearer "+b.registry.Access)
			}
			response, err := fixtureRoundTrip(b, req, fixtureTransport(func(*http.Request) (*http.Response, error) {
				upstream := rawFixtureResponse([]byte(tc.body))
				upstream.Header.Set("Content-Type", tc.contentType)
				for key, values := range tc.header {
					upstream.Header[key] = values
				}
				return upstream, nil
			}))
			var body string
			if err == nil {
				body, err = readAllChunks(response.Body)
				_ = response.Body.Close()
			}
			if tc.wantErr {
				if !errors.Is(err, ErrResponse) {
					t.Fatalf("credential-bearing response delivered: %q, %v", body, err)
				}
				if strings.Contains(body, fixtureAccess) || strings.Contains(body, fixtureRefresh) || strings.Contains(body, "minted") || strings.Contains(body, "new-format") {
					t.Fatalf("credential bytes escaped before the tripwire: %q", body)
				}
				return
			}
			if err != nil || body != tc.body {
				t.Fatalf("ordinary response = %q, %v", body, err)
			}
		})
	}
}

type chunkedBody struct{ chunks chan []byte }

func (c chunkedBody) Read(p []byte) (int, error) {
	chunk, ok := <-c.chunks
	if !ok {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}

func (chunkedBody) Close() error { return nil }

func TestTripwireStreamsEventsWithoutDelayAndStopsBeforeSecret(t *testing.T) {
	secret := "synthetic-secret-value"
	body := chunkedBody{chunks: make(chan []byte, 1)}
	tw := &tripwire{body: body, patterns: [][]byte{[]byte(secret)}}
	buffer := make([]byte, 128)

	body.chunks <- []byte("data: one\n\n")
	if n, err := tw.Read(buffer); err != nil || string(buffer[:n]) != "data: one\n\n" {
		t.Fatalf("complete event was held back: %q, %v", buffer[:n], err)
	}
	body.chunks <- []byte("data: synthetic-sec")
	if n, err := tw.Read(buffer); err != nil || string(buffer[:n]) != "data: " {
		t.Fatalf("partial secret was not held back: %q, %v", buffer[:n], err)
	}
	body.chunks <- []byte("ret-value\n\n")
	if n, err := tw.Read(buffer); !errors.Is(err, ErrResponse) || n != 0 {
		t.Fatalf("secret split across chunks escaped: %q, %v", buffer[:n], err)
	}
}

type cancelledBody struct{ context.Context }

func (r cancelledBody) Read([]byte) (int, error) { <-r.Done(); return 0, r.Err() }
func (cancelledBody) Close() error               { return nil }

func TestCaptureHasWholeExchangeDeadlineAndFailsClosedOnBodyCancellation(t *testing.T) {
	b := fixtureBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := loginRequest(t).WithContext(ctx)
	response, err := fixtureRoundTrip(b, req, fixtureTransport(func(outgoing *http.Request) (*http.Response, error) {
		deadline, ok := outgoing.Context().Deadline()
		if !ok || time.Until(deadline) > exchangeTimeout || time.Until(deadline) < time.Minute {
			t.Error("capture lacks bounded whole-exchange deadline")
		}
		cancel()
		upstream := rawFixtureResponse(nil)
		upstream.Body = cancelledBody{Context: outgoing.Context()}
		upstream.ContentLength = -1
		return upstream, nil
	}))
	if response != nil || !errors.Is(err, ErrResponse) {
		t.Fatalf("body cancellation = %v, %v", response, err)
	}
	stored, err := b.readGrant()
	if err != nil || stored.Status != "uncertain" {
		t.Fatalf("cancelled capture state = %+v, %v", stored, err)
	}
}

func TestSubscriptionScopeDoesNotPermitAPIKeyCreation(t *testing.T) {
	b := fixtureBroker(t)
	token := fixtureToken()
	token.Scope = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers"
	response, err := fixtureRoundTrip(b, loginRequest(t), fixtureTransport(func(*http.Request) (*http.Response, error) { return fixtureResponse(token), nil }))
	if err != nil {
		t.Fatal(err)
	}
	projected := responseToken(t, response)
	request, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/api/oauth/claude_cli/create_api_key", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+projected.Access)
	response, err = fixtureRoundTrip(b, request, fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Error("API key creation is outside the protected subscription adapter")
		return nil, io.EOF
	}))
	if response != nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("API key creation response=%v error=%v", response, err)
	}
}

func TestBrokerLockFailuresUseStateSentinelAndPreserveCallbackErrors(t *testing.T) {
	for _, github := range []bool{false, true} {
		t.Run(strconv.FormatBool(github), func(t *testing.T) {
			b := fixtureBroker(t)
			lockPath, locked := b.config.LockPath, b.locked
			if github {
				b = fixtureGitHub(t, githubNativeFixture())
				lockPath, locked = b.github.config.LockPath, b.github.locked
			}
			for _, want := range []error{nil, ErrDenied, ErrResponse, ErrUncertain} {
				if err := locked(func() error { return want }); !errors.Is(err, want) {
					t.Fatalf("callback error changed: %v, want %v", err, want)
				}
			}
			if err := os.Remove(lockPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(lockPath, 0o700); err != nil {
				t.Fatal(err)
			}
			called := false
			err := locked(func() error { called = true; return nil })
			if !errors.Is(err, ErrState) || called {
				t.Fatalf("lock failure escaped state boundary: %v, called=%t", err, called)
			}
		})
	}
}

func TestRuleReleasedSecretsPassButAreGuarded(t *testing.T) {
	b := fixtureBroker(t)
	enroll(t, b)
	const secret = "sk-ant-oat01-host-configured-long-lived-token"
	for _, tc := range []struct {
		name, url, body string
		extraHandle     bool
		wantErr         error
	}{
		{name: "first-party bearer", url: "https://api.anthropic.com/v1/messages", body: `{"ok":true}`},
		{name: "first-party reflection", url: "https://api.anthropic.com/v1/messages", body: "data: " + secret, wantErr: ErrResponse},
		{name: "other host reflection", url: "https://api.example.test/v1/echo", body: secret, wantErr: ErrResponse},
		{name: "other host structured data", url: "https://api.example.test/v1/data", body: `{"access_token":"theirs"}`},
		{name: "handle beside released secret", url: "https://api.anthropic.com/v1/messages", body: `{}`, extraHandle: true, wantErr: ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+secret)
			if tc.extraHandle {
				req.Header.Set("X-Other", b.registry.Access)
			}
			req = req.WithContext(WithReleasedSecrets(req.Context(), []string{secret}))
			response, err := fixtureRoundTrip(b, req, fixtureTransport(func(outgoing *http.Request) (*http.Response, error) {
				if outgoing.Header.Get("Authorization") != "Bearer "+secret {
					t.Error("released secret was altered")
				}
				return rawFixtureResponse([]byte(tc.body)), nil
			}))
			var body string
			if err == nil {
				body, err = readAllChunks(response.Body)
				_ = response.Body.Close()
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || strings.Contains(body, secret) {
					t.Fatalf("released secret not guarded: %q, %v", body, err)
				}
				return
			}
			if err != nil || body != tc.body {
				t.Fatalf("released request = %q, %v", body, err)
			}
		})
	}
}
