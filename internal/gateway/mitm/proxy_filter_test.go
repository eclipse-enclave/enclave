// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/filter/engine"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
	"enclave/internal/netcapture"
	"enclave/internal/netlog"
)

const messageURL = "https://" + gmailHost + "/gmail/v1/users/me/messages/"

// I2, I3: an allowed request is intercepted in the default coarse log mode,
// reaches the API with the real token in place of the placeholder, and is
// logged with its filter, operation, and capability.
func TestFilterAllowedRequestGetsRealToken(t *testing.T) {
	h := newFilterHarness(t, "readonly")

	got := h.do(t, http.MethodGet, messageURL+"M1?format=full", "", withPlaceholder())

	if got.status != http.StatusOK || !strings.Contains(got.body, upstreamMarker) {
		t.Fatalf("response = %d %q, want the upstream response", got.status, got.body)
	}
	requests := h.google.apiRequests()
	if len(requests) != 1 {
		t.Fatalf("upstream requests = %+v, want 1", requests)
	}
	if want := "Bearer " + filterAccessToken + "-1"; requests[0].Authorization != want {
		t.Fatalf("upstream Authorization = %q, want %q", requests[0].Authorization, want)
	}
	if requests[0].Host != gmailHost || requests[0].Path != "/gmail/v1/users/me/messages/M1" || requests[0].RawQuery != "format=full" {
		t.Fatalf("upstream request = %+v", requests[0])
	}
	h.waitHTTPEvents(t, 1)
	events := readAuditEvents(t, h.logPath)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want a tcp dispatch and an http event", events)
	}
	if dispatch := events[0]; dispatch.Type != netlog.TypeTCP || dispatch.Rule != "mitm-dispatch" || dispatch.Domain != gmailHost {
		t.Fatalf("first event = %+v, want tcp mitm-dispatch for %s in coarse mode", dispatch, gmailHost)
	}
	event := events[1]
	if event.Verdict != netlog.VerdictPass || event.Rule != netlog.RuleFilter || event.Filter != "gws" ||
		event.Op != "gmail.messages.get" || event.Capability != "mail.read" || event.Reason != "" ||
		event.Status != http.StatusOK || event.Domain != gmailHost {
		t.Fatalf("http event = %+v, want a filter pass for gmail.messages.get", event)
	}
	assertNoCredentialMaterial(t, "response", got.body)
	h.assertLogClean(t)
}

// I2: requests without a matching route, or whose capability the preset does
// not enable, never reach the API.
func TestFilterDeniedRequestsNeverReachUpstream(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	for i, tc := range []struct {
		name, method, url, body string
		reason                  string
	}{
		{name: "no route", method: http.MethodGet, url: "https://" + gmailHost + "/gmail/v1/users/me/settings/filters", reason: engine.ReasonNoRoute},
		{name: "capability not granted", method: http.MethodPost, url: messageURL + "send", body: `{"raw":"eA"}`, reason: engine.ReasonCapability},
		{name: "deny route", method: http.MethodPost, url: "https://" + googleAPIHost + "/drive/v3/files/F1/permissions", body: `{}`, reason: engine.ReasonDenyRoute},
		{name: "query condition", method: http.MethodGet, url: messageURL + "M1?format=metadata&format=full", reason: engine.ReasonNoRoute},
		{name: "encoded slash", method: http.MethodGet, url: messageURL + "a%2Fb", reason: engine.ReasonAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := h.do(t, tc.method, tc.url, tc.body, withPlaceholder(), header("Content-Type", "application/json"))
			assertFilterDenial(t, got, h.lastHTTPEvent(t, i+1), http.StatusForbidden, tc.reason)
			if !strings.Contains(got.body, `"PERMISSION_DENIED"`) || got.header.Get("Content-Type") != "application/json; charset=UTF-8" {
				t.Fatalf("deny response = %v %q, want the filter's JSON deny body", got.header, got.body)
			}
		})
	}
	if requests := h.google.apiRequests(); len(requests) != 0 {
		t.Fatalf("upstream requests = %+v, want none", requests)
	}
	if h.google.refreshCount() != 0 {
		t.Fatal("a denied request minted a token")
	}
	h.assertLogClean(t)
}

// I4, I5, I11: a foreign credential, a denied header or parameter, the
// placeholder outside the injection header, and method overrides are denied.
func TestFilterDeniesForeignCredentialsAndPlaceholderMisuse(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	for i, tc := range []struct {
		name   string
		url    string
		opts   []requestOption
		reason string
	}{
		{name: "foreign bearer token", url: messageURL + "M1", opts: []requestOption{header("Authorization", "Bearer ya29.attacker-token")}, reason: engine.ReasonForeignCredential},
		{name: "api key header", url: messageURL + "M1", opts: []requestOption{withPlaceholder(), header("X-Goog-Api-Key", "AIzaAttacker")}, reason: engine.ReasonForeignCredential},
		{name: "api key parameter", url: messageURL + "M1?key=AIzaAttacker", opts: []requestOption{withPlaceholder()}, reason: engine.ReasonForeignCredential},
		{name: "access token parameter", url: messageURL + "M1?access_token=ya29.attacker", reason: engine.ReasonForeignCredential},
		{name: "placeholder in another header", url: messageURL + "M1", opts: []requestOption{withPlaceholder(), header("X-Debug", filterPlaceholder)}, reason: engine.ReasonPlaceholder},
		{name: "placeholder in the URL", url: messageURL + "M1?q=" + filterPlaceholder, opts: []requestOption{withPlaceholder()}, reason: engine.ReasonPlaceholder},
		{name: "missing placeholder", url: messageURL + "M1", reason: engine.ReasonMissingCredential},
		{name: "method override", url: messageURL + "M1", opts: []requestOption{withPlaceholder(), header("X-HTTP-Method-Override", "DELETE")}, reason: engine.ReasonAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := h.do(t, http.MethodGet, tc.url, "", tc.opts...)
			assertFilterDenial(t, got, h.lastHTTPEvent(t, i+1), http.StatusForbidden, tc.reason)
		})
	}
	if requests := h.google.apiRequests(); len(requests) != 0 {
		t.Fatalf("upstream requests = %+v, want none", requests)
	}
	h.assertLogClean(t)
}

// I3: a public route forwards without any credential, so the Authorization
// the client sent is removed and no token is minted.
func TestFilterPublicRouteStripsAuthorization(t *testing.T) {
	h := newFilterHarness(t, "readonly")

	got := h.do(t, http.MethodGet, "https://"+googleAPIHost+"/discovery/v1/apis/gmail/v1/rest", "", withPlaceholder())

	if got.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%q)", got.status, got.body)
	}
	requests := h.google.apiRequests()
	if len(requests) != 1 || requests[0].Authorization != "" {
		t.Fatalf("upstream requests = %+v, want one without Authorization", requests)
	}
	if h.google.refreshCount() != 0 {
		t.Fatal("a public route minted a token")
	}
	if event := h.lastHTTPEvent(t, 1); event.Capability != "discovery" || event.Verdict != netlog.VerdictPass {
		t.Fatalf("event = %+v, want a discovery pass", event)
	}
}

// I7: a response whose counted content would exceed the quota is withheld:
// the client gets the quota denial and none of the upstream body.
func TestFilterWithholdsResponseOverQuota(t *testing.T) {
	h := newFilterHarness(t, "quota", withUserPreset("quota", "allow:\n  mail.read: {}\nquotas:\n  mail.read: 2\n"))
	h.google.setRespond(func(w http.ResponseWriter, r *http.Request, _ string) {
		ids := map[string][]string{
			"/gmail/v1/users/me/threads/T3": {"M1", "M2", "M3"},
			"/gmail/v1/users/me/threads/T2": {"M1", "M2"},
		}[r.URL.Path]
		messages := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			messages = append(messages, map[string]string{"id": id, "snippet": upstreamMarker})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "T", "messages": messages})
	})
	threadURL := "https://" + gmailHost + "/gmail/v1/users/me/threads/"

	withheld := h.do(t, http.MethodGet, threadURL+"T3", "", withPlaceholder())
	assertFilterDenial(t, withheld, h.lastHTTPEvent(t, 1), http.StatusForbidden, engine.ReasonQuota)
	if strings.Contains(withheld.body, upstreamMarker) || !strings.Contains(withheld.body, "RESOURCE_EXHAUSTED") {
		t.Fatalf("withheld response = %q, want the quota denial without upstream content", withheld.body)
	}
	if event := h.lastHTTPEvent(t, 1); event.Op != "gmail.threads.get" || event.ResponseSize != 0 {
		t.Fatalf("event = %+v, want gmail.threads.get without a relayed size", event)
	}

	relayed := h.do(t, http.MethodGet, threadURL+"T2", "", withPlaceholder())
	if relayed.status != http.StatusOK || !strings.Contains(relayed.body, upstreamMarker) {
		t.Fatalf("response = %d %q, want the thread within quota", relayed.status, relayed.body)
	}
	if relayed.header.Get("Content-Encoding") != "" || relayed.header.Get("Content-Length") != fmt.Sprint(len(relayed.body)) {
		t.Fatalf("inspected response headers = %v, want an exact Content-Length and no encoding", relayed.header)
	}
	h.waitHTTPEvents(t, 2)

	// M1 was counted with T2, M9 would be a third message.
	if again := h.do(t, http.MethodGet, messageURL+"M1", "", withPlaceholder()); again.status != http.StatusOK {
		t.Fatalf("re-reading a counted message = %d, want 200", again.status)
	}
	h.waitHTTPEvents(t, 3)
	over := h.do(t, http.MethodGet, messageURL+"M9", "", withPlaceholder())
	assertFilterDenial(t, over, h.lastHTTPEvent(t, 4), http.StatusForbidden, engine.ReasonQuota)
	if n := len(h.google.apiRequests()); n != 3 {
		t.Fatalf("upstream requests = %d, want 3 (the request quota denial never leaves the gateway)", n)
	}
	h.assertLogClean(t)
}

// Under full capture a governed request is recorded with the placeholder the
// session sent, never the token the filter injected. A withheld response was
// still relayed upstream, so it keeps its capture, without the response the
// client never got.
func TestFilterFullCaptureRecordsPlaceholderAndWithheldResponse(t *testing.T) {
	h := newFilterHarness(t, "quota", withUserPreset("quota", "allow:\n  mail.read: {}\nquotas:\n  mail.read: 2\n"))
	dir := t.TempDir()
	captures, err := netcapture.Open(netcapture.Options{Dir: dir, Session: testSession})
	if err != nil {
		t.Fatal(err)
	}
	h.proxy.captures = captures
	h.google.setRespond(func(w http.ResponseWriter, r *http.Request, _ string) {
		ids := map[string][]string{
			"/gmail/v1/users/me/threads/T3": {"M1", "M2", "M3"},
			"/gmail/v1/users/me/threads/T2": {"M1", "M2"},
		}[r.URL.Path]
		messages := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			messages = append(messages, map[string]string{"id": id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "T", "messages": messages})
	})
	threadURL := "https://" + gmailHost + "/gmail/v1/users/me/threads/"
	load := func(event netlog.Event) netcapture.Record {
		t.Helper()
		if event.CaptureID == "" {
			t.Fatalf("event %+v carries no capture_id", event)
		}
		record, err := netcapture.Load(netcapture.RecordPath(dir, event.CaptureID))
		if err != nil {
			t.Fatal(err)
		}
		return record
	}

	withheld := h.do(t, http.MethodGet, threadURL+"T3", "", withPlaceholder())
	assertFilterDenial(t, withheld, h.lastHTTPEvent(t, 1), http.StatusForbidden, engine.ReasonQuota)
	if record := load(h.lastHTTPEvent(t, 1)); record.Response != nil || !strings.Contains(record.Error, "response withheld") {
		t.Fatalf("withheld record response = %+v, error = %q, want no response and the withholding", record.Response, record.Error)
	}

	relayed := h.do(t, http.MethodGet, threadURL+"T2", "", withPlaceholder())
	if relayed.status != http.StatusOK {
		t.Fatalf("response = %d %q, want the thread within quota", relayed.status, relayed.body)
	}
	record := load(h.lastHTTPEvent(t, 2))
	if auth := record.Request.Headers.Get("Authorization"); !strings.Contains(auth, filterPlaceholder) {
		t.Fatalf("captured Authorization = %q, want the placeholder", auth)
	}
	if record.Response == nil || record.Response.Status != http.StatusOK || record.Response.Body == nil {
		t.Fatalf("relayed record response = %+v, want the 200 with its body", record.Response)
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		assertNoCredentialMaterial(t, "capture "+file.Name(), string(raw))
	}
}

// I7: concurrent requests never take more than the quota.
func TestFilterQuotaHoldsUnderConcurrentRequests(t *testing.T) {
	const quota, total = 5, 20
	h := newFilterHarness(t, "quota", withUserPreset("quota", fmt.Sprintf("allow:\n  mail.read: {}\nquotas:\n  mail.read: %d\n", quota)))
	gate := make(chan struct{})
	h.google.setRespond(func(w http.ResponseWriter, _ *http.Request, _ string) {
		<-gate
		_, _ = io.WriteString(w, "{}")
	})
	client := h.client()

	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := map[int]int{}
	denied := make(chan struct{}, total)
	for i := range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%sM%d", messageURL, i), nil)
			if err != nil {
				t.Error(err)
				return
			}
			withPlaceholder()(req)
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			mu.Lock()
			statuses[resp.StatusCode]++
			mu.Unlock()
			if resp.StatusCode == http.StatusForbidden {
				denied <- struct{}{}
			}
		}()
	}
	// Hold the allowed requests upstream until all others were denied, so
	// every request is in flight at once.
	timeout := time.After(10 * time.Second)
	for range total - quota {
		select {
		case <-denied:
		case <-timeout:
			t.Error("timed out waiting for the requests over quota to be denied")
		}
	}
	close(gate)
	wg.Wait()

	if statuses[http.StatusOK] != quota || statuses[http.StatusForbidden] != total-quota {
		t.Fatalf("statuses = %v, want %d allowed and %d denied", statuses, quota, total-quota)
	}
	if n := len(h.google.apiRequests()); n != quota {
		t.Fatalf("upstream requests = %d, want %d", n, quota)
	}
	if h.google.refreshCount() != 1 {
		t.Fatalf("token refreshes = %d, want one shared refresh", h.google.refreshCount())
	}
}

// I11: the placeholder never goes to a host no filter governs, over plain
// HTTP, intercepted HTTPS, or a host with a secret release rule.
func TestFilterPlaceholderToUngovernedHostDenied(t *testing.T) {
	const releasePlaceholder = "ENCLAVE_SECRET_release0123456789abcdef"
	rules := []model.SecretReleaseEntry{{
		Placeholder: releasePlaceholder, Value: "release-secret", Hosts: []string{ungovernedHost}, Header: "x-api-key",
	}}
	h := newFilterHarness(t, "readonly", withReleaseRules(rules))
	count := 0
	for _, tc := range []struct {
		name string
		url  string
		opts []requestOption
	}{
		{name: "plain HTTP header", url: "http://" + ungovernedHost + "/v1", opts: []requestOption{withPlaceholder()}},
		{name: "intercepted HTTPS header", url: "https://" + ungovernedHost + "/v1", opts: []requestOption{header("X-Token", filterPlaceholder)}},
		{name: "intercepted HTTPS query", url: "https://" + ungovernedHost + "/v1?token=" + filterPlaceholder},
		{name: "release header", url: "https://" + ungovernedHost + "/v1", opts: []requestOption{header("X-Api-Key", filterPlaceholder)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count++
			got := h.do(t, http.MethodGet, tc.url, "", tc.opts...)
			assertFilterDenial(t, got, h.lastHTTPEvent(t, count), http.StatusForbidden, engine.ReasonPlaceholder)
			if event := h.lastHTTPEvent(t, count); event.Domain != ungovernedHost || event.Op != "" {
				t.Fatalf("event = %+v, want a deny for %s without an operation", event, ungovernedHost)
			}
		})
	}
	if requests := h.google.apiRequests(); len(requests) != 0 {
		t.Fatalf("upstream requests = %+v, want none", requests)
	}

	// The release rule of the ungoverned host still works next to the filter.
	got := h.do(t, http.MethodGet, "https://"+ungovernedHost+"/v1", "", header("X-Api-Key", releasePlaceholder))
	if got.status != http.StatusOK {
		t.Fatalf("release request = %d %q, want 200", got.status, got.body)
	}
	if event := h.lastHTTPEvent(t, count+1); event.Rule != netlog.RuleAllowlist || event.Filter != "" {
		t.Fatalf("event = %+v, want an allowlist pass without filter fields", event)
	}
	h.assertLogClean(t)
}

// A release rule's placeholder never reaches a governed host, and release
// rules cannot override the filter's decision there.
func TestFilterGovernedHostRefusesReleasePlaceholder(t *testing.T) {
	const releasePlaceholder = "ENCLAVE_SECRET_release0123456789abcdef"
	rules := []model.SecretReleaseEntry{{
		Placeholder: releasePlaceholder, Value: "release-secret", Hosts: []string{ungovernedHost}, Header: "x-api-key",
	}}
	h := newFilterHarness(t, "readonly", withReleaseRules(rules))

	got := h.do(t, http.MethodGet, messageURL+"M1", "", withPlaceholder(), header("X-Api-Key", releasePlaceholder))

	if got.status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", got.status)
	}
	if event := h.lastHTTPEvent(t, 1); event.Rule != "secret-injection" || event.Filter != "gws" || event.Verdict != netlog.VerdictDeny {
		t.Fatalf("event = %+v, want a secret-injection deny on the governed host", event)
	}
	if requests := h.google.apiRequests(); len(requests) != 0 {
		t.Fatalf("upstream requests = %+v, want none", requests)
	}
}

// I3: a request whose TLS server name and Host header differ cannot borrow
// the governed host's interception, nor reach a governed host through
// another host's interception.
func TestFilterDeniesDomainFronting(t *testing.T) {
	h := newFilterHarness(t, "readonly", withRequestsLogMode())

	// Each request also logs the existing sni-host-mismatch event first.
	toUngoverned := h.do(t, http.MethodGet, messageURL+"M1", "", withPlaceholder(), hostHeader(ungovernedHost))
	assertFilterDenial(t, toUngoverned, h.lastHTTPEvent(t, 2), http.StatusForbidden, engine.ReasonNotGoverned)

	toGoverned := h.do(t, http.MethodGet, "https://"+ungovernedHost+"/gmail/v1/users/me/messages/M1", "", withPlaceholder(), hostHeader(gmailHost))
	assertFilterDenial(t, toGoverned, h.lastHTTPEvent(t, 4), http.StatusForbidden, engine.ReasonAmbiguous)

	if requests := h.google.apiRequests(); len(requests) != 0 {
		t.Fatalf("upstream requests = %+v, want none", requests)
	}
}

// Plain HTTP to a governed host is denied: the credential only travels over
// HTTPS. A CONNECT sent to the plain HTTP listener is a plain request too.
func TestFilterDeniesPlainHTTPAndConnectToGovernedHost(t *testing.T) {
	h := newFilterHarness(t, "readonly")

	got := h.do(t, http.MethodGet, "http://"+gmailHost+"/gmail/v1/users/me/messages/M1", "", withPlaceholder())
	assertFilterDenial(t, got, h.lastHTTPEvent(t, 1), http.StatusForbidden, engine.ReasonPlainHTTP)

	conn, err := net.Dial("tcp", h.httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, "CONNECT "+gmailHost+":443 HTTP/1.1\r\nHost: "+gmailHost+":443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT status = %d, want 403", resp.StatusCode)
	}
	if event := h.lastHTTPEvent(t, 2); event.Method != http.MethodConnect || event.Reason != engine.ReasonPlainHTTP {
		t.Fatalf("event = %+v, want the CONNECT denied as plain HTTP", event)
	}
	if requests := h.google.apiRequests(); len(requests) != 0 {
		t.Fatalf("upstream requests = %+v, want none", requests)
	}
}

// A client behind an explicit proxy reaches the dispatch listener through a
// CONNECT tunnel; the governed host is intercepted and filtered there too.
func TestFilterInterceptsGovernedHostThroughConnectTunnel(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(startConnectShim(t, h.httpsAddr)),
			TLSClientConfig: &tls.Config{RootCAs: h.gatewayCA, MinVersion: tls.VersionTLS12},
		},
	}

	allowed := doWith(t, client, http.MethodGet, messageURL+"M1", "", withPlaceholder())
	denied := doWith(t, client, http.MethodGet, messageURL+"M1", "", header("Authorization", "Bearer foreign"))

	if allowed.status != http.StatusOK || denied.status != http.StatusForbidden {
		t.Fatalf("statuses = %d, %d, want 200 and 403", allowed.status, denied.status)
	}
	requests := h.google.apiRequests()
	if len(requests) != 1 || requests[0].Authorization != "Bearer "+filterAccessToken+"-1" {
		t.Fatalf("upstream requests = %+v, want one with the real token", requests)
	}
	if events := readAuditEvents(t, h.logPath); events[0].Type != netlog.TypeTCP || events[0].Rule != "mitm-dispatch" {
		t.Fatalf("events = %+v, want the tunnelled connection intercepted", events)
	}
}

// A request body the filter inspects is checked and forwarded intact.
func TestFilterInspectsAndForwardsRequestBody(t *testing.T) {
	h := newFilterHarness(t, "send-eclipsesource")
	send := func(to string) response {
		raw := base64.RawURLEncoding.EncodeToString([]byte("To: " + to + "\r\nSubject: hi\r\n\r\nbody"))
		return h.do(t, http.MethodPost, messageURL+"send", `{"raw":"`+raw+`"}`, withPlaceholder(), header("Content-Type", "application/json"))
	}

	if got := send("colleague@eclipsesource.com"); got.status != http.StatusOK {
		t.Fatalf("allowed send = %d %q, want 200", got.status, got.body)
	}
	h.waitHTTPEvents(t, 1)
	denied := send("someone@attacker.example")
	assertFilterDenial(t, denied, h.lastHTTPEvent(t, 2), http.StatusForbidden, engine.ReasonConstraint)
	if !strings.Contains(denied.body, "someone@attacker.example") {
		t.Fatalf("deny body = %q, want it to name the recipient", denied.body)
	}

	requests := h.google.apiRequests()
	if len(requests) != 1 || !strings.Contains(requests[0].Body, `"raw":`) || requests[0].Method != http.MethodPost {
		t.Fatalf("upstream requests = %+v, want the allowed send with its body", requests)
	}
	h.assertLogClean(t)
}

// An upstream 401 invalidates the injected token, so the next request mints a
// new one instead of reusing the rejected token.
func TestFilterUpstreamUnauthorizedInvalidatesToken(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	h.google.setRespond(func(w http.ResponseWriter, _ *http.Request, token string) {
		if token == filterAccessToken+"-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "{}")
	})

	first := h.do(t, http.MethodGet, messageURL+"M1", "", withPlaceholder())
	second := h.do(t, http.MethodGet, messageURL+"M1", "", withPlaceholder())

	if first.status != http.StatusUnauthorized || second.status != http.StatusOK {
		t.Fatalf("statuses = %d, %d, want 401 then 200", first.status, second.status)
	}
	if h.google.refreshCount() != 2 {
		t.Fatalf("token refreshes = %d, want 2", h.google.refreshCount())
	}
	if requests := h.google.apiRequests(); requests[1].Authorization != "Bearer "+filterAccessToken+"-2" {
		t.Fatalf("second upstream Authorization = %q, want the new token", requests[1].Authorization)
	}
}

// I6: a failed token refresh denies with 502 and the filter's deny body,
// naming the filter and the login command, and never reaches the API.
func TestFilterTokenRefreshFailureDenies(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	h.google.mu.Lock()
	h.google.tokenFail = true
	h.google.mu.Unlock()

	got := h.do(t, http.MethodGet, messageURL+"M1", "", withPlaceholder())

	assertFilterDenial(t, got, h.lastHTTPEvent(t, 1), http.StatusBadGateway, "token:invalid_grant")
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatalf("deny body %q is not the filter's JSON: %v", got.body, err)
	}
	if body.Error.Code != http.StatusBadGateway || body.Error.Status != "UNAVAILABLE" || !strings.Contains(body.Error.Message, "enclave filters login gws") {
		t.Fatalf("deny body = %+v, want 502 UNAVAILABLE naming the login command", body.Error)
	}
	if requests := h.google.apiRequests(); len(requests) != 0 {
		t.Fatalf("upstream requests = %+v, want none", requests)
	}
	h.assertLogClean(t)
}

// A failed upstream request releases its quota reservation.
func TestFilterUpstreamFailureReleasesQuota(t *testing.T) {
	h := newFilterHarness(t, "quota", withUserPreset("quota", "allow:\n  mail.read: {}\nquotas:\n  mail.read: 1\n"))
	h.google.setRespond(func(w http.ResponseWriter, r *http.Request, _ string) {
		if strings.HasSuffix(r.URL.Path, "/M1") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, "{}")
	})

	if got := h.do(t, http.MethodGet, messageURL+"M1", "", withPlaceholder()); got.status != http.StatusNotFound {
		t.Fatalf("status = %d, want the upstream 404", got.status)
	}
	if got := h.do(t, http.MethodGet, messageURL+"M2", "", withPlaceholder()); got.status != http.StatusOK {
		t.Fatalf("status = %d, want 200: the 404 must not use the quota", got.status)
	}
	if got := h.do(t, http.MethodGet, messageURL+"M3", "", withPlaceholder()); got.status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 once the quota is used", got.status)
	}
}

// An allowlist reload keeps the filter state: quota usage survives, while the
// connections the proxy held are closed as a restart would close them.
func TestFilterQuotaSurvivesAllowlistReload(t *testing.T) {
	h := newFilterHarness(t, "quota", withUserPreset("quota", "allow:\n  mail.read: {}\nquotas:\n  mail.read: 1\n"))
	if got := h.do(t, http.MethodGet, messageURL+"M1", "", withPlaceholder()); got.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", got.status)
	}
	h.waitHTTPEvents(t, 1)
	heldTLS, err := tls.Dial("tcp", h.httpsAddr, &tls.Config{ServerName: gmailHost, RootCAs: h.gatewayCA, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = heldTLS.Close() }()
	heldHTTP, err := net.Dial("tcp", h.httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = heldHTTP.Close() }()
	waitForTracked(t, h)

	h.proxy.reload(Allowlist{Allowed: []string{gmailHost, tokenHost}}, h.httpConns, h.dispatch)

	for name, conn := range map[string]net.Conn{"TLS": heldTLS, "HTTP": heldHTTP} {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil || isTimeout(err) {
			t.Errorf("a %s connection held across the reload stayed open (%v)", name, err)
		}
	}
	over := h.do(t, http.MethodGet, messageURL+"M2", "", withPlaceholder())
	assertFilterDenial(t, over, h.lastHTTPEvent(t, 2), http.StatusForbidden, engine.ReasonQuota)
	if _, err := h.client().Get("https://" + googleAPIHost + "/discovery/v1/apis/gmail/v1/rest"); err == nil {
		t.Fatal("a host the reloaded allowlist dropped still passes")
	}
}

// waitForTracked waits until both servers track the held connections.
func waitForTracked(t *testing.T, h *filterHarness) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.dispatch.mu.Lock()
		tls := len(h.dispatch.conns)
		h.dispatch.mu.Unlock()
		h.httpConns.mu.Lock()
		plain := len(h.httpConns.conns)
		h.httpConns.mu.Unlock()
		if tls > 0 && plain > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("held connections were never tracked")
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func TestProxyInterceptsGovernedHostsInCoarseMode(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	for host, want := range map[string]bool{
		gmailHost:                  true,
		googleAPIHost:              true,
		"docs.googleapis.com":      true,
		"x." + gmailHost:           true,
		ungovernedHost:             false,
		"sheets.googleapis.com":    false,
		"googleapis.com":           false,
		"x." + gmailHost + ".evil": false,
	} {
		if got := h.proxy.intercepts(host); got != want {
			t.Errorf("intercepts(%s) = %v, want %v", host, got, want)
		}
	}
}

// The allowlist entry of a governed host admits its subdomains, so the filter
// governs them too. Routes match their hosts exactly, so a subdomain gets the
// no route denial and never reaches upstream without a check.
func TestFilterGovernsSubdomainsOfGovernedHosts(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	host := "x." + gmailHost
	if !h.proxy.hostPermitted(host) {
		t.Fatalf("%s is not admitted by the allowlist entry of %s", host, gmailHost)
	}
	for i, opts := range [][]requestOption{{withPlaceholder()}, {}} {
		got := h.do(t, http.MethodGet, "https://"+host+"/gmail/v1/users/me/messages/M1", "", opts...)
		assertFilterDenial(t, got, h.lastHTTPEvent(t, i+1), http.StatusForbidden, engine.ReasonNoRoute)
	}
	if requests := h.google.apiRequests(); len(requests) != 0 {
		t.Fatalf("upstream requests = %+v, want none", requests)
	}
	h.assertLogClean(t)
}

// An unrestricted proxy never enforces filters, so it refuses to run with
// them.
func TestRunRefusesFiltersInUnrestrictedMode(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	err := Run(t.Context(), Options{
		NetworkMode: model.NetworkModeUnrestricted,
		Filters:     h.proxy.filters,
		TLSStore:    tlsstore.New(t.TempDir()),
	})
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("Run() error = %v, want request filters refused in unrestricted mode", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

// Run applies an allowlist reload in place: the filters stay active, and the
// proxy reports ready again so the entrypoint knows the reload is done.
func TestRunReloadsAllowlistAndKeepsFilters(t *testing.T) {
	h := newFilterHarness(t, "readonly")
	reloads := make(chan Allowlist)
	ready := make(chan struct{}, 4)
	httpPort := freePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			HTTPPort:       httpPort,
			HTTPSPort:      freePort(t),
			AllowedDomains: []string{gmailHost, googleAPIHost},
			TLSStore:       tlsstore.New(t.TempDir()),
			Filters:        h.proxy.filters,
			Reloads:        reloads,
			OnReady:        func() { ready <- struct{}{} },
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})
	awaitReady := func() {
		t.Helper()
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("Run() ended: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("proxy never reported ready")
		}
	}
	plainGet := func(host string) response {
		t.Helper()
		return doWith(t, &http.Client{Timeout: 5 * time.Second}, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/x", httpPort), "", hostHeader(host))
	}
	awaitReady()

	// Both hosts are governed; plain HTTP to them is the filter's to deny.
	if got := plainGet(gmailHost); got.status != http.StatusForbidden || !strings.Contains(got.body, "plain HTTP") {
		t.Fatalf("before reload = %d %q, want the filter's plain HTTP denial", got.status, got.body)
	}
	reloads <- Allowlist{Allowed: []string{googleAPIHost}}
	awaitReady()

	if got := plainGet(gmailHost); got.status != http.StatusForbidden || !strings.Contains(got.body, "Domain not in allowlist") {
		t.Fatalf("after reload = %d %q, want the allowlist denial", got.status, got.body)
	}
	if got := plainGet(googleAPIHost); got.status != http.StatusForbidden || !strings.Contains(got.body, "plain HTTP") {
		t.Fatalf("after reload = %d %q, want the filter still active", got.status, got.body)
	}
}
