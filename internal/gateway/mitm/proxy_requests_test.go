// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
	"enclave/internal/netlog"
)

// interceptedHost is covered by the httptest server certificate. The proxy never
// resolves it: it reaches the upstream only through the original destination.
const interceptedHost = "example.com"

const testPlaceholder = "ENCLAVE_SECRET_0123456789abcdef"

// placeholderRule would release a secret to interceptedHost in restricted mode.
func placeholderRule() []model.SecretReleaseEntry {
	return []model.SecretReleaseEntry{{
		Placeholder: testPlaceholder,
		Value:       "real-secret",
		Hosts:       []string{interceptedHost},
		Header:      "authorization",
	}}
}

type seenRequest struct {
	host, path, query, authorization string
}

func startRecordingTLSUpstream(t *testing.T) (*httptest.Server, <-chan seenRequest) {
	t.Helper()
	seen := make(chan seenRequest, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- seenRequest{r.Host, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	return upstream, seen
}

// startDispatch runs the HTTPS frontend with a gateway CA and returns its
// address and the CA the session would trust.
func startHTTPSDispatch(t *testing.T, p *proxy) (string, *x509.CertPool) {
	t.Helper()
	store := tlsstore.New(t.TempDir())
	if err := store.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA() error = %v", err)
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
		t.Fatalf("newHTTPSDispatchServer() error = %v", err)
	}
	go func() { _ = server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return listener.Addr().String(), gatewayCA
}

// sessionClient sends every connection to the proxy, as the gateway's
// redirect does, and trusts only roots.
func sessionClient(proxyAddr string, roots *x509.CertPool) *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, proxyAddr)
			},
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		},
	}
}

func trustUpstream(p *proxy, upstream *httptest.Server) {
	trust := upstream.Client().Transport.(*http.Transport).TLSClientConfig
	p.transport.TLSClientConfig = trust.Clone()
	p.upgradeTransport.TLSClientConfig = trust.Clone()
}

func newUnrestrictedRequestsProxy(t *testing.T, upstream *httptest.Server) (*proxy, string) {
	t.Helper()
	p, logPath := newUnrestrictedTestProxy(t)
	p.forceHTTPSMITM = true
	p.originalDst = fixedOriginalDestination(t, upstream.Listener.Addr().String())
	trustUpstream(p, upstream)
	return p, logPath
}

func awaitUpstreamRequest(t *testing.T, seen <-chan seenRequest) seenRequest {
	t.Helper()
	select {
	case got := <-seen:
		return got
	case <-time.After(15 * time.Second):
		t.Fatal("upstream received no request")
		return seenRequest{}
	}
}

// With --network-log=requests an unrestricted proxy terminates TLS for any
// host, forwards each request to the address the client dialled, and logs it
// as an http event without its query string.
func TestUnrestrictedRequestsModeLogsHTTPSRequestsAndDialsOriginalDestination(t *testing.T) {
	upstream, seen := startRecordingTLSUpstream(t)
	p, logPath := newUnrestrictedRequestsProxy(t, upstream)
	proxyAddr, gatewayCA := startHTTPSDispatch(t, p)

	resp, err := sessionClient(proxyAddr, gatewayCA).Get("https://" + interceptedHost + "/path?token=hidden")
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("response = %d %q, want 200 ok", resp.StatusCode, body)
	}
	if got := awaitUpstreamRequest(t, seen); got.host != interceptedHost || got.path != "/path" || got.query != "token=hidden" {
		t.Fatalf("upstream saw %+v, want Host %s /path?token=hidden", got, interceptedHost)
	}
	events := waitForAuditEvents(t, logPath, 2)
	if len(events) != 2 {
		t.Fatalf("audit events = %+v, want 2", events)
	}
	dispatch, request := events[0], events[1]
	if dispatch.Type != netlog.TypeTCP || dispatch.Verdict != netlog.VerdictPass || dispatch.Rule != "mitm-dispatch" ||
		dispatch.Domain != interceptedHost || dispatch.Session != testSession {
		t.Fatalf("first event = %+v, want tcp pass mitm-dispatch for %s", dispatch, interceptedHost)
	}
	if request.Type != netlog.TypeHTTP || request.Verdict != netlog.VerdictPass || request.Rule != netlog.RuleUnrestricted ||
		request.Domain != interceptedHost || request.Path != "/path" || request.Status != http.StatusOK || request.Session != testSession {
		t.Fatalf("second event = %+v, want http pass rule=unrestricted for %s/path", request, interceptedHost)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("hidden")) {
		t.Fatalf("network log contains the query string: %s", raw)
	}
}

// An unrestricted session holds real secret values, so its gateway releases
// nothing: placeholders pass unchanged and never cause a denial.
func TestUnrestrictedRequestsModeReleasesNoSecrets(t *testing.T) {
	upstream, seen := startRecordingTLSUpstream(t)
	p, _ := newUnrestrictedRequestsProxy(t, upstream)
	p.secretRules = placeholderRule()
	proxyAddr, gatewayCA := startHTTPSDispatch(t, p)

	req, err := http.NewRequest(http.MethodGet, "https://"+interceptedHost+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", testPlaceholder)
	resp, err := sessionClient(proxyAddr, gatewayCA).Do(req)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := awaitUpstreamRequest(t, seen); got.authorization != testPlaceholder {
		t.Fatalf("upstream Authorization = %q, want the placeholder unchanged", got.authorization)
	}
}

// In the default coarse mode an unrestricted proxy never terminates TLS, not
// even for hosts a secret rule names: the client sees the upstream's own
// certificate.
func TestUnrestrictedCoarseModeNeverTerminatesTLS(t *testing.T) {
	upstream, _ := startRecordingTLSUpstream(t)
	p, logPath := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, upstream.Listener.Addr().String())
	p.secretRules = placeholderRule()
	proxyAddr, _ := startHTTPSDispatch(t, p)
	upstreamRoots := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	resp, err := sessionClient(proxyAddr, upstreamRoots).Get("https://" + interceptedHost + "/")
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	_ = resp.Body.Close()

	if leaf := resp.TLS.PeerCertificates[0]; !leaf.Equal(upstream.Certificate()) {
		t.Fatalf("client saw certificate %q, want the upstream's", leaf.Subject)
	}
	assertSingleTCPPass(t, logPath, interceptedHost, 443)
}
