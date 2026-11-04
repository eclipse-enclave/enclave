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
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"enclave/internal/netlog"
)

// sniffTimeout bounds how long the HTTP frontend waits for a request line
// before treating the connection as another protocol.
const sniffTimeout = 10 * time.Second

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
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if sniffed, ok := conn.(*sniffedConn); ok && sniffed.dst.IsValid() {
				return context.WithValue(ctx, originalDstKey{}, sniffed.dst)
			}
			return ctx
		},
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
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(sniffTimeout))
	isHTTP := startsWithHTTPRequestLine(reader)
	_ = conn.SetReadDeadline(time.Time{})

	if isHTTP {
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
	l.proxy.passThrough(conn, preface, "", 80, target)
	_ = conn.Close()
}

// startsWithHTTPRequestLine reports whether the buffered stream opens with an
// HTTP/1 method followed by a space. The HTTP/2 prior-knowledge preface
// ("PRI * HTTP/2.0") does not count: the HTTP server cannot serve it.
func startsWithHTTPRequestLine(reader *bufio.Reader) bool {
	for n := 1; n <= maxHTTPMethodLength+1; n++ {
		peeked, err := reader.Peek(n)
		if err != nil {
			return false
		}
		switch c := peeked[n-1]; {
		case c == ' ':
			method := string(peeked[:n-1])
			return method != "" && method != "PRI"
		case (c < 'A' || c > 'Z') && c != '-' && c != '_':
			return false
		}
	}
	return false
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
