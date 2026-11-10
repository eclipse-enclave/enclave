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
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"time"

	"enclave/internal/netlog"
)

// sniffTimeout bounds how long the HTTP frontend waits for a request line
// before treating the connection as another protocol.
const sniffTimeout = 10 * time.Second

// maxSniffedLineLength bounds how much of a connection the HTTP frontend
// buffers to find the end of its first line. Common web servers reject longer
// request lines (Apache 8190 bytes, nginx 8 KiB), and a longer first line is
// passed through as another protocol.
const maxSniffedLineLength = 8192

// maxHTTPMethodLength is longer than any registered HTTP method.
const maxHTTPMethodLength = 24

type originalDstKey struct{}

func originalDestinationFrom(ctx context.Context) netip.AddrPort {
	dst, _ := ctx.Value(originalDstKey{}).(netip.AddrPort)
	return dst
}

// originalDestination reports where the client sent a connection before the
// gateway redirected it, or the zero value when that is unknown. A connection
// that reached the proxy directly reports the proxy itself, which is not a
// destination: dialling it would loop.
func (p *proxy) originalDestination(conn net.Conn) netip.AddrPort {
	if p.originalDst == nil {
		return netip.AddrPort{}
	}
	dst, err := p.originalDst(conn)
	if err != nil || !dst.IsValid() {
		return netip.AddrPort{}
	}
	// A dual-stack listener reports IPv4 addresses as IPv4-mapped IPv6.
	dst = unmapped(dst)
	if local, ok := conn.LocalAddr().(*net.TCPAddr); ok && unmapped(local.AddrPort()) == dst {
		return netip.AddrPort{}
	}
	return dst
}

func unmapped(addr netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
}

// passThrough logs a connection the proxy forwards without understanding it
// and tunnels it to target. Without a target it can only drop the connection.
func (p *proxy) passThrough(conn net.Conn, preface []byte, domain string, port int, target string) {
	rule := p.passRule()
	if target == "" {
		rule = ruleUpstreamError
	}
	event := newTCPAuditEvent(domain, netlog.VerdictPass, rule)
	event.Port = port
	p.logEvent(event)
	if target == "" {
		_ = conn.Close()
		return
	}
	tunnelToAddress(conn, preface, target)
}

func (p *proxy) newHTTPServer() *http.Server {
	return &http.Server{
		Handler:           p.handler("http"),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext:       originalDestinationConnContext,
	}
}

// serveHTTP serves plaintext HTTP. A proxy that only observes must not break
// other protocols on port 80, so it first sniffs each connection and tunnels
// anything that does not open with an HTTP/1 request line.
func (p *proxy) serveHTTP(server *http.Server, listener net.Listener) error {
	if !p.caps.dialOriginalDestination {
		return server.Serve(listener)
	}
	sniffing := &sniffListener{
		Listener: listener,
		proxy:    p,
		conns:    make(chan net.Conn),
		errs:     make(chan error),
		done:     make(chan struct{}),
	}
	go sniffing.acceptLoop()
	return server.Serve(sniffing)
}

// sniffListener hands the HTTP server only the connections that speak HTTP.
type sniffListener struct {
	net.Listener
	proxy *proxy
	conns chan net.Conn
	errs  chan error
	done  chan struct{}
	once  sync.Once
}

// acceptLoop hands every accept error to the HTTP server, which retries
// temporary ones and closes the listener on the rest.
func (l *sniffListener) acceptLoop() {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			select {
			case l.errs <- err:
			case <-l.done:
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go l.dispatch(conn)
	}
}

func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case err := <-l.errs:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *sniffListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

func (l *sniffListener) dispatch(conn net.Conn) {
	dst := l.proxy.originalDestination(conn)
	reader := bufio.NewReaderSize(conn, maxSniffedLineLength)
	_ = conn.SetReadDeadline(time.Now().Add(sniffTimeout))
	line := sniffRequestLine(reader)
	_ = conn.SetReadDeadline(time.Time{})

	// A request meant for an explicit proxy goes to that proxy as sent. Without
	// a destination the HTTP server answers it with an error instead.
	if line.kind == originFormRequest || (line.kind == proxyRequest && !dst.IsValid()) {
		select {
		case l.conns <- &sniffedConn{Conn: conn, reader: reader, dst: dst}:
		case <-l.done:
			_ = conn.Close()
		}
		return
	}

	target := ""
	if dst.IsValid() {
		target = dst.String()
	}
	preface, _ := reader.Peek(reader.Buffered())
	l.proxy.passThrough(conn, preface, line.domain, 80, target)
	_ = conn.Close()
}

type requestLineKind int

const (
	notHTTP requestLineKind = iota
	originFormRequest
	// proxyRequest is a CONNECT or absolute-form request, addressed to an
	// explicit proxy.
	proxyRequest
)

type requestLine struct {
	kind requestLineKind
	// domain is the host a proxyRequest asks for.
	domain string
}

// sniffRequestLine classifies the first line of the buffered stream. Only a
// complete HTTP/1 request line counts as HTTP; the HTTP/2 prior-knowledge
// preface does not, since the HTTP server cannot serve it.
func sniffRequestLine(reader *bufio.Reader) requestLine {
	line, complete := peekFirstLine(reader)
	if !complete {
		return requestLine{}
	}
	method, rest, ok := bytes.Cut(line, []byte(" "))
	if !ok || !isHTTPMethod(method) {
		return requestLine{}
	}
	target, version, hasVersion := bytes.Cut(rest, []byte(" "))
	if !hasVersion || !isHTTP1Version(version) {
		return requestLine{}
	}
	switch {
	case string(method) == http.MethodConnect:
		// A CONNECT target is a bare authority; the slashes make it parse as one.
		return requestLine{kind: proxyRequest, domain: proxyTargetDomain("//" + string(target))}
	case len(target) > 0 && target[0] != '/' && target[0] != '*':
		return requestLine{kind: proxyRequest, domain: proxyTargetDomain(string(target))}
	}
	return requestLine{kind: originFormRequest}
}

// peekFirstLine returns the first line of the buffered stream without its line
// ending, and whether it ended within the buffer. A stream that ends or stalls
// first returns nothing.
func peekFirstLine(reader *bufio.Reader) ([]byte, bool) {
	for {
		// Peek blocks until at least one byte more than before is buffered.
		if _, err := reader.Peek(reader.Buffered() + 1); err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				peeked, _ := reader.Peek(reader.Buffered())
				return peeked, false
			}
			return nil, false
		}
		peeked, _ := reader.Peek(reader.Buffered())
		if i := bytes.IndexByte(peeked, '\n'); i >= 0 {
			return bytes.TrimSuffix(peeked[:i], []byte("\r")), true
		}
	}
}

func isHTTPMethod(method []byte) bool {
	if len(method) == 0 || len(method) > maxHTTPMethodLength {
		return false
	}
	for _, c := range method {
		if (c < 'A' || c > 'Z') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func isHTTP1Version(version []byte) bool {
	return len(version) == len("HTTP/1.1") && bytes.HasPrefix(version, []byte("HTTP/1.")) &&
		version[len(version)-1] >= '0' && version[len(version)-1] <= '9'
}

// proxyTargetDomain is the host a proxy request target names, or empty when
// it does not parse.
func proxyTargetDomain(target string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return normalizeHostValue(parsed.Hostname())
}

// sniffedConn replays the bytes read while sniffing and carries the
// connection's original destination to the request handler.
type sniffedConn struct {
	net.Conn
	reader io.Reader
	dst    netip.AddrPort
}

func (c *sniffedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *sniffedConn) originalDestination() netip.AddrPort {
	return c.dst
}

// originalDestinationConnContext hands a connection's original destination,
// if it carries one, to the handlers of its requests.
func originalDestinationConnContext(ctx context.Context, conn net.Conn) context.Context {
	if tlsConn, ok := conn.(*tls.Conn); ok {
		conn = tlsConn.NetConn()
	}
	carrier, ok := conn.(interface{ originalDestination() netip.AddrPort })
	if !ok || !carrier.originalDestination().IsValid() {
		return ctx
	}
	return context.WithValue(ctx, originalDstKey{}, carrier.originalDestination())
}

// intercept hands a TLS connection to the MITM server together with its
// original destination and returns once the server is done with it.
func (s *httpsDispatchServer) intercept(conn net.Conn, preface []byte, dst netip.AddrPort) {
	intercepted := &interceptedConn{
		sniffedConn: sniffedConn{Conn: conn, reader: io.MultiReader(bytes.NewReader(preface), conn), dst: dst},
		closed:      make(chan struct{}),
	}
	if !s.handoff.deliver(intercepted) {
		_ = conn.Close()
		return
	}
	<-intercepted.closed
}

// interceptedConn replays the ClientHello read while dispatching, carries the
// connection's original destination, and reports when the MITM server closes
// it.
type interceptedConn struct {
	sniffedConn
	closed    chan struct{}
	closeOnce sync.Once
}

func (c *interceptedConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() { close(c.closed) })
	return err
}

// handoffListener is a listener fed in process instead of by the network.
type handoffListener struct {
	addr  net.Addr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newHandoffListener(addr net.Addr) *handoffListener {
	return &handoffListener{addr: addr, conns: make(chan net.Conn), done: make(chan struct{})}
}

// deliver passes conn to Accept and reports false once the listener is closed.
func (l *handoffListener) deliver(conn net.Conn) bool {
	select {
	case l.conns <- conn:
		return true
	case <-l.done:
		return false
	}
}

func (l *handoffListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *handoffListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *handoffListener) Addr() net.Addr {
	return l.addr
}
