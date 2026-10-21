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
	"crypto/tls"
	"crypto/x509"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// rawRequest carries no User-Agent and no Accept-Encoding, so anything the
// proxy adds shows up at the upstream.
const rawRequest = "GET /path HTTP/1.1\r\nHost: " + interceptedHost + "\r\nX-Client: 1\r\nConnection: close\r\n\r\n"

func startHeaderRecordingUpstream(t *testing.T, tlsServer bool) (*httptest.Server, <-chan http.Header) {
	t.Helper()
	seen := make(chan http.Header, 2)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
	}))
	if tlsServer {
		upstream.StartTLS()
	} else {
		upstream.Start()
	}
	t.Cleanup(upstream.Close)
	return upstream, seen
}

// exchangeRaw sends rawRequest over conn and returns the response as received.
func exchangeRaw(t *testing.T, conn net.Conn) *http.Response {
	t.Helper()
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.WriteString(conn, rawRequest); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp
}

func dialPlain(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func dialInterceptedTLS(t *testing.T, addr string, roots *x509.CertPool) net.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: interceptedHost, RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("TLS dial %s: %v", addr, err)
	}
	return conn
}

func receivedHeaders(t *testing.T, seen <-chan http.Header) http.Header {
	t.Helper()
	select {
	case h := <-seen:
		return h
	case <-time.After(15 * time.Second):
		t.Fatal("upstream received no request")
		return nil
	}
}

// An observing proxy adds nothing to a request: the upstream sees the same
// headers as from a client that reached it directly.
func TestObservingProxyForwardsRequestHeadersUnchanged(t *testing.T) {
	t.Run("plain HTTP", func(t *testing.T) {
		upstream, seen := startHeaderRecordingUpstream(t, false)
		p, _ := newUnrestrictedTestProxy(t)
		p.originalDst = fixedOriginalDestination(t, upstream.Listener.Addr().String())
		proxyAddr := serveHTTP(t, p)

		exchangeRaw(t, dialPlain(t, upstream.Listener.Addr().String()))
		direct := receivedHeaders(t, seen)
		exchangeRaw(t, dialPlain(t, proxyAddr))
		assertSameHeaders(t, receivedHeaders(t, seen), direct)
	})
	t.Run("intercepted HTTPS", func(t *testing.T) {
		upstream, seen := startHeaderRecordingUpstream(t, true)
		p, _ := newUnrestrictedRequestsProxy(t, upstream)
		proxyAddr, gatewayCA := startHTTPSDispatch(t, p)
		upstreamRoots := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

		exchangeRaw(t, dialInterceptedTLS(t, upstream.Listener.Addr().String(), upstreamRoots))
		direct := receivedHeaders(t, seen)
		exchangeRaw(t, dialInterceptedTLS(t, proxyAddr, gatewayCA))
		assertSameHeaders(t, receivedHeaders(t, seen), direct)
	})
}

func assertSameHeaders(t *testing.T, got, want http.Header) {
	t.Helper()
	if !maps.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("upstream headers via proxy = %v, want as sent directly %v", got, want)
	}
}

// An observing proxy hands the client the response body exactly as the
// upstream encoded it.
func TestObservingProxyForwardsEncodedResponseUnchanged(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, _ = zw.Write([]byte("hello"))
	_ = zw.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(compressed.Len()))
		_, _ = w.Write(compressed.Bytes())
	}))
	defer upstream.Close()
	p, _ := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, upstream.Listener.Addr().String())

	resp := exchangeRaw(t, dialPlain(t, serveHTTP(t, p)))
	body, _ := io.ReadAll(resp.Body)

	if resp.Header.Get("Content-Encoding") != "gzip" || resp.ContentLength != int64(compressed.Len()) || !bytes.Equal(body, compressed.Bytes()) {
		t.Fatalf("response Content-Encoding=%q Content-Length=%d body=%q, want gzip %d %q",
			resp.Header.Get("Content-Encoding"), resp.ContentLength, body, compressed.Len(), compressed.Bytes())
	}
}

// An observing proxy adds no Date or sniffed Content-Type to a response
// whose upstream sent neither.
func TestObservingProxyAddsNoResponseHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Date"] = nil
		w.Header()["Content-Type"] = nil
		_, _ = io.WriteString(w, "<html>hello</html>")
	}))
	defer upstream.Close()
	p, _ := newUnrestrictedTestProxy(t)
	p.originalDst = fixedOriginalDestination(t, upstream.Listener.Addr().String())

	resp := exchangeRaw(t, dialPlain(t, serveHTTP(t, p)))

	for _, name := range []string{"Date", "Content-Type"} {
		if values, ok := resp.Header[name]; ok {
			t.Errorf("response carries %s %q, which the upstream did not send", name, values)
		}
	}
}

// A slow response is still a response: an observing proxy waits for it as
// long as a client without a gateway would.
func TestObservingProxyHasNoResponseHeaderTimeout(t *testing.T) {
	p, _ := newUnrestrictedTestProxy(t)
	dst := netip.MustParseAddrPort("192.0.2.1:443")
	for _, upgrade := range []bool{false, true} {
		if timeout := p.transportFor(dst, upgrade).ResponseHeaderTimeout; timeout != 0 {
			t.Fatalf("ResponseHeaderTimeout (upgrade=%v) = %v, want none", upgrade, timeout)
		}
	}
}

// Restricted mode keeps its forwarding headers and its timeout.
func TestRestrictedProxyAddsForwardingHeaders(t *testing.T) {
	upstream, seen := startHeaderRecordingUpstream(t, false)
	p := newProxy([]string{"127.0.0.1"}, nil, nil, false, nil, testSession)
	if p.transport.ResponseHeaderTimeout != 120*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 120s", p.transport.ResponseHeaderTimeout)
	}
	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/", nil)
	req.Host = upstream.Listener.Addr().String()
	if _, err := p.forward(httptest.NewRecorder(), req, "http", nil); err != nil {
		t.Fatalf("forward() error = %v", err)
	}

	got := receivedHeaders(t, seen)
	if got.Get("X-Forwarded-Proto") != "http" || got.Get("X-Forwarded-For") == "" {
		t.Fatalf("upstream headers = %v, want X-Forwarded-Proto and X-Forwarded-For", got)
	}
}

// startIdentifiedTLSUpstream answers every request with name. All httptest
// servers share one certificate, which covers interceptedHost.
func startIdentifiedTLSUpstream(t *testing.T, name string, http2 bool) *httptest.Server {
	t.Helper()
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Proto", r.Proto)
		_, _ = io.WriteString(w, name)
	}))
	upstream.EnableHTTP2 = http2
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	return upstream
}

// With --network-log=requests, a hostname pinned to another address reaches
// that address, even while the proxy holds a connection to the first one for
// the same hostname.
func TestObservingProxyPoolsInterceptedConnectionsPerOriginalDestination(t *testing.T) {
	for _, tc := range []struct {
		name  string
		http2 bool
		proto string
	}{
		{"HTTP/1.1", false, "HTTP/1.1"},
		{"HTTP/2", true, "HTTP/2.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := startIdentifiedTLSUpstream(t, "first", tc.http2)
			second := startIdentifiedTLSUpstream(t, "second", tc.http2)
			p, _ := newUnrestrictedRequestsProxy(t, first)
			var dst atomic.Value
			p.originalDst = func(net.Conn) (netip.AddrPort, error) { return dst.Load().(netip.AddrPort), nil }
			proxyAddr, gatewayCA := startHTTPSDispatch(t, p)

			for _, upstream := range []*httptest.Server{first, second, first} {
				dst.Store(netip.MustParseAddrPort(upstream.Listener.Addr().String()))
				// A fresh client opens a fresh connection, as curl --resolve does.
				resp, err := sessionClient(proxyAddr, gatewayCA).Get("https://" + interceptedHost + "/")
				if err != nil {
					t.Fatalf("request through proxy: %v", err)
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				want := map[*httptest.Server]string{first: "first", second: "second"}[upstream]
				if string(body) != want || resp.Header.Get("X-Upstream-Proto") != tc.proto {
					t.Fatalf("reached %q over %s, want %q over %s", body, resp.Header.Get("X-Upstream-Proto"), want, tc.proto)
				}
			}
		})
	}
}

// Transports of destinations no request used for longer than the idle timeout
// are released.
func TestObservingProxyReleasesIdleDestinationTransports(t *testing.T) {
	p, _ := newUnrestrictedTestProxy(t)
	now := time.Now()
	p.destinations.now = func() time.Time { return now }
	stale := netip.MustParseAddrPort("192.0.2.1:443")
	fresh := netip.MustParseAddrPort("192.0.2.2:443")

	p.transportFor(stale, false)
	now = now.Add(p.transport.IdleConnTimeout + time.Second)
	p.transportFor(fresh, false)

	if _, ok := p.destinations.byDestination[stale]; ok {
		t.Fatal("transport of an idle destination was kept")
	}
	if _, ok := p.destinations.byDestination[fresh]; !ok {
		t.Fatal("transport of a destination in use was released")
	}
}
