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
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/filter"
	"enclave/internal/filter/spec"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
	"enclave/internal/netlog"
)

// Credential material of the test filter. None of it may ever reach the
// session: not in a response, a deny body, or the network log.
const (
	filterPlaceholder  = "ENCLAVE_SECRET_00112233445566778899aabbccddeeff0011223344556677"
	filterRefreshToken = "1//refresh-token-never-in-container"
	filterClientSecret = "client-secret-never-in-container"
	filterAccessToken  = "ya29.access-token-never-in-container"
	// upstreamMarker stands for mail content in upstream responses.
	upstreamMarker = "UPSTREAM-MAIL-CONTENT"
)

const (
	gmailHost     = "gmail.googleapis.com"
	googleAPIHost = "www.googleapis.com"
	tokenHost     = "oauth2.googleapis.com"
	// ungovernedHost is allowlisted but no filter governs it.
	ungovernedHost = "api.example.com"
)

var filterFixtureDir = filepath.Join("..", "..", "filter", "testdata", "gws")

// apiRequest is what the fake Google API saw.
type apiRequest struct {
	Host, Method, Path, RawQuery, Authorization string
	Body                                        string
}

// fakeGoogle serves the token endpoint and the governed APIs over TLS with a
// certificate for every requested name.
type fakeGoogle struct {
	server *httptest.Server
	roots  *x509.CertPool

	mu        sync.Mutex
	refreshes int
	tokenFail bool
	requests  []apiRequest
	// respond answers API requests; nil answers 200 with upstreamMarker.
	respond func(w http.ResponseWriter, r *http.Request, token string)
}

func startFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	store := tlsstore.New(t.TempDir())
	if err := store.EnsureCA(); err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(store.CACertPath)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGoogle{roots: x509.NewCertPool()}
	if !g.roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("parse upstream CA")
	}
	g.server = httptest.NewUnstartedServer(http.HandlerFunc(g.serve))
	g.server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return store.Certificate(hello.ServerName)
		},
	}
	g.server.StartTLS()
	t.Cleanup(g.server.Close)
	return g
}

func (g *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	if r.Host == tokenHost && r.URL.Path == "/token" {
		g.serveToken(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.requests = append(g.requests, apiRequest{
		Host: r.Host, Method: r.Method, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery,
		Authorization: r.Header.Get("Authorization"), Body: string(body),
	})
	respond := g.respond
	g.mu.Unlock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if respond != nil {
		respond(w, r, token)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"content":"`+upstreamMarker+`"}`)
}

func (g *fakeGoogle) serveToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	g.mu.Lock()
	g.refreshes++
	n, fail := g.refreshes, g.tokenFail
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if fail || r.PostForm.Get("refresh_token") != filterRefreshToken || r.PostForm.Get("client_secret") != filterClientSecret {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": fmt.Sprintf("%s-%d", filterAccessToken, n),
		"expires_in":   3600,
		"token_type":   "Bearer",
	})
}

func (g *fakeGoogle) apiRequests() []apiRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]apiRequest(nil), g.requests...)
}

func (g *fakeGoogle) refreshCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refreshes
}

func (g *fakeGoogle) setRespond(respond func(http.ResponseWriter, *http.Request, string)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.respond = respond
}

// dial sends every connection to the fake, whatever name it is for.
func (g *fakeGoogle) dial(ctx context.Context, network string, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, g.server.Listener.Addr().String())
}

func (g *fakeGoogle) transport() *http.Transport {
	return &http.Transport{
		DialContext:     g.dial,
		TLSClientConfig: &tls.Config{RootCAs: g.roots, MinVersion: tls.VersionTLS12},
	}
}

// filterHarness is a restricted proxy with the gws test filter, in the
// default coarse log mode, in front of fakeGoogle.
type filterHarness struct {
	proxy   *proxy
	google  *fakeGoogle
	logPath string
	// httpsAddr is the HTTPS dispatch listener, the target of redirected
	// port 443 traffic. gatewayCA is the CA the session trusts.
	httpsAddr string
	gatewayCA *x509.CertPool
	// httpAddr is the plain HTTP listener, the target of port 80 traffic.
	httpAddr string
	// dispatch and httpConns are what a reload closes connections of.
	dispatch  *httpsDispatchServer
	httpConns *connSet
}

type harnessOption func(*harnessConfig)

type harnessConfig struct {
	userPresets map[string]string
	rules       []model.SecretReleaseEntry
	forceMITM   bool
}

func withUserPreset(name, body string) harnessOption {
	return func(c *harnessConfig) {
		if c.userPresets == nil {
			c.userPresets = map[string]string{}
		}
		c.userPresets[name] = body
	}
}

func withReleaseRules(rules []model.SecretReleaseEntry) harnessOption {
	return func(c *harnessConfig) { c.rules = rules }
}

func withRequestsLogMode() harnessOption {
	return func(c *harnessConfig) { c.forceMITM = true }
}

// writeFilterBundle compiles the gws fixture with preset and writes the files
// the host hands the gateway.
func writeFilterBundle(t *testing.T, preset string, userPresets map[string]string) string {
	t.Helper()
	f, err := spec.Load(filterFixtureDir, "gws")
	if err != nil {
		t.Fatalf("spec.Load() error = %v", err)
	}
	userDir := t.TempDir()
	for name, body := range userPresets {
		if err := os.WriteFile(filepath.Join(userDir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := f.ResolvePreset(preset, userDir)
	if err != nil {
		t.Fatalf("ResolvePreset(%s) error = %v", preset, err)
	}
	policy, err := spec.Compile(f, p, filterPlaceholder)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	cred, err := f.NewOAuth2Credential("client-id.apps.googleusercontent.com", filterClientSecret, filterRefreshToken, preset, nil)
	if err != nil {
		t.Fatalf("NewOAuth2Credential() error = %v", err)
	}
	policyJSON, err := policy.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	credJSON, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filter.PolicyFileName("gws")), policyJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filter.CredentialFileName("gws")), credJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newFilterHarness(t *testing.T, preset string, opts ...harnessOption) *filterHarness {
	t.Helper()
	var cfg harnessConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	google := startFakeGoogle(t)
	client := &http.Client{
		Transport: google.transport(),
		Timeout:   10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	set, err := LoadFilters(writeFilterBundle(t, preset, cfg.userPresets), client)
	if err != nil {
		t.Fatalf("LoadFilters() error = %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = audit.Close() })
	allowed := []string{gmailHost, googleAPIHost, tokenHost, "docs.googleapis.com", ungovernedHost}
	p := newProxy(allowed, nil, cfg.rules, cfg.forceMITM, audit, testSession)
	p.filters = set
	for _, transport := range []*http.Transport{p.transport, p.upgradeTransport} {
		transport.DialContext = google.dial
		transport.TLSClientConfig = &tls.Config{RootCAs: google.roots, MinVersion: tls.VersionTLS12}
	}
	h := &filterHarness{proxy: p, google: google, logPath: logPath, httpConns: newConnSet()}
	h.dispatch, h.httpsAddr, h.gatewayCA = startDispatchServer(t, p)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveHTTPOn(t, p, h.httpConns.track(listener))
	h.httpAddr = listener.Addr().String()
	return h
}

// startDispatchServer is startHTTPSDispatch that also returns the server.
func startDispatchServer(t *testing.T, p *proxy) (*httpsDispatchServer, string, *x509.CertPool) {
	t.Helper()
	store := tlsstore.New(t.TempDir())
	if err := store.EnsureCA(); err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(store.CACertPath)
	if err != nil {
		t.Fatal(err)
	}
	gatewayCA := x509.NewCertPool()
	if !gatewayCA.AppendCertsFromPEM(caPEM) {
		t.Fatal("parse gateway CA")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := newHTTPSDispatchServer(p, store, listener)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return server, listener.Addr().String(), gatewayCA
}

// client is the session: every HTTPS connection goes to the dispatch listener
// and every plain HTTP connection to the HTTP listener, as the gateway's
// redirects send them.
func (h *filterHarness) client() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				target := h.httpsAddr
				if strings.HasSuffix(addr, ":80") {
					target = h.httpAddr
				}
				return (&net.Dialer{}).DialContext(ctx, network, target)
			},
			TLSClientConfig: &tls.Config{RootCAs: h.gatewayCA, MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type requestOption func(*http.Request)

func header(name, value string) requestOption {
	return func(r *http.Request) { r.Header.Set(name, value) }
}

func withPlaceholder() requestOption {
	return header("Authorization", "Bearer "+filterPlaceholder)
}

func hostHeader(host string) requestOption {
	return func(r *http.Request) { r.Host = host }
}

type response struct {
	status int
	header http.Header
	body   string
}

func (h *filterHarness) do(t *testing.T, method, rawURL string, body string, opts ...requestOption) response {
	t.Helper()
	return doWith(t, h.client(), method, rawURL, body, opts...)
}

func doWith(t *testing.T, client *http.Client, method, rawURL string, body string, opts ...requestOption) response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, opt := range opts {
		opt(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response of %s %s: %v", method, rawURL, err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: string(data)}
}

// httpEvents returns the http events of the network log.
func (h *filterHarness) httpEvents(t *testing.T, want int) []netlog.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var events []netlog.Event
		for _, event := range readAuditEvents(t, h.logPath) {
			if event.Type == netlog.TypeHTTP {
				events = append(events, event)
			}
		}
		if len(events) >= want || time.Now().After(deadline) {
			return events
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// lastHTTPEvent waits for the count-th http event and returns it.
func (h *filterHarness) lastHTTPEvent(t *testing.T, count int) netlog.Event {
	t.Helper()
	events := h.httpEvents(t, count)
	if len(events) != count {
		t.Fatalf("http events = %+v, want %d", events, count)
	}
	return events[count-1]
}

// waitHTTPEvents waits for count http events. The proxy logs a request after
// the client got its response, and every h.do uses a new connection, so a test
// that reads events by position waits for them before the next request.
func (h *filterHarness) waitHTTPEvents(t *testing.T, count int) {
	t.Helper()
	_ = h.lastHTTPEvent(t, count)
}

// assertFilterDenial checks a deny response from the gws filter and its log
// event.
func assertFilterDenial(t *testing.T, got response, event netlog.Event, status int, reason string) {
	t.Helper()
	if got.status != status {
		t.Fatalf("status = %d, want %d (body %q)", got.status, status, got.body)
	}
	if event.Verdict != netlog.VerdictDeny || event.Rule != netlog.RuleFilter || event.Filter != "gws" ||
		event.Reason != reason || event.Status != status || event.Session != testSession {
		t.Fatalf("event = %+v, want filter deny gws reason %s status %d", event, reason, status)
	}
	assertNoCredentialMaterial(t, "deny body", got.body)
}

// assertNoCredentialMaterial enforces I1 and I12 on text the session or the
// log reader sees.
func assertNoCredentialMaterial(t *testing.T, what string, text string) {
	t.Helper()
	for _, secret := range []string{filterAccessToken, filterRefreshToken, filterClientSecret} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s contains credential material %q: %s", what, secret, text)
		}
	}
}

// assertLogClean checks that the network log holds no credential material,
// placeholder, or body content.
func (h *filterHarness) assertLogClean(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(h.logPath)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialMaterial(t, "network log", string(raw))
	for _, value := range []string{filterPlaceholder, upstreamMarker} {
		if strings.Contains(string(raw), value) {
			t.Fatalf("network log contains %q: %s", value, raw)
		}
	}
}

// startConnectShim is a minimal explicit proxy: it answers CONNECT with 200
// and relays the tunnel to target, the gateway's HTTPS dispatch listener.
func startConnectShim(t *testing.T, target string) *url.URL {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				req, err := http.ReadRequest(reader)
				if err != nil || req.Method != http.MethodConnect {
					_, _ = io.WriteString(conn, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
					return
				}
				upstream, err := net.Dial("tcp", target)
				if err != nil {
					_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer func() { _ = upstream.Close() }()
				_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
				go func() {
					_, _ = io.Copy(upstream, reader)
					closeWrite(upstream)
				}()
				_, _ = io.Copy(conn, upstream)
			}()
		}
	}()
	return &url.URL{Scheme: "http", Host: listener.Addr().String()}
}
