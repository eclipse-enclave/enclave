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
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/cryptobyte"

	"enclave/internal/domainpattern"
	"enclave/internal/gateway/exchange"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
	"enclave/internal/netcapture"
	"enclave/internal/netlog"
	"enclave/internal/util"
)

type Options struct {
	// HTTPPort and HTTPSPort are the loopback ports the proxy listens on; zero
	// selects the gateway's ports.
	HTTPPort       int
	HTTPSPort      int
	AllowedDomains []string
	DeniedDomains  []string
	SecretRules    []model.SecretReleaseEntry
	NativeAuth     *exchange.Store
	AuditLogPath   string
	// CaptureDir is the session directory full captures are written to; empty
	// disables capturing.
	CaptureDir string
	// Session names the gateway that produced an event. Sessions of the same
	// project and tool share one log file, so without it concurrent sessions
	// cannot be told apart.
	Session string
	// NetworkMode is the session's network mode; the proxy derives what it
	// enforces from it. Anything but unrestricted counts as restricted.
	NetworkMode    string
	ForceHTTPSMITM bool
	TLSStore       *tlsstore.Store
	OnReady        func()
}

const serverShutdownTimeout = 1 * time.Second

// ruleUpstreamError marks a passed connection or request the proxy could not
// deliver.
const ruleUpstreamError = "upstream-error"

const maxTLSRecordsBeforeClientHelloLength = 16

func LoadRules(path string) ([]model.SecretReleaseEntry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path comes from controlled runtime env.
	if err != nil {
		return nil, fmt.Errorf("read secret release rules: %w", err)
	}
	var rules []model.SecretReleaseEntry
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("parse secret release rules: %w", err)
	}
	normalizedRules := make([]model.SecretReleaseEntry, 0, len(rules))
	for _, rule := range rules {
		normalized, err := normalizeRule(rule)
		if err != nil {
			return nil, err
		}
		normalizedRules = append(normalizedRules, normalized)
	}
	return normalizedRules, nil
}

func Run(ctx context.Context, opts Options) error {
	if opts.TLSStore == nil {
		return fmt.Errorf("TLS store is required")
	}
	httpPort := opts.HTTPPort
	if httpPort == 0 {
		httpPort = model.GatewayProxyHTTPPort
	}
	httpsPort := opts.HTTPSPort
	if httpsPort == 0 {
		httpsPort = model.GatewayProxyHTTPSPort
	}

	audit, err := netlog.NewAppender(opts.AuditLogPath)
	if err != nil {
		return fmt.Errorf("open proxy audit log: %w", err)
	}
	defer func() { _ = audit.Close() }()

	proxy := newProxy(opts.AllowedDomains, opts.DeniedDomains, opts.SecretRules, opts.ForceHTTPSMITM, audit, opts.Session)
	proxy.setCapabilities(capabilitiesFor(opts.NetworkMode))
	proxy.nativeAuth = opts.NativeAuth
	proxy.captures, err = netcapture.Open(netcapture.Options{
		Dir:     opts.CaptureDir,
		Session: opts.Session,
		Secrets: captureSecrets(opts.SecretRules),
	})
	if err != nil {
		// Capturing is an audit aid. A store that cannot be opened, for example
		// because its directory was deleted on the host, must not take egress
		// down with it: a proxy that fails to start shuts the gateway.
		log.Printf("full network capture disabled: %v", err)
	}
	httpServer := proxy.newHTTPServer()
	httpListener, err := listenLoopback(httpPort)
	if err != nil {
		return fmt.Errorf("listen HTTP proxy on port %d: %w", httpPort, err)
	}

	httpsListener, err := listenLoopback(httpsPort)
	if err != nil {
		_ = httpListener.Close()
		return fmt.Errorf("listen HTTPS proxy on port %d: %w", httpsPort, err)
	}
	httpsServer, err := newHTTPSDispatchServer(proxy, opts.TLSStore, httpsListener)
	if err != nil {
		_ = httpListener.Close()
		_ = httpsListener.Close()
		return fmt.Errorf("create HTTPS dispatch server: %w", err)
	}

	errCh := make(chan error, 2)
	go func() {
		if err := proxy.serveHTTP(httpServer, httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			errCh <- fmt.Errorf("serve HTTP proxy: %w", err)
		}
	}()
	go func() {
		if err := httpsServer.Serve(); err != nil && !errors.Is(err, net.ErrClosed) {
			errCh <- fmt.Errorf("serve HTTPS proxy: %w", err)
		}
	}()
	if opts.OnReady != nil {
		opts.OnReady()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		// Closing the upgraded connections first lets their exchanges be logged
		// and captured while the servers shut down.
		proxy.exchanges.closeUpgraded()
		_ = httpServer.Shutdown(shutdownCtx)
		_ = httpsServer.Shutdown(shutdownCtx)
		proxy.exchanges.wait(shutdownCtx)
		return nil
	case err := <-errCh:
		_ = httpServer.Close()
		_ = httpsServer.Shutdown(context.Background())
		return err
	}
}

// capabilities are what the proxy does beyond logging. They are derived from
// the session's network mode: restricted enforces, unrestricted only observes.
type capabilities struct {
	enforceAllowlist bool
	// dialOriginalDestination sends each connection to the address the client
	// dialled instead of re-resolving its SNI or Host, and tunnels traffic the
	// proxy cannot parse there unchanged.
	dialOriginalDestination bool
	// releaseSecrets swaps placeholders for real values. An unrestricted
	// session already holds the real values.
	releaseSecrets bool
	// forwardUnchanged sends requests and responses on as they were sent, so
	// neither end can tell a proxy sat in between.
	forwardUnchanged bool
}

func capabilitiesFor(networkMode string) capabilities {
	restricted := networkMode != model.NetworkModeUnrestricted
	return capabilities{
		enforceAllowlist:        restricted,
		dialOriginalDestination: !restricted,
		releaseSecrets:          restricted,
		forwardUnchanged:        !restricted,
	}
}

// setCapabilities configures the proxy for caps.
func (p *proxy) setCapabilities(caps capabilities) {
	p.caps = caps
	if caps.forwardUnchanged {
		for _, transport := range []*http.Transport{p.transport, p.upgradeTransport} {
			// Left on, the transport asks for gzip on the client's behalf and
			// decompresses the response, and turns a slow response into a 502.
			transport.DisableCompression = true
			transport.ResponseHeaderTimeout = 0
		}
	}
	if caps.dialOriginalDestination {
		// A custom dialer turns off the HTTP/2 the transport otherwise
		// negotiates; the upgrade transport is pinned to HTTP/1.1 regardless.
		p.transport.ForceAttemptHTTP2 = true
		p.destinations = newDestinationTransports(p.transport, p.upgradeTransport)
	}
}

// transportFor returns the transport for a request to dst, or for a request
// whose target is in its URL when dst is invalid.
func (p *proxy) transportFor(dst netip.AddrPort, upgrade bool) *http.Transport {
	if dst.IsValid() && p.destinations != nil {
		return p.destinations.get(dst, upgrade)
	}
	if upgrade {
		return p.upgradeTransport
	}
	return p.transport
}

// destinationTransports keeps a pair of transports per original destination.
// A transport pools connections by the name in the URL, which intercepted HTTPS
// keeps for certificate checks; one shared transport would send a request to
// a connection dialled for another address of the same name.
type destinationTransports struct {
	template, upgradeTemplate *http.Transport
	now                       func() time.Time

	mu            sync.Mutex
	byDestination map[netip.AddrPort]*destinationTransport
	lastSweep     time.Time
}

type destinationTransport struct {
	transport, upgradeTransport *http.Transport
	lastUsed                    time.Time
}

func newDestinationTransports(template, upgradeTemplate *http.Transport) *destinationTransports {
	return &destinationTransports{
		template:        template,
		upgradeTemplate: upgradeTemplate,
		now:             time.Now,
		byDestination:   map[netip.AddrPort]*destinationTransport{},
	}
}

func (d *destinationTransports) get(dst netip.AddrPort, upgrade bool) *http.Transport {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.releaseIdle(now)
	entry, ok := d.byDestination[dst]
	if !ok {
		entry = &destinationTransport{
			transport:        dialingOnly(d.template, dst),
			upgradeTransport: dialingOnly(d.upgradeTemplate, dst),
		}
		d.byDestination[dst] = entry
	}
	entry.lastUsed = now
	if upgrade {
		return entry.upgradeTransport
	}
	return entry.transport
}

// releaseIdle drops the transports of destinations no request used for longer
// than the idle timeout, after which their pooled connections are closed
// anyway. Requests still in flight keep their transport until they end.
func (d *destinationTransports) releaseIdle(now time.Time) {
	idle := d.template.IdleConnTimeout
	if now.Sub(d.lastSweep) < idle {
		return
	}
	d.lastSweep = now
	for dst, entry := range d.byDestination {
		if now.Sub(entry.lastUsed) > idle {
			entry.transport.CloseIdleConnections()
			entry.upgradeTransport.CloseIdleConnections()
			delete(d.byDestination, dst)
		}
	}
}

// dialingOnly returns a copy of template that dials dst for every request.
func dialingOnly(template *http.Transport, dst netip.AddrPort) *http.Transport {
	transport := template.Clone()
	transport.DialContext = func(ctx context.Context, network string, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, dst.String())
	}
	return transport
}

// releaseRules are the secret release rules the proxy applies.
func (p *proxy) releaseRules() []model.SecretReleaseEntry {
	if !p.caps.releaseSecrets {
		return nil
	}
	return p.secretRules
}

type proxy struct {
	caps           capabilities
	allowedDomains []string
	deniedDomains  []string
	secretRules    []model.SecretReleaseEntry
	nativeAuth     *exchange.Store
	forceHTTPSMITM bool
	audit          *netlog.Appender
	session        string
	// originalDst reports where the client sent a redirected connection.
	originalDst func(net.Conn) (netip.AddrPort, error)
	// captures is nil unless --network-log=full asked for full captures.
	captures  *netcapture.Store
	exchanges exchangeTracker
	transport *http.Transport
	// upgradeTransport speaks only HTTP/1.1 and carries every request with an
	// Upgrade header, whether or not Connection names it: HTTP/2 refuses any
	// Upgrade header, and transport switches to HTTP/1.1 by itself only for
	// WebSocket, not for upgrades such as SPDY.
	upgradeTransport *http.Transport
	// destinations holds the transports of a proxy that dials original
	// destinations; transport and upgradeTransport serve as their templates.
	destinations *destinationTransports
}

func normalizeHostPatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		value, err := domainpattern.NormalizeHost(pattern)
		if err != nil || value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func newProxy(allowedDomains []string, deniedDomains []string, secretRules []model.SecretReleaseEntry, forceHTTPSMITM bool, audit *netlog.Appender, session string) *proxy {
	upgradeTransport := newUpstreamTransport()
	upgradeTransport.Protocols = new(http.Protocols)
	upgradeTransport.Protocols.SetHTTP1(true)
	return &proxy{
		caps:             capabilitiesFor(model.NetworkModeRestricted),
		allowedDomains:   normalizeHostPatterns(allowedDomains),
		deniedDomains:    normalizeHostPatterns(deniedDomains),
		secretRules:      secretRules,
		forceHTTPSMITM:   forceHTTPSMITM,
		audit:            audit,
		session:          session,
		originalDst:      kernelOriginalDestination,
		transport:        newUpstreamTransport(),
		upgradeTransport: upgradeTransport,
	}
}

// captureSecrets lists the values the gateway releases, so that the capture can
// put the placeholder back wherever a released value shows up.
func captureSecrets(rules []model.SecretReleaseEntry) []netcapture.Secret {
	secrets := make([]netcapture.Secret, 0, len(rules))
	for _, rule := range rules {
		secrets = append(secrets, netcapture.Secret{Value: rule.Value, Placeholder: rule.Placeholder})
	}
	return secrets
}

func newUpstreamTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     false,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	}
}

// logEvent stamps the gateway session on every event, so a log shared by
// concurrent sessions of the same project and tool can be split again.
func (p *proxy) logEvent(event netlog.Event) {
	event.Method = auditField(event.Method)
	event.Domain = auditField(event.Domain)
	event.Path = auditField(event.Path)
	event.ContentType = auditField(event.ContentType)
	event.Session = p.session
	p.audit.Append(event)
}

// Four fields at sixfold JSON escaping stay below the reader's 1 MiB line limit.
const maxAuditFieldBytes = 16 * 1024

func auditField(value string) string {
	if len(value) <= maxAuditFieldBytes {
		return value
	}
	return value[:maxAuditFieldBytes] + "...[truncated]"
}

func (p *proxy) handler(scheme string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		end := p.exchanges.begin()
		defer end()
		host := normalizedRequestHost(req)
		event := newHTTPAuditEvent(req, host, scheme)
		if scheme == "https" {
			if sni := normalizedTLSHost(req); sni != "" && host != "" && sni != host {
				mismatchEvent := event
				mismatchEvent.Domain = host
				mismatchEvent.Verdict = netlog.VerdictPass
				mismatchEvent.Rule = "sni-host-mismatch"
				p.logEvent(mismatchEvent)
			}
		}
		if !p.hasTarget(req, host) {
			event.Status = http.StatusBadRequest
			event.Verdict = netlog.VerdictDeny
			event.Rule = "invalid-host"
			p.logEvent(event)
			http.Error(w, "Missing target host", http.StatusBadRequest)
			return
		}
		if !p.hostPermitted(host) {
			event.Status = http.StatusForbidden
			event.Verdict = netlog.VerdictDeny
			event.Rule = netlog.RuleAllowlist
			p.logEvent(event)
			http.Error(w, "Domain not in allowlist", http.StatusForbidden)
			return
		}
		rules := p.releaseRules()
		if p.nativeAuth != nil {
			if resp, handled := p.nativeAuth.Intercept(req, scheme); handled {
				defer func() { _ = resp.Body.Close() }()
				copyHeaders(w.Header(), resp.Header)
				w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
				w.WriteHeader(resp.StatusCode)
				n, _ := io.Copy(w, resp.Body)
				event.Status = resp.StatusCode
				event.ResponseSize = n
				event.ContentType = "application/json"
				event.Rule = "native-auth-exchange"
				event.Verdict = netlog.VerdictPass
				if resp.StatusCode >= 400 {
					event.Verdict = netlog.VerdictDeny
				}
				p.logEvent(event)
				return
			}
			nativeRules, err := p.nativeAuth.Releases(req.Context(), req.Header)
			if err != nil {
				event.Status = http.StatusForbidden
				event.Verdict = netlog.VerdictDeny
				event.Rule = "native-auth-release"
				p.logEvent(event)
				http.Error(w, "request denied", http.StatusForbidden)
				return
			}
			rules = append(append([]model.SecretReleaseEntry(nil), rules...), nativeRules...)
		}
		// The capture snapshots the request before secrets are released into it,
		// so it holds the placeholders the sandbox sent and never a real value.
		// A denied request was not relayed and leaves no capture.
		capture := p.captures.Begin(req, scheme)
		if host != "" {
			if err := rewriteHeaders(scheme, host, req.Header, rules); err != nil || (p.nativeAuth != nil && markedHeaders(req.Header)) {
				event.Status = http.StatusForbidden
				event.Verdict = netlog.VerdictDeny
				event.Rule = "secret-injection"
				p.logEvent(event)
				http.Error(w, "request denied", http.StatusForbidden)
				return
			}
		}
		if capture != nil {
			// Explicit identity also disables the transport's automatic gzip
			// decoding, so unexpected encoding headers remain visible to the
			// capture's omission check. No WebSocket extension such as
			// permessage-deflate is offered either. The record keeps the
			// client's headers.
			req.Header.Set("Accept-Encoding", "identity")
			req.Header.Del("Sec-WebSocket-Extensions")
		}
		result, err := p.forward(w, req, scheme, capture)
		if err != nil {
			event.Status = http.StatusBadGateway
			event.Verdict = netlog.VerdictPass
			event.Rule = ruleUpstreamError
			event.CaptureID = capture.Finish(err)
			p.logEvent(event)
			http.Error(w, "Upstream request failed", http.StatusBadGateway)
			return
		}
		event.CaptureID = capture.Finish(nil)
		event.Status = result.StatusCode
		event.ResponseSize = result.ResponseBytes
		event.ContentType = result.ContentType
		event.Verdict = netlog.VerdictPass
		event.Rule = p.passRule()
		p.logEvent(event)
	}
}

// hasTarget reports whether the proxy knows where to send req. A proxy that
// dials original destinations has none for a request that reached it
// directly; its Host header may name the proxy itself, so dialling that would
// loop.
func (p *proxy) hasTarget(req *http.Request, host string) bool {
	if originalDestinationFrom(req.Context()).IsValid() {
		return true
	}
	return host != "" && !p.caps.dialOriginalDestination
}

type forwardResult struct {
	StatusCode    int
	ResponseBytes int64
	ContentType   string
}

func (p *proxy) forward(w http.ResponseWriter, req *http.Request, scheme string, capture *netcapture.Capture) (forwardResult, error) {
	target := *req.URL
	target.Scheme = scheme
	host := req.Host
	if strings.TrimSpace(host) == "" {
		host = normalizedRequestHost(req)
	}
	target.Host = host
	if dst := originalDestinationFrom(req.Context()); dst.IsValid() && (scheme == "http" || strings.TrimSpace(target.Host) == "") {
		// Keep the Host header and only redirect the dial. HTTPS keeps the
		// name in the URL for certificate checks; the transport dials dst.
		target.Host = dst.String()
	}
	if strings.TrimSpace(target.Host) == "" {
		return forwardResult{}, fmt.Errorf("missing request host")
	}
	if !strings.Contains(target.Host, ":") {
		if scheme == "https" {
			target.Host += ":443"
		} else {
			target.Host += ":80"
		}
	}
	outgoing := req.Clone(req.Context())
	outgoing.RequestURI = ""
	outgoing.URL = &target
	outgoing.Body = capture.RequestBody(req.Body)
	if p.caps.forwardUnchanged {
		if _, ok := outgoing.Header["User-Agent"]; !ok {
			// An empty value stops the transport from sending its own.
			outgoing.Header["User-Agent"] = []string{""}
		}
	} else {
		addForwardingHeaders(outgoing, req.RemoteAddr, scheme)
	}
	transport := p.transportFor(originalDestinationFrom(req.Context()), outgoing.Header.Get("Upgrade") != "")
	resp, err := transport.RoundTrip(outgoing)
	if err != nil {
		return forwardResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	capture.Response(resp)
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return p.relayUpgrade(w, outgoing, resp, capture)
	}
	copyHeaders(w.Header(), resp.Header)
	if p.caps.forwardUnchanged {
		suppressServerAddedHeaders(w.Header())
	}
	w.WriteHeader(resp.StatusCode)
	// Send the head before waiting for the body: an event stream can stay
	// silent for a long time after it opens, and the client cannot see the
	// stream as open until the head arrives.
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	var client io.Writer = flushWriter{w: w, rc: rc}
	if sink := capture.ResponseBody(); sink != nil {
		client = captureWriter{relay: client, capture: sink}
	}
	responseBytes, copyErr := io.Copy(client, resp.Body)
	capture.ResponseEnded(copyErr)
	return forwardResult{
		StatusCode:    resp.StatusCode,
		ResponseBytes: responseBytes,
		ContentType:   strings.TrimSpace(resp.Header.Get("Content-Type")),
	}, nil
}

// suppressServerAddedHeaders stops the HTTP server from adding a Date or a
// sniffed Content-Type the upstream response did not carry: a nil value marks
// the header as present but writes nothing.
func suppressServerAddedHeaders(header http.Header) {
	for _, name := range []string{"Date", "Content-Type"} {
		if _, ok := header[name]; !ok {
			header[name] = nil
		}
	}
}

func addForwardingHeaders(outgoing *http.Request, remoteAddr string, scheme string) {
	outgoing.Header.Set("X-Forwarded-Proto", scheme)
	if clientIP := clientIPFromAddr(remoteAddr); clientIP != "" {
		if prior := strings.TrimSpace(outgoing.Header.Get("X-Forwarded-For")); prior != "" {
			outgoing.Header.Set("X-Forwarded-For", prior+", "+clientIP)
		} else {
			outgoing.Header.Set("X-Forwarded-For", clientIP)
		}
	}
}

// flushWriter passes each chunk to the client as soon as it arrives. Without
// it, streamed responses such as server-sent events sit in the server's write
// buffer until it fills or the response ends.
type flushWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err != nil {
		return n, err
	}
	return n, f.rc.Flush()
}

// captureWriter tees relayed bytes into their capture. Each chunk is relayed
// before it is captured, and only what was relayed is captured; the next chunk
// is read once the capture has it, so a slow disk slows the stream down.
type captureWriter struct {
	relay   io.Writer
	capture io.Writer
}

func (c captureWriter) Write(p []byte) (int, error) {
	n, err := c.relay.Write(p)
	_, _ = c.capture.Write(p[:n])
	return n, err
}

// relayUpgrade completes a protocol switch such as a WebSocket handshake. The
// 101 response must reach the client before either side sends a frame, and
// the upgraded connection then carries traffic in both directions, so it is
// taken over from the HTTP server rather than written as a response body.
// The messages of a WebSocket are captured as each side's bytes are relayed.
func (p *proxy) relayUpgrade(w http.ResponseWriter, req *http.Request, resp *http.Response, capture *netcapture.Capture) (forwardResult, error) {
	requested, switched := upgradeType(req.Header), upgradeType(resp.Header)
	if requested == "" || !strings.EqualFold(requested, switched) {
		return forwardResult{}, fmt.Errorf("upstream switched to protocol %q when %q was requested", switched, requested)
	}
	upstream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		return forwardResult{}, fmt.Errorf("upgrade response body is not writable")
	}
	downstream, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return forwardResult{}, fmt.Errorf("take over upgraded connection: %w", err)
	}
	defer func() { _ = downstream.Close() }()
	defer p.exchanges.upgraded(downstream)()

	// From here on the connection is no longer the HTTP server's, so no error
	// response can be sent: a client that is gone ends the relay.
	result := forwardResult{StatusCode: resp.StatusCode}
	head := *resp
	head.Body = nil
	if head.Write(buffered) != nil || buffered.Flush() != nil {
		return result, nil
	}

	var toUpstream io.Writer = upstream
	var toClient io.Writer = downstream
	if fromClient, fromServer := capture.WebSocket(resp); fromClient != nil {
		toUpstream = captureWriter{relay: upstream, capture: fromClient}
		toClient = captureWriter{relay: downstream, capture: fromServer}
	}

	// Either side ending the session ends the relay: closing both connections
	// unblocks the copy in the other direction.
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(toUpstream, buffered.Reader)
		done <- struct{}{}
	}()
	go func() {
		result.ResponseBytes, _ = io.Copy(toClient, upstream)
		done <- struct{}{}
	}()
	<-done
	_ = upstream.Close()
	_ = downstream.Close()
	<-done
	return result, nil
}

// exchangeTracker lets the proxy finish what it relays when it stops. The
// HTTP servers' Shutdown neither closes nor waits for connections taken over
// for protocol upgrades, so without it an upgraded connection open at shutdown
// would be cut without its event or capture being written.
type exchangeTracker struct {
	mu       sync.Mutex
	stopping bool
	conns    map[io.Closer]struct{}
	active   sync.WaitGroup
}

// begin counts an exchange in flight and returns the func that ends it.
// Exchanges that begin while the proxy stops are not waited for.
func (t *exchangeTracker) begin() func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopping {
		return func() {}
	}
	t.active.Add(1)
	return t.active.Done
}

// upgraded registers a connection taken over for an upgrade, to be closed when
// the proxy stops, and returns the func that deregisters it. One taken over
// while the proxy stops is closed at once.
func (t *exchangeTracker) upgraded(conn io.Closer) func() {
	t.mu.Lock()
	if t.stopping {
		t.mu.Unlock()
		_ = conn.Close()
		return func() {}
	}
	if t.conns == nil {
		t.conns = map[io.Closer]struct{}{}
	}
	t.conns[conn] = struct{}{}
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		delete(t.conns, conn)
		t.mu.Unlock()
	}
}

// closeUpgraded closes the upgraded connections, which ends their relays.
func (t *exchangeTracker) closeUpgraded() {
	t.mu.Lock()
	t.stopping = true
	conns := t.conns
	t.conns = nil
	t.mu.Unlock()
	for conn := range conns {
		_ = conn.Close()
	}
}

// wait returns once the exchanges in flight have ended, or ctx is done. It
// follows closeUpgraded, after which no exchange is counted any more.
func (t *exchangeTracker) wait(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		t.active.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// upgradeType returns the protocol a message asks to switch to, or "" when
// its Connection header does not name the upgrade.
func upgradeType(h http.Header) string {
	for _, value := range h.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return h.Get("Upgrade")
			}
		}
	}
	return ""
}

type httpsDispatchServer struct {
	listener     net.Listener
	mitmListener net.Listener
	// handoff passes intercepted connections of a proxy that dials original
	// destinations to the MITM server in process, so each keeps its
	// destination; a loopback tunnel would lose it.
	handoff    *handoffListener
	proxy      *proxy
	mitmServer *http.Server
	mitmAddr   string

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	closing bool
}

func newHTTPSDispatchServer(proxy *proxy, store *tlsstore.Store, listener net.Listener) (*httpsDispatchServer, error) {
	mitmListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &httpsDispatchServer{
		listener:     listener,
		mitmListener: mitmListener,
		handoff:      newHandoffListener(mitmListener.Addr()),
		proxy:        proxy,
		mitmServer: &http.Server{
			Handler:           proxy.handler("https"),
			ReadHeaderTimeout: 10 * time.Second,
			ConnContext:       originalDestinationConnContext,
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
					if hello == nil || strings.TrimSpace(hello.ServerName) == "" {
						return nil, errors.New("missing SNI")
					}
					return store.Certificate(hello.ServerName)
				},
			},
		},
		mitmAddr: mitmListener.Addr().String(),
		conns:    map[net.Conn]struct{}{},
	}
	return server, nil
}

func (s *httpsDispatchServer) Serve() error {
	errCh := make(chan error, 2)
	serveMITM := func(listener net.Listener) {
		if err := s.mitmServer.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			errCh <- err
			_ = s.listener.Close()
		}
	}
	go serveMITM(s.mitmListener)
	if s.proxy.caps.dialOriginalDestination {
		go serveMITM(s.handoff)
	}

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case mitmErr := <-errCh:
				return mitmErr
			default:
			}
			if s.isClosing() {
				return nil
			}
			return err
		}
		s.trackConn(conn)
		go s.serveConn(conn)
	}
}

func (s *httpsDispatchServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		_ = s.listener.Close()
		_ = s.mitmListener.Close()
		_ = s.handoff.Close()
		_ = s.mitmServer.Shutdown(ctx)
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *httpsDispatchServer) serveConn(conn net.Conn) {
	defer s.untrackConn(conn)

	if s.proxy.caps.dialOriginalDestination {
		s.serveTransparentConn(conn)
		return
	}
	host, preface, err := readClientHello(conn)
	if err != nil {
		s.proxy.logEvent(newTCPAuditEvent("", netlog.VerdictDeny, "tls-clienthello"))
		_ = conn.Close()
		return
	}
	if !s.proxy.hostPermitted(host) {
		s.proxy.logEvent(newTCPAuditEvent(host, netlog.VerdictDeny, netlog.RuleAllowlist))
		_ = conn.Close()
		return
	}
	if shouldMITM(host, s.proxy.releaseRules(), s.proxy.forceHTTPSMITM) || (s.proxy.nativeAuth != nil && s.proxy.nativeAuth.RequiresMITM(host)) {
		s.proxy.logEvent(newTCPAuditEvent(host, netlog.VerdictPass, "mitm-dispatch"))
		tunnelToAddress(conn, preface, s.mitmAddr)
		return
	}
	s.proxy.logEvent(newTCPAuditEvent(host, netlog.VerdictPass, s.proxy.passRule()))
	tunnelTLS(conn, preface, host)
}

// serveTransparentConn is serveConn for a proxy that only observes: every
// connection reaches the address the client dialled, including those without
// SNI and those that are not TLS at all.
func (s *httpsDispatchServer) serveTransparentConn(conn net.Conn) {
	dst := s.proxy.originalDestination(conn)
	recorded := &recordingConn{Conn: conn}
	host, preface, err := readClientHello(recorded)
	if err != nil {
		host, preface = "", recorded.read.Bytes()
	}
	if host != "" && shouldMITM(host, s.proxy.releaseRules(), s.proxy.forceHTTPSMITM) {
		s.proxy.logEvent(newTCPAuditEvent(host, netlog.VerdictPass, "mitm-dispatch"))
		s.intercept(conn, preface, dst)
		return
	}
	target := ""
	switch {
	case dst.IsValid():
		target = dst.String()
	case host != "":
		target = net.JoinHostPort(host, "443")
	}
	s.proxy.passThrough(conn, preface, host, 443, target)
}

// recordingConn keeps every byte read through it, so traffic the proxy failed
// to parse can still be forwarded in full.
type recordingConn struct {
	net.Conn
	read bytes.Buffer
}

func (c *recordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Write(p[:n])
	return n, err
}

func (s *httpsDispatchServer) trackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		_ = conn.Close()
		return
	}
	s.conns[conn] = struct{}{}
}

func (s *httpsDispatchServer) untrackConn(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *httpsDispatchServer) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func requiresMITM(host string, rules []model.SecretReleaseEntry) bool {
	normalizedHost, err := domainpattern.NormalizeHost(host)
	if err != nil {
		return false
	}
	for _, rule := range rules {
		if releaseHostMatch(normalizedHost, rule) {
			return true
		}
	}
	return false
}

// releaseHostMatch reports whether a normalized request host is a release
// target of rule. Declared patterns keep the allowlist semantics, where a bare
// host also covers its subdomains; a rule marked ExactHosts matches only the
// hosts it names, because those were selected at runtime to name one instance.
func releaseHostMatch(host string, rule model.SecretReleaseEntry) bool {
	if rule.ExactHosts {
		normalized, err := domainpattern.NormalizeHost(host)
		if err != nil {
			return false
		}
		return slices.Contains(rule.Hosts, normalized)
	}
	return hostAllowed(host, rule.Hosts)
}

func shouldMITM(host string, rules []model.SecretReleaseEntry, forceHTTPSMITM bool) bool {
	return forceHTTPSMITM || requiresMITM(host, rules)
}

func tunnelTLS(downstream net.Conn, preface []byte, host string) {
	tunnelToAddress(downstream, preface, net.JoinHostPort(host, "443"))
}

func tunnelToAddress(downstream net.Conn, preface []byte, upstreamAddr string) {
	upstream, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", upstreamAddr)
	if err != nil {
		_ = downstream.Close()
		return
	}
	defer func() { _ = upstream.Close() }()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, io.MultiReader(bytes.NewReader(preface), downstream))
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(downstream, upstream)
		closeWrite(downstream)
		done <- struct{}{}
	}()
	// Wait for both copy loops; their errors do not change tunnel cleanup.
	<-done
	<-done
}

func closeWrite(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := conn.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}

func readClientHello(conn net.Conn) (string, []byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	preface := make([]byte, 0, 4096)
	handshake := make([]byte, 0, 4096)
	totalHandshakeLen := -1
	recordCount := 0

	for {
		recordHeader := make([]byte, 5)
		if _, err := io.ReadFull(conn, recordHeader); err != nil {
			return "", nil, err
		}
		preface = append(preface, recordHeader...)
		recordType := recordHeader[0]
		if recordType != 22 {
			return "", nil, fmt.Errorf("unexpected TLS record type: %d", recordType)
		}
		recordLen := int(binary.BigEndian.Uint16(recordHeader[3:5]))
		if recordLen <= 0 || recordLen > 1<<14+2048 {
			return "", nil, fmt.Errorf("invalid TLS record length")
		}
		recordPayload := make([]byte, recordLen)
		if _, err := io.ReadFull(conn, recordPayload); err != nil {
			return "", nil, err
		}
		preface = append(preface, recordPayload...)
		handshake = append(handshake, recordPayload...)

		if totalHandshakeLen == -1 {
			recordCount++
			if recordCount > maxTLSRecordsBeforeClientHelloLength {
				return "", nil, fmt.Errorf("TLS ClientHello length not determined after %d records", maxTLSRecordsBeforeClientHelloLength)
			}
			if len(handshake) < 4 {
				continue
			}
			if handshake[0] != 1 {
				return "", nil, fmt.Errorf("first TLS handshake is not ClientHello")
			}
			totalHandshakeLen = int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
			if totalHandshakeLen <= 0 || totalHandshakeLen > 1<<20 {
				return "", nil, fmt.Errorf("invalid TLS ClientHello length")
			}
		}

		if len(handshake) >= totalHandshakeLen+4 {
			host, err := parseClientHelloServerName(handshake[4 : 4+totalHandshakeLen])
			if err != nil {
				return "", nil, err
			}
			if host == "" {
				return "", nil, fmt.Errorf("missing SNI")
			}
			return host, preface, nil
		}
	}
}

func parseClientHelloServerName(body []byte) (string, error) {
	s := cryptobyte.String(body)
	var legacyVersion uint16
	var random []byte
	if !s.ReadUint16(&legacyVersion) || !s.ReadBytes(&random, 32) {
		return "", fmt.Errorf("invalid ClientHello")
	}

	var sessionID cryptobyte.String
	if !s.ReadUint8LengthPrefixed(&sessionID) {
		return "", fmt.Errorf("invalid ClientHello")
	}

	var cipherSuites cryptobyte.String
	if !s.ReadUint16LengthPrefixed(&cipherSuites) || len(cipherSuites) == 0 {
		return "", fmt.Errorf("invalid ClientHello")
	}

	var compressionMethods cryptobyte.String
	if !s.ReadUint8LengthPrefixed(&compressionMethods) || len(compressionMethods) == 0 {
		return "", fmt.Errorf("invalid ClientHello")
	}

	if s.Empty() {
		return "", nil
	}

	var extensions cryptobyte.String
	if !s.ReadUint16LengthPrefixed(&extensions) || !s.Empty() {
		return "", fmt.Errorf("invalid ClientHello")
	}

	for !extensions.Empty() {
		var extType uint16
		var extData cryptobyte.String
		if !extensions.ReadUint16(&extType) || !extensions.ReadUint16LengthPrefixed(&extData) {
			return "", fmt.Errorf("invalid ClientHello extension")
		}
		if extType != 0 {
			continue
		}

		var nameList cryptobyte.String
		if !extData.ReadUint16LengthPrefixed(&nameList) || !extData.Empty() {
			return "", fmt.Errorf("invalid SNI extension")
		}
		for !nameList.Empty() {
			var nameType uint8
			var nameValue cryptobyte.String
			if !nameList.ReadUint8(&nameType) || !nameList.ReadUint16LengthPrefixed(&nameValue) {
				return "", fmt.Errorf("invalid SNI hostname")
			}
			if nameType == 0 {
				name, err := domainpattern.NormalizeHost(string(nameValue))
				if err == nil && name != "" {
					return name, nil
				}
			}
		}
		return "", nil
	}
	return "", nil
}

func copyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func normalizeRule(rule model.SecretReleaseEntry) (model.SecretReleaseEntry, error) {
	placeholder := strings.TrimSpace(rule.Placeholder)
	if placeholder == "" {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry missing placeholder for %q", rule.SecretID)
	}
	if rule.Value == "" {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry missing secret for %q", rule.SecretID)
	}
	header := strings.ToLower(strings.TrimSpace(rule.Header))
	if header == "" {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry missing header for %q", rule.SecretID)
	}
	format := strings.TrimSpace(rule.Format)
	if format != "" && !strings.Contains(format, "%s") {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry has invalid format for %q", rule.SecretID)
	}
	normalizedHosts := make([]string, 0, len(rule.Hosts))
	for _, host := range rule.Hosts {
		value, err := domainpattern.Normalize(host)
		if err != nil {
			return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry has invalid host pattern %q for %q: %w", strings.TrimSpace(host), rule.SecretID, err)
		}
		if value == "" {
			continue
		}
		// A wildcard cannot be matched by equality, so an exact rule carrying
		// one would silently release the secret nowhere. The host selector
		// already rejects wildcards; fail loudly if one reaches the gateway.
		if rule.ExactHosts && strings.Contains(value, "*") {
			return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry has wildcard host %q in exact-host rule for %q", value, rule.SecretID)
		}
		normalizedHosts = append(normalizedHosts, value)
	}
	hosts := util.Dedupe(normalizedHosts)
	if len(hosts) == 0 {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry missing hosts for %q", rule.SecretID)
	}
	rule.Placeholder = placeholder
	rule.Header = header
	rule.Format = format
	rule.Hosts = hosts
	return rule, nil
}

func rewriteHeaders(scheme string, host string, headers http.Header, rules []model.SecretReleaseEntry) error {
	normalizedHost, err := domainpattern.NormalizeHost(host)
	if err != nil {
		return fmt.Errorf("invalid request host")
	}
	secureTransport := strings.EqualFold(strings.TrimSpace(scheme), "https")
	for header, values := range headers {
		for _, rule := range rules {
			if strings.Contains(strings.ToLower(header), strings.ToLower(rule.Placeholder)) {
				return fmt.Errorf("request denied")
			}
			for _, value := range values {
				if strings.Contains(value, rule.Placeholder) && (!secureTransport || !releaseHostMatch(normalizedHost, rule) || !strings.EqualFold(header, rule.Header)) {
					return fmt.Errorf("request denied")
				}
			}
		}
	}
	for header, values := range headers {
		updated := make([]string, len(values))
		copy(updated, values)
		changed := false
		for i, value := range updated {
			for _, rule := range rules {
				if !strings.Contains(value, rule.Placeholder) {
					continue
				}
				replacement := rule.Value
				// Apply format only when the header value is the bare placeholder;
				// this avoids turning "Bearer <placeholder>" into "Bearer Bearer <secret>".
				if strings.EqualFold(header, rule.Header) && rule.Format != "" && strings.TrimSpace(value) == rule.Placeholder {
					replacement = strings.ReplaceAll(rule.Format, "%s", rule.Value)
				}
				value = strings.ReplaceAll(value, rule.Placeholder, replacement)
				changed = true
			}
			updated[i] = value
		}
		if changed {
			headers[header] = updated
		}
	}
	return nil
}

func hostAllowed(host string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	value, err := domainpattern.NormalizeHost(host)
	if err != nil {
		return false
	}
	for _, pattern := range patterns {
		if domainpattern.MatchNormalizedHost(value, pattern) {
			return true
		}
	}
	return false
}

// hostPermitted enforces deny-wins: a host that matches any denied domain (or
// is a subdomain of one) is rejected even when it would match an allow entry.
// dnsmasq blocks denied apex domains, but the proxy suffix-matches, so a
// denied host more specific than an allowed parent (deny tracking.example.com,
// allow example.com) would otherwise pass here — the deny check closes that.
func (p *proxy) hostPermitted(host string) bool {
	if !p.caps.enforceAllowlist {
		return true
	}
	if hostAllowed(host, p.deniedDomains) {
		return false
	}
	return hostAllowed(host, p.allowedDomains)
}

func (p *proxy) passRule() string {
	if !p.caps.enforceAllowlist {
		return netlog.RuleUnrestricted
	}
	return netlog.RuleAllowlist
}

func newHTTPAuditEvent(req *http.Request, host string, scheme string) netlog.Event {
	event := netlog.Event{
		Timestamp: time.Now().UTC().Format(netlog.TimeFormat),
		Type:      netlog.TypeHTTP,
		Method:    strings.ToUpper(strings.TrimSpace(reqMethod(req))),
		Domain:    host,
		Path:      requestPath(req),
		Port:      requestPort(req, scheme),
	}
	if req != nil && req.ContentLength > 0 {
		event.RequestSize = req.ContentLength
	}
	return event
}

func reqMethod(req *http.Request) string {
	if req == nil {
		return ""
	}
	return req.Method
}

func requestPath(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "/"
	}
	path := strings.TrimSpace(req.URL.EscapedPath())
	if path == "" {
		path = strings.TrimSpace(req.URL.Path)
	}
	if path == "" {
		return "/"
	}
	return path
}

func requestPort(req *http.Request, scheme string) int {
	defaultPort := 80
	if strings.EqualFold(strings.TrimSpace(scheme), "https") {
		defaultPort = 443
	}
	if req == nil {
		return defaultPort
	}

	if port := parsePortFromHost(req.Host); port > 0 {
		return port
	}
	if req.URL != nil {
		if port := parsePortFromHost(req.URL.Host); port > 0 {
			return port
		}
	}
	return defaultPort
}

func parsePortFromHost(rawHost string) int {
	hostValue := strings.TrimSpace(rawHost)
	if hostValue == "" {
		return 0
	}
	if _, port, err := net.SplitHostPort(hostValue); err == nil {
		parsed, convErr := strconv.Atoi(strings.TrimSpace(port))
		if convErr == nil && parsed > 0 {
			return parsed
		}
		return 0
	}
	return 0
}

func newTCPAuditEvent(domain string, verdict string, rule string) netlog.Event {
	return netlog.Event{
		Timestamp: time.Now().UTC().Format(netlog.TimeFormat),
		Type:      netlog.TypeTCP,
		Domain:    strings.TrimSpace(domain),
		Port:      443,
		Verdict:   strings.TrimSpace(verdict),
		Rule:      strings.TrimSpace(rule),
	}
}

func normalizedRequestHost(req *http.Request) string {
	if req == nil {
		return ""
	}
	if host := normalizeHostValue(req.Host); host != "" {
		return host
	}
	if req.URL != nil {
		if host := normalizeHostValue(req.URL.Host); host != "" {
			return host
		}
		if parsed, err := url.Parse(req.URL.String()); err == nil {
			if host := normalizeHostValue(parsed.Host); host != "" {
				return host
			}
		}
	}
	return ""
}

func normalizedTLSHost(req *http.Request) string {
	if req == nil || req.TLS == nil {
		return ""
	}
	return normalizeHostValue(req.TLS.ServerName)
}

func normalizeHostValue(value string) string {
	host, err := domainpattern.NormalizeHost(value)
	if err != nil {
		return ""
	}
	return host
}

func clientIPFromAddr(remoteAddr string) string {
	addr := strings.TrimSpace(remoteAddr)
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.TrimSpace(host)
	}
	return addr
}

func markedHeaders(headers http.Header) bool {
	for key, values := range headers {
		if strings.Contains(strings.ToLower(key), strings.ToLower(model.SecretPlaceholderMarker)) {
			return true
		}
		for _, value := range values {
			if strings.Contains(value, model.SecretPlaceholderMarker) {
				return true
			}
		}
	}
	return false
}
