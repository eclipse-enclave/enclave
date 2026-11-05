// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCopilotCLIEntitlementOverHTTPProtocols(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		for _, declared := range []bool{false, true} {
			t.Run("http"+strconv.Itoa(protocol)+"/declared="+strconv.FormatBool(declared), func(t *testing.T) {
				broker := fixtureToolBroker(t, "copilot")
				response, err := captureCopilotCLI(t, broker, map[string]any{"login": "engine-check"})
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				credential := broker.copilot.Token
				expected := fixtureCopilotToken
				if declared {
					credential = "github_pat_protocol_fixture"
					expected = credential
				}
				var calls atomic.Int32
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.ProtoMajor != protocol {
						t.Errorf("server protocol = %d, want %d", req.ProtoMajor, protocol)
					}
					t.Logf("incoming HTTP/%d contentLength=%d body=%T", req.ProtoMajor, req.ContentLength, req.Body)
					req.URL.Scheme, req.URL.Host, req.Host = "https", githubAPIHost, githubAPIHost
					if declared {
						req = req.WithContext(WithReleasedSecrets(req.Context(), []string{credential}))
					}
					upstream, err := fixtureRoundTrip(broker, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
						calls.Add(1)
						if out.Header.Get("Authorization") != "Bearer "+expected {
							t.Error("upstream did not receive the selected private credential")
						}
						response := fixtureResponse(map[string]any{"login": "engine-check", "future_field": "omit"})
						response.Header.Set("Set-Cookie", "ignored=value")
						return response, nil
					}))
					if err != nil {
						http.Error(w, "denied", http.StatusForbidden)
						return
					}
					defer func() { _ = upstream.Body.Close() }()
					for key, values := range upstream.Header {
						for _, value := range values {
							w.Header().Add(key, value)
						}
					}
					w.WriteHeader(upstream.StatusCode)
					_, _ = io.Copy(w, upstream.Body)
				}))
				server.EnableHTTP2 = protocol == 2
				server.StartTLS()
				defer server.Close()
				client := server.Client()
				client.Timeout = 5 * time.Second
				for _, test := range []struct {
					method, path, body string
					allowed            bool
				}{
					{http.MethodGet, copilotUserPath, "", true},
					{http.MethodGet, copilotUserPath, "x", false},
					{http.MethodPost, copilotUserPath, "", false},
					{http.MethodGet, copilotUserPath + "?extra=x", "", false},
					{http.MethodGet, "/repos/acme/private", "", false},
					{http.MethodPost, "/graphql", "", false},
				} {
					// Explicitly declared host keys retain their own release rules;
					// unrelated API-path refusals here concern captured handles.
					if declared && !strings.HasPrefix(test.path, copilotUserPath) {
						continue
					}
					t.Run(test.method+test.path+"/body="+test.body, func(t *testing.T) {
						before := calls.Load()
						req, err := http.NewRequest(test.method, server.URL+test.path, strings.NewReader(test.body))
						if err != nil {
							t.Fatal(err)
						}
						req.Header.Set("Authorization", "Bearer "+credential)
						response, err := client.Do(req)
						if err != nil {
							t.Fatal(err)
						}
						defer func() { _ = response.Body.Close() }()
						data, err := io.ReadAll(response.Body)
						if err != nil {
							t.Fatal(err)
						}
						if response.ProtoMajor != protocol {
							t.Fatalf("client protocol = %d, want %d", response.ProtoMajor, protocol)
						}
						if test.allowed {
							if response.StatusCode != http.StatusOK || calls.Load() != before+1 {
								t.Fatalf("empty entitlement GET: status=%d, dispatched=%d", response.StatusCode, calls.Load()-before)
							}
							if strings.Contains(string(data), "future_field") || response.Header.Get("Set-Cookie") != "" {
								t.Fatal("entitlement metadata was not sanitized")
							}
						} else if response.StatusCode != http.StatusForbidden || calls.Load() != before {
							t.Fatalf("unapproved request: status=%d, dispatched=%d", response.StatusCode, calls.Load()-before)
						}
					})
				}
			})
		}
	}
}

type copilotBodyReadFunc func([]byte) (int, error)

func (f copilotBodyReadFunc) Read(data []byte) (int, error) { return f(data) }

func TestCopilotCLIEntitlementRequiresBodyEOF(t *testing.T) {
	for _, name := range []string{"empty EOF", "undeclared byte", "read error", "no EOF", "late trailer"} {
		t.Run(name, func(t *testing.T) {
			broker := fixtureToolBroker(t, "copilot")
			response, err := captureCopilotCLI(t, broker, map[string]any{"login": "engine-check"})
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			req, err := http.NewRequest(http.MethodGet, "https://api.github.com"+copilotUserPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+broker.copilot.Token)
			req.Body = io.NopCloser(copilotBodyReadFunc(func(data []byte) (int, error) {
				switch name {
				case "undeclared byte":
					return copy(data, "x"), io.EOF
				case "read error":
					return 0, io.ErrUnexpectedEOF
				case "no EOF":
					return 0, nil
				case "late trailer":
					req.Trailer = http.Header{"X-Extra": {"value"}}
				}
				return 0, io.EOF
			}))
			called := false
			response, err = fixtureRoundTrip(broker, req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
				called = true
				if out.Body != http.NoBody {
					t.Error("verified empty reader was not normalized before dispatch")
				}
				return fixtureResponse(map[string]any{"login": "engine-check"}), nil
			}))
			if name == "empty EOF" {
				if err != nil || !called {
					t.Fatalf("verified empty request rejected: dispatched=%t err=%v", called, err)
				}
			} else if !errors.Is(err, ErrDenied) || called {
				t.Fatalf("unverified body accepted: dispatched=%t err=%v", called, err)
			}
			if response != nil {
				_ = response.Body.Close()
			}
		})
	}
}

func TestCopilotCLILeavesOtherCredentialsBodyUntouched(t *testing.T) {
	broker := fixtureToolBroker(t, "copilot")
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com"+copilotUserPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer another-adapters-handle")
	read := false
	req.Body = io.NopCloser(copilotBodyReadFunc(func([]byte) (int, error) {
		read = true
		return 0, io.EOF
	}))
	_, handled, err := (copilotAdapter{b: broker}).roundTrip(req, nil, nil)
	if err != nil || handled || read {
		t.Fatalf("another adapter's input was changed: handled=%t read=%t err=%v", handled, read, err)
	}
}
