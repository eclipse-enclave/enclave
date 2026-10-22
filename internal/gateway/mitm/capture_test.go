// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/model"
	"enclave/internal/netcapture"
	"enclave/internal/netlog"
)

// captureProxy is a proxy for host with full capture into a fresh directory
// and an audit log, as --network-log=full runs it.
func captureProxy(t *testing.T, allowed []string, rules []model.SecretReleaseEntry, opts netcapture.Options) (*proxy, string, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "network.log")
	audit, err := netlog.NewAppender(logPath)
	if err != nil {
		t.Fatalf("netlog.NewAppender() error = %v", err)
	}
	t.Cleanup(func() { _ = audit.Close() })

	p := newProxy(allowed, nil, rules, false, audit, testSession)
	opts.Dir = t.TempDir()
	opts.Session = testSession
	opts.Secrets = captureSecrets(rules)
	p.captures, err = netcapture.Open(opts)
	if err != nil {
		t.Fatalf("netcapture.Open() error = %v", err)
	}
	return p, opts.Dir, logPath
}

// capturedEvent returns the single http event and the record it points to.
func capturedEvent(t *testing.T, logPath string, dir string) (netlog.Event, netcapture.Record, string) {
	t.Helper()
	events := readAuditEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("audit events = %+v, want one", events)
	}
	event := events[0]
	if event.CaptureID == "" {
		t.Fatalf("event %+v carries no capture_id", event)
	}
	path := netcapture.RecordPath(dir, event.CaptureID)
	record, err := netcapture.Load(path)
	if err != nil {
		t.Fatalf("Load(%s) error = %v", path, err)
	}
	return event, record, path
}

func readCapturedBody(t *testing.T, recordPath string, body *netcapture.Body) string {
	t.Helper()
	path, ok := netcapture.BodyPath(recordPath, body)
	if !ok {
		t.Fatalf("body %+v has no file", body)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test-owned temp path.
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

func upstreamHost(t *testing.T, server *httptest.Server) (string, string) {
	t.Helper()
	hostPort := strings.TrimPrefix(strings.TrimPrefix(server.URL, "http://"), "https://")
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", hostPort, err)
	}
	return host, hostPort
}

func TestProxyCapturesPostExchange(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"received":%q}`, body)
	}))
	defer upstream.Close()
	host, hostPort := upstreamHost(t, upstream)

	p, dir, logPath := captureProxy(t, []string{host}, nil, netcapture.Options{})
	req := httptest.NewRequest(http.MethodPost, "http://"+hostPort+"/v1/items?draft=true", strings.NewReader(`{"name":"report"}`))
	req.Host = hostPort
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "abc")
	recorder := httptest.NewRecorder()

	p.handler("http")(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("proxy status = %d, want %d", recorder.Code, http.StatusCreated)
	}

	event, record, recordPath := capturedEvent(t, logPath, dir)
	if event.Path != "/v1/items" {
		t.Fatalf("event path = %q: the log must keep leaving the query out", event.Path)
	}
	if record.Session != testSession || record.ID != event.CaptureID {
		t.Fatalf("record identity = %s/%s", record.Session, record.ID)
	}
	if record.Request.Method != http.MethodPost || record.Request.URL != "http://"+hostPort+"/v1/items?draft=true" {
		t.Fatalf("request line = %s %s", record.Request.Method, record.Request.URL)
	}
	if record.Request.Headers.Get("X-Request-Id") != "abc" || record.Request.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("request headers = %v", record.Request.Headers)
	}
	if got := readCapturedBody(t, recordPath, record.Request.Body); got != `{"name":"report"}` || record.Request.Body.Truncated {
		t.Fatalf("request body = %q (%+v)", got, record.Request.Body)
	}
	if record.Response == nil || record.Response.Status != http.StatusCreated || record.Response.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("response = %+v", record.Response)
	}
	if got := readCapturedBody(t, recordPath, record.Response.Body); got != recorder.Body.String() {
		t.Fatalf("response body = %q, want what the client received: %q", got, recorder.Body.String())
	}
}

// The capture snapshots the request before the gateway releases the secret, and
// redacts the value wherever it appears afterwards, so the store holds the
// placeholder even when the upstream echoes the credential back.
func TestProxyCaptureKeepsPlaceholderAndNeverTheReleasedSecret(t *testing.T) {
	const placeholder = "ENCLAVE_SECRET_0123456789abcdef"
	const secret = "sk-live-do-not-store-0123456789"
	var received string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Echo-Authorization", received)
		_, _ = fmt.Fprintf(w, `{"authorization":%q}`, received)
	}))
	defer upstream.Close()

	// The test certificate is valid for example.com; every connection goes to
	// the test server whatever host the request names.
	rules := []model.SecretReleaseEntry{{
		SecretID:    "API_KEY",
		Placeholder: placeholder,
		Value:       secret,
		Hosts:       []string{"example.com"},
		Header:      "authorization",
		Format:      "Bearer %s",
	}}
	p, dir, logPath := captureProxy(t, []string{"example.com"}, rules, netcapture.Options{})
	p.transport.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	p.transport.DialContext = func(ctx context.Context, network string, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}

	req := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", strings.NewReader(`{"prompt":"hello"}`))
	req.Host = "example.com"
	req.Header.Set("Authorization", placeholder)
	recorder := httptest.NewRecorder()

	p.handler("https")(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if received != "Bearer "+secret {
		t.Fatalf("upstream received Authorization %q, want the released secret", received)
	}

	_, record, recordPath := capturedEvent(t, logPath, dir)
	if got := record.Request.Headers.Get("Authorization"); got != placeholder {
		t.Fatalf("captured Authorization = %q, want the placeholder", got)
	}
	if got := record.Response.Headers.Get("X-Echo-Authorization"); got != "Bearer "+placeholder {
		t.Fatalf("captured echo header = %q, want the placeholder", got)
	}
	if got := readCapturedBody(t, recordPath, record.Response.Body); got != `{"authorization":"Bearer `+placeholder+`"}` {
		t.Fatalf("captured response body = %q", got)
	}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, readErr := os.ReadFile(path) // #nosec G304 -- test-owned temp path.
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s contains the released secret", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk capture dir: %v", err)
	}
}

func TestProxyCaptureTruncatesBodiesOverTheCap(t *testing.T) {
	response := strings.Repeat("r", 100)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, response)
	}))
	defer upstream.Close()
	host, hostPort := upstreamHost(t, upstream)

	p, dir, logPath := captureProxy(t, []string{host}, nil, netcapture.Options{MaxBodyBytes: 16})
	req := httptest.NewRequest(http.MethodPost, "http://"+hostPort+"/upload", strings.NewReader(strings.Repeat("q", 50)))
	req.Host = hostPort
	recorder := httptest.NewRecorder()

	p.handler("http")(recorder, req)
	if recorder.Body.String() != response {
		t.Fatalf("client received %d bytes, want the whole response: the cap applies to the capture only", recorder.Body.Len())
	}

	_, record, recordPath := capturedEvent(t, logPath, dir)
	for name, tc := range map[string]struct {
		body *netcapture.Body
		full int64
	}{
		"request":  {record.Request.Body, 50},
		"response": {record.Response.Body, 100},
	} {
		if tc.body == nil || !tc.body.Truncated || tc.body.Bytes != tc.full || tc.body.StoredBytes != 16 {
			t.Fatalf("%s body = %+v, want truncated at 16 of %d bytes", name, tc.body, tc.full)
		}
		if got := readCapturedBody(t, recordPath, tc.body); len(got) != 16 {
			t.Fatalf("%s body file holds %d bytes, want 16", name, len(got))
		}
	}
}

func TestProxyCaptureKeepsStreamingIncremental(t *testing.T) {
	sendLast := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: first\n\n")
		_ = rc.Flush()
		<-sendLast
		_, _ = io.WriteString(w, "data: last\n\n")
	}))
	defer upstream.Close()
	host, hostPort := upstreamHost(t, upstream)

	p, dir, logPath := captureProxy(t, []string{host}, nil, netcapture.Options{})
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()
	releaseLast := sync.OnceFunc(func() { close(sendLast) })
	defer releaseLast()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyServer.URL+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Host = hostPort
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The first event arrives while the upstream still holds the stream open.
	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "data: first\n" {
		t.Fatalf("first line = %q, %v; want it before the stream ends", line, err)
	}
	releaseLast()
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("read rest of stream: %v", err)
	}

	_, record, recordPath := capturedEvent(t, logPath, dir)
	if got := readCapturedBody(t, recordPath, record.Response.Body); got != "data: first\n\ndata: last\n\n" {
		t.Fatalf("captured stream = %q", got)
	}
}

func TestProxyCaptureRecordsUpstreamError(t *testing.T) {
	p, dir, logPath := captureProxy(t, []string{"api.example.com"}, nil, netcapture.Options{})
	p.transport = &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, fmt.Errorf("forced dial failure")
		},
	}
	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/fail", nil)
	req.Host = "api.example.com"
	p.handler("http")(httptest.NewRecorder(), req)

	event, record, _ := capturedEvent(t, logPath, dir)
	if event.Rule != "upstream-error" || record.Response != nil || !strings.Contains(record.Error, "forced dial failure") {
		t.Fatalf("event %+v, record %+v", event, record)
	}
}

func TestProxyCaptureSkipsDeniedRequests(t *testing.T) {
	rules := []model.SecretReleaseEntry{{
		Placeholder: "ENCLAVE_SECRET_abc",
		Value:       "real-secret",
		Hosts:       []string{"api.example.com"},
		Header:      "authorization",
	}}
	p, dir, logPath := captureProxy(t, []string{"api.example.com"}, rules, netcapture.Options{})

	blocked := httptest.NewRequest(http.MethodGet, "http://evil.example.com/", nil)
	blocked.Host = "evil.example.com"
	p.handler("http")(httptest.NewRecorder(), blocked)
	// A placeholder over plaintext HTTP is refused before anything is relayed.
	refused := httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil)
	refused.Host = "api.example.com"
	refused.Header.Set("Authorization", "ENCLAVE_SECRET_abc")
	p.handler("http")(httptest.NewRecorder(), refused)

	for _, event := range readAuditEvents(t, logPath) {
		if event.Verdict != netlog.VerdictDeny || event.CaptureID != "" {
			t.Fatalf("event = %+v, want a deny without capture", event)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("capture dir holds %d entries after denied requests only", len(entries))
	}
}

func TestProxyCapturesUpgradeHandshakeOnly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(echoUpgradeHandler))
	defer upstream.Close()
	host, hostPort := upstreamHost(t, upstream)

	p, dir, logPath := captureProxy(t, []string{host}, nil, netcapture.Options{})
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()
	assertEchoUpgradeRelayed(t, proxyServer, hostPort)

	waitForEvents(t, logPath, 1)
	_, record, _ := capturedEvent(t, logPath, dir)
	if record.Response == nil || record.Response.Status != http.StatusSwitchingProtocols || record.Response.Body != nil {
		t.Fatalf("response = %+v, want the 101 head without a body", record.Response)
	}
	if record.Request.Headers.Get("Upgrade") != "echo" {
		t.Fatalf("request headers = %v", record.Request.Headers)
	}
}

// waitForEvents polls the audit log, which the proxy writes once an exchange
// has ended rather than when the client has its response.
func waitForEvents(t *testing.T, logPath string, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(readAuditEvents(t, logPath)) < count && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProxyCaptureMarksRequestBodyTheUpstreamDidNotRead(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/read" {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Refuse before reading the upload, as an API rejecting a bad token does.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	host, hostPort := upstreamHost(t, upstream)

	p, dir, logPath := captureProxy(t, []string{host}, nil, netcapture.Options{})
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()

	post := func(path string, size int) {
		req, err := http.NewRequest(http.MethodPost, proxyServer.URL+path, strings.NewReader(strings.Repeat("u", size)))
		if err != nil {
			t.Fatalf("NewRequest() error = %v", err)
		}
		req.Host = hostPort
		// The proxy may close the connection without reading an upload the
		// upstream refused, so the client can see a write error instead of the
		// response; only the capture matters here.
		if resp, err := proxyServer.Client().Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}

	post("/read", 64*1024)
	waitForEvents(t, logPath, 1)
	_, read, _ := capturedEvent(t, logPath, dir)
	if body := read.Request.Body; body == nil || body.Truncated || body.Bytes != 64*1024 {
		t.Fatalf("fully read request body = %+v, want 64 KiB and complete", body)
	}

	post("/refuse", 8*1024*1024)
	waitForEvents(t, logPath, 2)
	events := readAuditEvents(t, logPath)
	refused, err := netcapture.Load(netcapture.RecordPath(dir, events[1].CaptureID))
	if err != nil {
		t.Fatalf("load capture of the refused upload: %v", err)
	}
	if refused.Response == nil || refused.Response.Status != http.StatusUnauthorized {
		t.Fatalf("response = %+v", refused.Response)
	}
	if body := refused.Request.Body; body == nil || !body.Truncated {
		t.Fatalf("request body the upstream refused = %+v, want it marked truncated", body)
	}
}

// Most APIs compress their responses. The client still gets the compressed
// bytes as sent, while the capture stores them decoded so that an echoed
// credential is redacted there too.
func TestProxyCaptureRedactsSecretsInCompressedResponses(t *testing.T) {
	const placeholder = "ENCLAVE_SECRET_0123456789abcdef"
	const secret = "sk-live-do-not-store-0123456789"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = fmt.Fprintf(writer, `{"authorization":%q}`, r.Header.Get("Authorization"))
		_ = writer.Close()
	}))
	defer upstream.Close()

	rules := []model.SecretReleaseEntry{{
		SecretID:    "API_KEY",
		Placeholder: placeholder,
		Value:       secret,
		Hosts:       []string{"example.com"},
		Header:      "authorization",
		Format:      "Bearer %s",
	}}
	p, dir, logPath := captureProxy(t, []string{"example.com"}, rules, netcapture.Options{})
	p.transport.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	p.transport.DialContext = func(ctx context.Context, network string, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}

	req := httptest.NewRequest(http.MethodGet, "https://example.com/v1/whoami", nil)
	req.Host = "example.com"
	req.Header.Set("Authorization", placeholder)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	p.handler("https")(recorder, req)

	relayed, err := gzip.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("client did not receive the gzip body as sent: %v", err)
	}
	plain, _ := io.ReadAll(relayed)
	if string(plain) != `{"authorization":"Bearer `+secret+`"}` {
		t.Fatalf("client received %q", plain)
	}

	_, record, recordPath := capturedEvent(t, logPath, dir)
	body := record.Response.Body
	if body == nil || body.DecodedFrom != "gzip" || body.Bytes != int64(recorder.Body.Len()) {
		t.Fatalf("response body = %+v, want it decoded from gzip", body)
	}
	if got := readCapturedBody(t, recordPath, body); got != `{"authorization":"Bearer `+placeholder+`"}` {
		t.Fatalf("captured body = %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name())) // #nosec G304 -- test-owned temp path.
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s contains the released secret", entry.Name())
		}
	}
}
