// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"net"
	"sync"
)

// connSet tracks the connections a listener accepted until they close, so an
// allowlist reload can end them the way a proxy restart would.
type connSet struct {
	mu    sync.Mutex
	conns map[*trackedConn]struct{}
}

func newConnSet() *connSet {
	return &connSet{conns: map[*trackedConn]struct{}{}}
}

// track wraps listener so that every connection it accepts joins the set.
func (s *connSet) track(listener net.Listener) net.Listener {
	return &trackingListener{Listener: listener, set: s}
}

// closeAll closes every tracked connection.
func (s *connSet) closeAll() {
	s.mu.Lock()
	conns := make([]*trackedConn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (s *connSet) add(conn *trackedConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[conn] = struct{}{}
}

func (s *connSet) remove(conn *trackedConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

type trackingListener struct {
	net.Listener
	set *connSet
}

func (l *trackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, set: l.set}
	l.set.add(tracked)
	return tracked, nil
}

type trackedConn struct {
	net.Conn
	set *connSet
}

func (c *trackedConn) Close() error {
	c.set.remove(c)
	return c.Conn.Close()
}
