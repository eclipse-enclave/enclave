// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"enclave/internal/util"
)

func siblingBroker(t *testing.T, b *Broker, session string) *Broker {
	t.Helper()
	config := b.config
	config.RuntimeDir = fixtureViewDirectory(t)
	config.Session, config.Incarnation = session, session
	if config.GitHub != nil {
		github := *config.GitHub
		github.RuntimeDir, github.ViewDir = fixtureViewDirectory(t), fixtureViewDirectory(t)
		github.Session, github.Incarnation = session, session
		config.GitHub = &github
	}
	sibling, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	sibling.now = b.now
	return sibling
}

func TestConcurrentSessionsShareRefreshButNeverHandles(t *testing.T) {
	first := fixtureBroker(t)
	enroll(t, first)
	advanceToRefresh(first)
	brokers := []*Broker{first}
	for i := range 7 {
		brokers = append(brokers, siblingBroker(t, first, "session-"+strconv.Itoa(i)))
	}
	handles := map[string]bool{}
	for _, b := range brokers {
		for _, handle := range []string{b.registry.Access, b.registry.Refresh} {
			if handles[handle] {
				t.Fatal("sessions reused a handle")
			}
			handles[handle] = true
		}
		if err := b.InitializeView(fixtureViewDirectory(t)); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	for generation := 2; generation <= 3; generation++ {
		wantRefresh := fixtureRefresh
		if generation == 3 {
			wantRefresh = rotatedRefresh
			for _, b := range brokers {
				advanceToRefresh(b)
			}
		}
		transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			var input tokenRequest
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				return nil, err
			}
			if input.Refresh != wantRefresh {
				t.Errorf("shared refresh used stale token: %q", input.Refresh)
			}
			token := fixtureToken()
			token.Access, token.Refresh = rotatedAccess, rotatedRefresh
			return fixtureResponse(token), nil
		})
		var wg sync.WaitGroup
		responses := make([]*http.Response, len(brokers))
		for i, b := range brokers {
			req := refreshRequest(t, b)
			wg.Add(1)
			go func() {
				defer wg.Done()
				response, err := fixtureRoundTrip(b, req, transport)
				if err != nil {
					t.Errorf("concurrent session refresh: %v", err)
					return
				}
				responses[i] = response
			}()
		}
		wg.Wait()
		for i, response := range responses {
			if response == nil {
				t.Fatal("concurrent refresh returned no response")
			}
			token := responseToken(t, response)
			if token.Access != brokers[i].registry.Access || token.Refresh != brokers[i].registry.Refresh {
				t.Error("refresh projected another session's handles")
			}
		}
		if calls.Load() != int32(generation-1) {
			t.Fatalf("provider calls = %d, want %d", calls.Load(), generation-1)
		}
		for _, b := range brokers {
			g, err := b.readGrant()
			if err != nil || g.Generation != uint64(generation) {
				t.Fatalf("session did not see shared generation: %+v, %v", g, err)
			}
			assertClaudeAccess(t, b, b.registry.Access, rotatedAccess, nil)
			foreign := brokers[0]
			if foreign == b {
				foreign = brokers[1]
			}
			assertClaudeAccess(t, b, foreign.registry.Access, "", ErrDenied)
			if response, err := fixtureRoundTrip(b, refreshRequest(t, foreign), transport); response != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("cross-session refresh handle accepted: %v", err)
			}
		}
	}

	// Ending one session removes only its registry; siblings remain usable,
	// including after a proxy reload that must not roll the shared grant back.
	if err := os.RemoveAll(first.config.RuntimeDir); err != nil {
		t.Fatal(err)
	}
	for _, b := range brokers[1:] {
		reloaded, err := Open(b.config)
		if err != nil {
			t.Fatal(err)
		}
		reloaded.now = b.now
		if reloaded.registry.Access != b.registry.Access || reloaded.registry.Refresh != b.registry.Refresh {
			t.Fatal("sibling teardown or reload changed handles")
		}
		assertClaudeAccess(t, reloaded, b.registry.Access, rotatedAccess, nil)
		assertClaudeAccess(t, reloaded, first.registry.Access, "", ErrDenied)
	}
}

func assertClaudeAccess(t *testing.T, b *Broker, handle, token string, wantErr error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+handle)
	response, err := fixtureRoundTrip(b, req, fixtureTransport(func(outgoing *http.Request) (*http.Response, error) {
		if wantErr != nil || outgoing.Header.Get("Authorization") != "Bearer "+token {
			t.Error("incorrect shared access credential release")
		}
		return rawFixtureResponse(nil), nil
	}))
	if !errors.Is(err, wantErr) {
		t.Fatalf("access error = %v, want %v", err, wantErr)
	}
	if response != nil {
		_ = response.Body.Close()
	}
}

func TestSiblingRefreshRecoversSharedCommitFailuresWithoutReload(t *testing.T) {
	for _, failAt := range []string{"before-intent", "after-intent", "before-commit", "after-commit"} {
		t.Run(failAt, func(t *testing.T) {
			b := fixtureBroker(t)
			enroll(t, b)
			advanceToRefresh(b)
			sibling := siblingBroker(t, b, "sibling")
			b.write = func(path string, value any) error {
				boundary := "intent"
				if value.(*grant).Status == "ready" {
					boundary = "commit"
				}
				if failAt == "before-"+boundary {
					return errors.New("injected write failure")
				}
				if failAt == "after-"+boundary {
					data, err := json.Marshal(value)
					if err != nil {
						return err
					}
					// Publish a synced file without syncing the rename. The live
					// sibling must supply the missing directory durability fence.
					if err := util.WriteFileAtomic(path, data, 0o600); err != nil {
						return err
					}
					return errors.New("injected directory sync failure")
				}
				return durableJSON(path, value)
			}
			calls := 0
			transport := fixtureTransport(func(*http.Request) (*http.Response, error) {
				calls++
				token := fixtureToken()
				token.Access, token.Refresh = rotatedAccess, rotatedRefresh
				return fixtureResponse(token), nil
			})
			if _, err := fixtureRoundTrip(b, refreshRequest(t, b), transport); !errors.Is(err, ErrState) {
				t.Fatalf("failed writer: %v", err)
			}
			response, err := fixtureRoundTrip(sibling, refreshRequest(t, sibling), transport)
			if failAt == "before-intent" || failAt == "after-commit" {
				if err != nil || calls != 1 {
					t.Fatalf("sibling recovery: %v, calls=%d", err, calls)
				}
				if responseToken(t, response).Access != sibling.registry.Access {
					t.Fatal("recovery returned another session's handle")
				}
				assertClaudeAccess(t, sibling, sibling.registry.Access, rotatedAccess, nil)
			} else {
				if !errors.Is(err, ErrUncertain) || response != nil {
					t.Fatalf("sibling used uncertain grant: %v", err)
				}
				wantCalls := 0
				if failAt == "before-commit" {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatal("sibling replayed uncertain refresh")
				}
				assertClaudeAccess(t, sibling, sibling.registry.Access, "", ErrUncertain)
			}
		})
	}
}

func TestGitHubConcurrentSessionsPreserveAccountsAndIsolateDeviceReplay(t *testing.T) {
	first := fixtureGitHub(t, githubNativeFixture())
	second := siblingBroker(t, first, "second")
	for _, b := range []*Broker{first, second} {
		if err := b.InitializeView(fixtureViewDirectory(t)); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	users := []string{"carol", "dave"}
	tokens := []string{githubRealAlice, githubRealBob}
	responses := make([]*http.Response, 2)
	for i, b := range []*Broker{first, second} {
		req := githubRequest(t, githubTokenPath, users[i])
		transport := githubSuccessTransport(t, b, &calls, users[i], tokens[i])
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := fixtureRoundTrip(b, req, transport)
			if err != nil {
				t.Errorf("concurrent device login: %v", err)
				return
			}
			responses[i] = response
		}()
	}
	wg.Wait()
	for i, b := range []*Broker{first, second} {
		body := githubSafeBody(t, responses[i])
		if !strings.Contains(body, b.github.registry.Handles[users[i]]) {
			t.Error("login did not return own session handle")
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("device/profile dispatches = %d, want four", calls.Load())
	}
	for i, b := range []*Broker{first, second} {
		g, err := b.github.readGrant()
		if err != nil || len(g.Accounts) != 4 || len(g.Devices) != 2 {
			t.Fatalf("concurrent login lost shared state: %+v, %v", g, err)
		}
		if first.github.registry.Handles["alice"] == second.github.registry.Handles["alice"] {
			t.Fatal("GitHub sessions share handles")
		}
		foreignCode := "dave"
		if i == 1 {
			foreignCode = "carol"
		}
		denyTransport := fixtureTransport(func(*http.Request) (*http.Response, error) {
			t.Error("foreign device replay reached upstream")
			return nil, ErrDenied
		})
		if response, err := fixtureRoundTrip(b, githubRequest(t, githubTokenPath, foreignCode), denyTransport); response != nil || !errors.Is(err, ErrDenied) {
			t.Fatalf("foreign device replay accepted: %v", err)
		}
	}

	// Reauthentication replaces a shared account token, not sibling handles or
	// their local account selection. Both gateways must read the new grant.
	response, err := fixtureRoundTrip(second, githubRequest(t, githubTokenPath, "rotate-alice"), githubSuccessTransport(t, second, &calls, "alice", githubRealBob))
	if err != nil {
		t.Fatal(err)
	}
	githubSafeBody(t, response)
	for _, b := range []*Broker{first, second} {
		assertGitHubAccess(t, b, b.github.registry.Handles["alice"], githubRealBob, nil)
		other := first
		if b == first {
			other = second
		}
		assertGitHubAccess(t, b, other.github.registry.Handles["alice"], "", ErrDenied)
	}
	if err := os.RemoveAll(first.github.config.RuntimeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(first.github.config.ViewDir); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(second.config)
	if err != nil {
		t.Fatal(err)
	}
	assertGitHubAccess(t, reloaded, second.github.registry.Handles["alice"], githubRealBob, nil)
	if _, err := os.Stat(filepath.Join(second.github.config.ViewDir, "hosts.yml")); err != nil {
		t.Fatalf("sibling view disappeared: %v", err)
	}
}

func assertGitHubAccess(t *testing.T, b *Broker, handle, token string, wantErr error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+handle)
	response, err := fixtureRoundTrip(b, req, fixtureTransport(func(outgoing *http.Request) (*http.Response, error) {
		if wantErr != nil || outgoing.Header.Get("Authorization") != "Bearer "+token {
			t.Error("incorrect shared GitHub credential release")
		}
		return rawFixtureResponse(nil), nil
	}))
	if !errors.Is(err, wantErr) {
		t.Fatalf("GitHub access error = %v, want %v", err, wantErr)
	}
	if response != nil {
		_ = response.Body.Close()
	}
}

func TestGitHubSiblingRecoversVisibleCommitWithoutReload(t *testing.T) {
	first := fixtureGitHub(t, githubNativeFixture())
	second := siblingBroker(t, first, "second")
	first.github.write = func(path string, value any) error {
		if g, ok := value.(*githubGrant); ok && g.Accounts["alice"].Token == githubRealBob {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if err := util.WriteFileAtomic(path, data, 0o600); err != nil {
				return err
			}
			return errors.New("injected directory sync failure")
		}
		return durableJSON(path, value)
	}
	var calls atomic.Int32
	if response, err := fixtureRoundTrip(first, githubRequest(t, githubTokenPath, "rotate-alice"), githubSuccessTransport(t, first, &calls, "alice", githubRealBob)); response != nil || !errors.Is(err, ErrState) {
		t.Fatalf("failed GitHub commit returned a token: %v", err)
	}
	assertGitHubAccess(t, first, first.github.registry.Handles["alice"], "", ErrState)
	assertGitHubAccess(t, second, second.github.registry.Handles["alice"], githubRealBob, nil)
}

func TestSharedGrantReadRefusesFailedDurabilityFence(t *testing.T) {
	if os.Getuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("requires Unix directory permissions and an unprivileged user")
	}
	for _, github := range []bool{false, true} {
		t.Run(strconv.FormatBool(github), func(t *testing.T) {
			first := fixtureBroker(t)
			enroll(t, first)
			dir, file := first.config.StoreDir, grantFile
			if github {
				first = fixtureGitHub(t, githubNativeFixture())
				dir, file = first.github.config.StoreDir, githubGrantFile
			}
			second := siblingBroker(t, first, "second")
			// Search permission permits reading the grant, but opening its
			// directory for the fsync fence must fail.
			if err := os.Chmod(dir, 0o100); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chmod(dir, 0o700) }()
			if _, err := os.ReadFile(filepath.Join(dir, file)); err != nil {
				t.Fatalf("fixture grant must remain readable: %v", err)
			}
			if github {
				assertGitHubAccess(t, second, second.github.registry.Handles["alice"], "", ErrState)
			} else {
				assertClaudeAccess(t, second, second.registry.Access, "", ErrState)
			}
		})
	}
}

func TestSharedClaudeReauthenticationPreservesAccountAndSiblingHandles(t *testing.T) {
	first := fixtureBroker(t)
	enroll(t, first)
	advanceToRefresh(first)
	second := siblingBroker(t, first, "second")
	if _, err := fixtureRoundTrip(first, refreshRequest(t, first), fixtureTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("lost refresh result")
	})); !errors.Is(err, ErrUncertain) {
		t.Fatalf("refresh uncertainty: %v", err)
	}
	assertClaudeAccess(t, second, second.registry.Access, "", ErrUncertain)
	wrongAccount := fixtureToken()
	wrongAccount.Account.UUID = "33333333-3333-4333-8333-333333333333"
	if _, err := fixtureRoundTrip(second, loginRequest(t), fixtureTransport(func(*http.Request) (*http.Response, error) {
		return fixtureResponse(wrongAccount), nil
	})); !errors.Is(err, ErrResponse) {
		t.Fatalf("reauthentication switched sibling account: %v", err)
	}
	assertClaudeAccess(t, first, first.registry.Access, "", ErrUncertain)
	response, err := fixtureRoundTrip(second, loginRequest(t), fixtureTransport(func(*http.Request) (*http.Response, error) {
		token := fixtureToken()
		token.Access, token.Refresh = rotatedAccess, rotatedRefresh
		return fixtureResponse(token), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if responseToken(t, response).Access != second.registry.Access {
		t.Fatal("reauthentication changed the session handle")
	}
	for _, b := range []*Broker{first, second} {
		assertClaudeAccess(t, b, b.registry.Access, rotatedAccess, nil)
	}
}

func TestConcurrentInitialClaudeLoginsCannotReplaceReadyGrant(t *testing.T) {
	first := fixtureBroker(t)
	second := siblingBroker(t, first, "second")
	enroll(t, first)
	calls := 0
	transport := fixtureTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return fixtureResponse(fixtureToken()), nil
	})
	if response, err := fixtureRoundTrip(second, loginRequest(t), transport); response != nil || !errors.Is(err, ErrDenied) || calls != 0 {
		t.Fatalf("second bootstrap replaced shared grant: %v, calls=%d", err, calls)
	}
	assertClaudeAccess(t, second, second.registry.Access, fixtureAccess, nil)
}
