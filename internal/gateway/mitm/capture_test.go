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

// Upgrades to protocols other than WebSocket, such as SPDY, are relayed
// without being parsed.
func TestProxyCapturesOnlyTheHandshakeOfOtherUpgrades(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(echoUpgradeHandler))
	defer upstream.Close()
	host, hostPort := upstreamHost(t, upstream)

	p, dir, logPath := captureProxy(t, []string{host}, nil, netcapture.Options{})
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()
	assertEchoUpgradeRelayed(t, proxyServer, hostPort)

	waitForEvents(t, logPath, 1)
	_, record, _ := capturedEvent(t, logPath, dir)
	if record.Response == nil || record.Response.Status != http.StatusSwitchingProtocols || record.Response.Body != nil || record.WebSocket != nil {
		t.Fatalf("response = %+v, websocket = %+v; want the 101 head alone", record.Response, record.WebSocket)
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

// An upstream that ignores identity encoding is still relayed unchanged, but
// its encoded body is never stored.
func TestProxyCaptureOmitsBodiesUpstreamsEncodeAnyway(t *testing.T) {
	const placeholder = "ENCLAVE_SECRET_0123456789abcdef"
	const secret = "sk-live-do-not-store-0123456789"
	for _, encoding := range []string{"gzip", "br"} {
		t.Run(encoding, func(t *testing.T) {
			var relayed []byte
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// This upstream ignores Accept-Encoding: identity.
				var buffer bytes.Buffer
				writer := gzip.NewWriter(&buffer)
				_, _ = fmt.Fprintf(writer, `{"authorization":%q}`, r.Header.Get("Authorization"))
				_ = writer.Close()
				relayed = buffer.Bytes()
				w.Header().Set("Content-Encoding", encoding)
				_, _ = w.Write(relayed)
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
			req.Header.Set("Accept-Encoding", "gzip, br")
			recorder := httptest.NewRecorder()
			p.handler("https")(recorder, req)

			if recorder.Header().Get("Content-Encoding") != encoding || !bytes.Equal(recorder.Body.Bytes(), relayed) {
				t.Fatalf("client did not receive the response as relayed: %q %x", recorder.Header().Get("Content-Encoding"), recorder.Body.Bytes())
			}
			_, record, _ := capturedEvent(t, logPath, dir)
			if record.Response == nil || record.Response.BodyOmitted != netcapture.OmittedEncoding || record.Response.Body != nil {
				t.Fatalf("response = %+v, want its body omitted", record.Response)
			}
			if record.Response.Headers.Get("Content-Encoding") != encoding {
				t.Fatalf("response headers = %v, want them stored", record.Response.Headers)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read capture dir: %v", err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".response.body") {
					t.Errorf("response body stored as %s", entry.Name())
				}
			}
		})
	}
}

func TestProxyCaptureOmitsEncodedRequestBodies(t *testing.T) {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	_, _ = io.WriteString(writer, `{"prompt":"compressed by the client"}`)
	_ = writer.Close()
	sent := buffer.Bytes()
	var upstreamGot []byte
	var upstreamEncoding string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamEncoding = r.Header.Get("Content-Encoding")
		upstreamGot, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	p, dir, logPath := captureProxy(t, []string{"example.com"}, nil, netcapture.Options{})
	p.transport.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	p.transport.DialContext = func(ctx context.Context, network string, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}

	req := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", bytes.NewReader(sent))
	req.Host = "example.com"
	req.Header.Set("Content-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	p.handler("https")(recorder, req)

	if upstreamEncoding != "gzip" || !bytes.Equal(upstreamGot, sent) {
		t.Fatalf("upstream received %q-encoded %x, want the body as sent", upstreamEncoding, upstreamGot)
	}
	_, record, recordPath := capturedEvent(t, logPath, dir)
	if record.Request.BodyOmitted != netcapture.OmittedEncoding || record.Request.Body != nil {
		t.Fatalf("request = %+v, want its body omitted", record.Request)
	}
	if got := readCapturedBody(t, recordPath, record.Response.Body); got != "ok" {
		t.Fatalf("response body = %q", got)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.request.body")); len(matches) != 0 {
		t.Fatalf("request body stored: %v", matches)
	}
}

func TestProxyCaptureRequestsIdentityForClientsWithoutAcceptEncoding(t *testing.T) {
	const placeholder = "ENCLAVE_SECRET_0123456789abcdef"
	const secret = "sk-live-do-not-store-0123456789"
	var upstreamAccepts string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAccepts = r.Header.Get("Accept-Encoding")
		if upstreamAccepts == "identity" {
			_, _ = fmt.Fprintf(w, `{"authorization":%q}`, r.Header.Get("Authorization"))
			return
		}
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
	recorder := httptest.NewRecorder()
	p.handler("https")(recorder, req)

	// Capture asks for identity even when the client sent no encoding header.
	if upstreamAccepts != "identity" || recorder.Header().Get("Content-Encoding") != "" {
		t.Fatalf("upstream saw %q, client got Content-Encoding %q", upstreamAccepts, recorder.Header().Get("Content-Encoding"))
	}
	_, record, recordPath := capturedEvent(t, logPath, dir)
	if record.Request.Headers.Get("Accept-Encoding") != "" {
		t.Fatal("capture changed the original request's encoding header")
	}
	if got := readCapturedBody(t, recordPath, record.Response.Body); got != `{"authorization":"Bearer `+placeholder+`"}` {
		t.Fatalf("captured body = %q", got)
	}
}

func TestProxyCaptureRedactsSecretsUpstreamsWouldCompress(t *testing.T) {
	const placeholder = "ENCLAVE_SECRET_0123456789abcdef"
	const secret = "sk-live-do-not-store-0123456789"
	const clientAccepts = "gzip, deflate, br, zstd"
	for _, encoding := range []string{"gzip", "br", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			var upstreamAccepts string
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamAccepts = r.Header.Get("Accept-Encoding")
				body := fmt.Sprintf(`{"authorization":%q}`, r.Header.Get("Authorization"))
				if !strings.Contains(upstreamAccepts, encoding) {
					_, _ = io.WriteString(w, body)
					return
				}
				// A server that may compress does; the stand-in encoding is
				// enough to show the body would not be stored as sent.
				w.Header().Set("Content-Encoding", encoding)
				writer := gzip.NewWriter(w)
				_, _ = io.WriteString(writer, body)
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
			req.Header.Set("Accept-Encoding", clientAccepts)
			recorder := httptest.NewRecorder()
			p.handler("https")(recorder, req)

			if upstreamAccepts != "identity" {
				t.Fatalf("upstream saw Accept-Encoding %q, want identity", upstreamAccepts)
			}
			if got := recorder.Header().Get("Content-Encoding"); got != "" {
				t.Fatalf("client received Content-Encoding %q", got)
			}
			if got := recorder.Body.String(); got != `{"authorization":"Bearer `+secret+`"}` {
				t.Fatalf("client received %q", got)
			}

			_, record, recordPath := capturedEvent(t, logPath, dir)
			if got := record.Request.Headers.Get("Accept-Encoding"); got != clientAccepts {
				t.Fatalf("captured Accept-Encoding = %q, want the client's", got)
			}
			if got := readCapturedBody(t, recordPath, record.Response.Body); got != `{"authorization":"Bearer `+placeholder+`"}` {
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
		})
	}
}

// A relay error does not turn an intentionally omitted body into an empty
// truncated body. Unencoded partial responses still retain their captured bytes.
func TestProxyCaptureInterruptedResponsesKeepOmittedBodiesAbsent(t *testing.T) {
	for _, encoding := range []string{"", "gzip", "br"} {
		t.Run("encoding="+encoding, func(t *testing.T) {
			const partial = "partial response"
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "100")
				if encoding != "" {
					w.Header().Set("Content-Encoding", encoding)
				}
				_, _ = io.WriteString(w, partial)
			}))
			defer upstream.Close()
			p, dir, logPath := captureProxy(t, []string{"example.com"}, nil, netcapture.Options{})
			p.transport.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			p.transport.DialContext = func(ctx context.Context, network string, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			req := httptest.NewRequest(http.MethodGet, "https://example.com/interrupted", nil)
			req.Host = "example.com"
			req.Header.Set("Accept-Encoding", "identity")
			recorder := httptest.NewRecorder()
			p.handler("https")(recorder, req)
			if recorder.Code != http.StatusOK || recorder.Body.String() != partial {
				t.Fatalf("relayed %d %q", recorder.Code, recorder.Body.String())
			}
			_, record, recordPath := capturedEvent(t, logPath, dir)
			if record.Response == nil {
				t.Fatal("response missing")
			}
			if encoding == "" {
				body := record.Response.Body
				if record.Response.BodyOmitted != "" || body == nil || !body.Truncated || body.Bytes != int64(len(partial)) {
					t.Fatalf("unencoded response = %+v", record.Response)
				}
				if got := readCapturedBody(t, recordPath, body); got != partial {
					t.Fatalf("stored %q", got)
				}
				return
			}
			if record.Response.BodyOmitted != netcapture.OmittedEncoding || record.Response.Body != nil {
				t.Fatalf("encoded response = %+v", record.Response)
			}
			if matches, _ := filepath.Glob(filepath.Join(dir, "*.response.body")); len(matches) != 0 {
				t.Fatalf("omitted body files: %v", matches)
			}
		})
	}
}

func TestProxyCaptureOmitsRepeatedGzipEncodingWithoutClientAcceptEncoding(t *testing.T) {
	const placeholder = "ENCLAVE_SECRET_0123456789abcdef"
	const secret = "synthetic-released-value-not-for-capture"
	var relayed []byte
	var upstreamAccepts string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAccepts = r.Header.Get("Accept-Encoding")
		payload := []byte(r.Header.Get("Authorization"))
		for range 2 {
			var buffer bytes.Buffer
			writer := gzip.NewWriter(&buffer)
			_, _ = writer.Write(payload)
			_ = writer.Close()
			payload = buffer.Bytes()
		}
		relayed = payload
		w.Header().Add("Content-Encoding", "gzip")
		w.Header().Add("Content-Encoding", "gzip")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()
	rules := []model.SecretReleaseEntry{{SecretID: "test", Placeholder: placeholder, Value: secret, Hosts: []string{"example.com"}, Header: "authorization", Format: "Bearer %s"}}
	p, dir, logPath := captureProxy(t, []string{"example.com"}, rules, netcapture.Options{})
	p.transport.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	p.transport.DialContext = func(ctx context.Context, network string, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	req := httptest.NewRequest(http.MethodGet, "https://example.com/repeated-encoding", nil)
	req.Host = "example.com"
	req.Header.Set("Authorization", placeholder)
	recorder := httptest.NewRecorder()
	p.handler("https")(recorder, req)
	_, record, recordPath := capturedEvent(t, logPath, dir)
	if record.Response.Body != nil {
		stored, err := gzip.NewReader(strings.NewReader(readCapturedBody(t, recordPath, record.Response.Body)))
		if err != nil {
			t.Fatalf("unexpected stored body: %v", err)
		}
		defer func() { _ = stored.Close() }()
		decoded, err := io.ReadAll(stored)
		if err != nil {
			t.Fatal(err)
		}
		if string(decoded) == "Bearer "+secret {
			t.Fatal("stored encoded body contains the released value after decompression")
		}
		t.Fatal("encoded response body stored")
	}
	if record.Response.BodyOmitted != netcapture.OmittedEncoding {
		t.Fatalf("response = %+v", record.Response)
	}
	if upstreamAccepts != "identity" || record.Request.Headers.Get("Accept-Encoding") != "" {
		t.Fatalf("upstream accepted %q, original request headers %v", upstreamAccepts, record.Request.Headers)
	}
	if recorder.Code != http.StatusOK || len(recorder.Header().Values("Content-Encoding")) != 2 || !bytes.Equal(recorder.Body.Bytes(), relayed) {
		t.Fatal("encoded response was not relayed unchanged")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.response.body")); len(matches) != 0 {
		t.Fatalf("omitted body files: %v", matches)
	}
}

func TestAuditTruncationPreservesRelayedAndCapturedURL(t *testing.T) {
	seen := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen <- req.URL.RequestURI()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	host, hostPort := upstreamHost(t, upstream)
	p, dir, logPath := captureProxy(t, []string{host}, nil, netcapture.Options{})
	path := "/" + strings.Repeat("x", 32*1024) + "?query=kept"
	wantURL := "http://" + hostPort + path
	req := httptest.NewRequest(http.MethodGet, wantURL, nil)
	p.handler("http")(httptest.NewRecorder(), req)
	event, record, _ := capturedEvent(t, logPath, dir)
	if !strings.HasSuffix(event.Path, "...[truncated]") || req.URL.String() != wantURL || record.Request.URL != wantURL {
		t.Fatal("audit truncation changed the request or captured URL")
	}
	if got := <-seen; got != path {
		t.Fatal("audit truncation changed the relayed URL")
	}
}

// wsFrame builds a WebSocket frame; first is its first header byte. Client
// frames are masked, as the protocol requires.
func wsFrame(first byte, payload string, masked bool) []byte {
	second := byte(len(payload))
	if masked {
		second |= 0x80
	}
	frame := []byte{first, second}
	if !masked {
		return append(frame, payload...)
	}
	mask := [4]byte{0xa1, 0xb2, 0xc3, 0xd4}
	frame = append(frame, mask[:]...)
	for i := range len(payload) {
		frame = append(frame, payload[i]^mask[i%4])
	}
	return frame
}

// websocketUpstream accepts a WebSocket handshake, and permessage-deflate if
// the handshake offers it, reads want bytes and hands them to received,
// answers with reply, and then waits for the other side to close.
func websocketUpstream(want int, reply []byte, received chan<- []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		head := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"
		if strings.Contains(r.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
			head += "Sec-WebSocket-Extensions: permessage-deflate\r\n"
		}
		_, _ = rw.WriteString(head + "\r\n")
		_ = rw.Flush()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, want)
		n, _ := io.ReadFull(rw, got)
		received <- got[:n]
		_, _ = rw.Write(reply)
		_ = rw.Flush()
		_, _ = io.Copy(io.Discard, rw)
	}
}

// openWebSocket sends a WebSocket handshake for host through the proxy, offering
// permessage-deflate, and returns the connection, its reader and the
// handshake response.
func openWebSocket(t *testing.T, proxyServer *httptest.Server, host string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxyServer.URL, "http://"))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	handshake := "GET /responses HTTP/1.1\r\nHost: " + host + "\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n"
	if _, err := io.WriteString(conn, handshake); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake response = %v, %v", resp, err)
	}
	return conn, reader, resp
}

func describeMessages(messages []netcapture.Message) string {
	var lines []string
	for _, m := range messages {
		lines = append(lines, m.From+" "+m.Type+" "+m.Text+string(m.Base64))
	}
	return strings.Join(lines, "\n")
}

func TestProxyCapturesWebSocketMessagesWithoutChangingThem(t *testing.T) {
	clientSends := bytes.Join([][]byte{
		wsFrame(0x81, "hello", true),
		wsFrame(0x01, "Hello, ", true),
		wsFrame(0x89, "p", true),
		wsFrame(0x80, "world", true),
		wsFrame(0x88, "\x03\xe8done", true),
	}, nil)
	serverSends := bytes.Join([][]byte{
		wsFrame(0x81, "welcome", false),
		wsFrame(0x82, "\x01\x02\x03", false),
		wsFrame(0x88, "\x03\xe8", false),
	}, nil)

	for _, captured := range []bool{false, true} {
		received := make(chan []byte, 1)
		upstream := httptest.NewServer(websocketUpstream(len(clientSends), serverSends, received))
		host, hostPort := upstreamHost(t, upstream)
		p := newProxy([]string{host}, nil, nil, false, nil, testSession)
		var dir, logPath string
		if captured {
			p, dir, logPath = captureProxy(t, []string{host}, nil, netcapture.Options{})
		}
		proxyServer := httptest.NewServer(p.handler("http"))

		conn, reader, handshake := openWebSocket(t, proxyServer, hostPort)
		// A captured connection offers the upstream no extension, so it
		// accepts none and the messages cross the proxy uncompressed.
		accepted := handshake.Header.Get("Sec-WebSocket-Extensions")
		if want := map[bool]string{false: "permessage-deflate", true: ""}[captured]; accepted != want {
			t.Fatalf("captured=%v: handshake accepted extensions %q, want %q", captured, accepted, want)
		}
		if _, err := conn.Write(clientSends); err != nil {
			t.Fatalf("write frames: %v", err)
		}
		clientGot := make([]byte, len(serverSends))
		if _, err := io.ReadFull(reader, clientGot); err != nil {
			t.Fatalf("captured=%v: read relayed frames: %v", captured, err)
		}
		if upstreamGot := <-received; !bytes.Equal(upstreamGot, clientSends) {
			t.Fatalf("captured=%v: upstream received %x, want %x", captured, upstreamGot, clientSends)
		}
		if !bytes.Equal(clientGot, serverSends) {
			t.Fatalf("captured=%v: client received %x, want %x", captured, clientGot, serverSends)
		}
		_ = conn.Close()
		if !captured {
			proxyServer.Close()
			upstream.Close()
			continue
		}

		waitForEvents(t, logPath, 1)
		event, record, recordPath := capturedEvent(t, logPath, dir)
		proxyServer.Close()
		upstream.Close()
		if event.Status != http.StatusSwitchingProtocols || record.WebSocket == nil || record.WebSocket.Open {
			t.Fatalf("event %+v, record websocket %+v", event, record.WebSocket)
		}
		_, messages, err := netcapture.ReadMessages(recordPath, record.WebSocket)
		if err != nil {
			t.Fatalf("ReadMessages() error = %v", err)
		}
		// Each side's messages keep their order. Between the sides the lines
		// follow the order of storing, which can lag the relay; the timestamps,
		// taken as each message was relayed, put every client message before the
		// reply the upstream sent only after reading them all.
		var fromClient, fromServer []netcapture.Message
		for _, m := range messages {
			if m.From == netcapture.FromClient {
				fromClient = append(fromClient, m)
			} else {
				fromServer = append(fromServer, m)
			}
		}
		wantClient := strings.Join([]string{
			"client text hello",
			"client ping p",
			"client text Hello, world",
			"client close done",
		}, "\n")
		wantServer := strings.Join([]string{
			"server text welcome",
			"server binary \x01\x02\x03",
			"server close ",
		}, "\n")
		if got := describeMessages(fromClient); got != wantClient {
			t.Fatalf("captured client messages =\n%s\nwant\n%s", got, wantClient)
		}
		if got := describeMessages(fromServer); got != wantServer {
			t.Fatalf("captured server messages =\n%s\nwant\n%s", got, wantServer)
		}
		lastClient, err := time.Parse(netlog.TimeFormat, fromClient[len(fromClient)-1].Timestamp)
		if err != nil {
			t.Fatalf("client timestamp: %v", err)
		}
		firstServer, err := time.Parse(netlog.TimeFormat, fromServer[0].Timestamp)
		if err != nil {
			t.Fatalf("server timestamp: %v", err)
		}
		if lastClient.After(firstServer) {
			t.Fatalf("last client message stamped %s, after the reply at %s", lastClient, firstServer)
		}
		if socket := record.WebSocket; socket.Bytes != int64(len(clientSends)+len(serverSends)) || socket.Messages != 7 {
			t.Fatalf("record websocket = %+v", socket)
		}
	}
}

func TestProxyCaptureOmitsWebSocketMessagesOfAnExtensionNegotiatedAnyway(t *testing.T) {
	// This upstream accepts permessage-deflate although the handshake it
	// received offered no extension.
	sends := wsFrame(0xc1, "\xf2\x48\xcd\xc9\xc9\x07\x00", true)
	reply := wsFrame(0xc1, "\xf2\x48\xcd\xc9\xc9\x07\x00", false)
	received := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n")
		_ = rw.Flush()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, len(sends))
		n, _ := io.ReadFull(rw, got)
		received <- got[:n]
		_, _ = rw.Write(reply)
		_ = rw.Flush()
		_, _ = io.Copy(io.Discard, rw)
	}))
	defer upstream.Close()
	host, hostPort := upstreamHost(t, upstream)
	p, dir, logPath := captureProxy(t, []string{host}, nil, netcapture.Options{})
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()

	conn, reader, _ := openWebSocket(t, proxyServer, hostPort)
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(sends); err != nil {
		t.Fatalf("write frames: %v", err)
	}
	clientGot := make([]byte, len(reply))
	if _, err := io.ReadFull(reader, clientGot); err != nil {
		t.Fatalf("read relayed frames: %v", err)
	}
	if upstreamGot := <-received; !bytes.Equal(upstreamGot, sends) || !bytes.Equal(clientGot, reply) {
		t.Fatalf("frames not relayed unchanged: upstream %x, client %x", upstreamGot, clientGot)
	}
	open, err := filepath.Glob(filepath.Join(dir, "*.open.json"))
	if err != nil || len(open) != 1 {
		t.Fatalf("live omitted handshake has %d open records: %v", len(open), err)
	}
	live, err := netcapture.Load(open[0])
	if err != nil {
		t.Fatal(err)
	}
	if live.WebSocket == nil || !live.WebSocket.Open || live.WebSocket.MessagesOmitted != netcapture.OmittedExtension || live.WebSocket.File != "" {
		t.Fatalf("live omission websocket = %+v", live.WebSocket)
	}
	_ = conn.Close()

	waitForEvents(t, logPath, 1)
	_, record, _ := capturedEvent(t, logPath, dir)
	if record.WebSocket == nil || record.WebSocket.Open || record.WebSocket.MessagesOmitted != netcapture.OmittedExtension || record.WebSocket.File != "" {
		t.Fatalf("record websocket = %+v", record.WebSocket)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.open.json")); len(matches) != 0 {
		t.Fatalf("open records after close: %v", matches)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.messages.jsonl")); len(matches) != 0 {
		t.Fatalf("messages stored: %v", matches)
	}
}

func TestProxyShowsAnOpenWebSocketAndFinishesItAtShutdown(t *testing.T) {
	request := wsFrame(0x81, "first turn", true)
	received := make(chan []byte, 1)
	upstream := httptest.NewServer(websocketUpstream(len(request), wsFrame(0x81, "first reply", false), received))
	defer upstream.Close()
	host, hostPort := upstreamHost(t, upstream)
	p, dir, logPath := captureProxy(t, []string{host}, nil, netcapture.Options{})
	proxyServer := httptest.NewServer(p.handler("http"))
	defer proxyServer.Close()

	conn, reader, _ := openWebSocket(t, proxyServer, hostPort)
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	<-received
	if _, err := reader.ReadString('y'); err != nil {
		t.Fatalf("read reply: %v", err)
	}

	// While the connection is open the record waits under its open name, with
	// the messages so far next to it, and nothing is logged yet.
	var openRecord string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.open.json"))
		if len(matches) == 1 {
			record, err := netcapture.Load(matches[0])
			if err == nil {
				if _, messages, _ := netcapture.ReadMessages(matches[0], record.WebSocket); len(messages) == 2 {
					openRecord = matches[0]
					break
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if openRecord == "" {
		t.Fatal("no open record with both messages while the connection is open")
	}
	if events := readAuditEvents(t, logPath); len(events) != 0 {
		t.Fatalf("events logged before the connection closed: %+v", events)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.exchanges.closeUpgraded()
	p.exchanges.wait(ctx)

	event, record, recordPath := capturedEvent(t, logPath, dir)
	if event.Status != http.StatusSwitchingProtocols || recordPath != strings.TrimSuffix(openRecord, ".open.json")+".json" {
		t.Fatalf("event %+v, record %s", event, recordPath)
	}
	if record.WebSocket == nil || record.WebSocket.Open || record.WebSocket.StoredMessages != 2 {
		t.Fatalf("record websocket = %+v", record.WebSocket)
	}
	if _, err := os.Stat(openRecord); !os.IsNotExist(err) {
		t.Fatalf("open record left behind: %v", err)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("the client connection is still open after shutdown")
	}
}
