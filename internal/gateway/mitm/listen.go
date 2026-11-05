// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"errors"
	"net"
	"strconv"
	"sync"
	"syscall"
)

// listenLoopback listens on port at 127.0.0.1 and ::1. The gateway's NAT rules
// redirect the session's traffic there; listening on no other address keeps a
// published port from reaching the proxy. A namespace without IPv6 has no ::1,
// and then no IPv6 traffic to redirect either.
func listenLoopback(port int) (net.Listener, error) {
	v4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	v6, err := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT) {
			return v4, nil
		}
		_ = v4.Close()
		return nil, err
	}
	return newMultiListener(v4, v6), nil
}

// multiListener accepts from several listeners as one. Addr reports the first.
type multiListener struct {
	listeners []net.Listener
	conns     chan net.Conn
	errs      chan error
	done      chan struct{}
	once      sync.Once
}

func newMultiListener(listeners ...net.Listener) *multiListener {
	l := &multiListener{
		listeners: listeners,
		conns:     make(chan net.Conn),
		errs:      make(chan error),
		done:      make(chan struct{}),
	}
	for _, listener := range listeners {
		go l.acceptLoop(listener)
	}
	return l
}

// acceptLoop hands every accept error to the caller of Accept, which decides
// whether to retry.
func (l *multiListener) acceptLoop(listener net.Listener) {
	for {
		conn, err := listener.Accept()
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
		select {
		case l.conns <- conn:
		case <-l.done:
			_ = conn.Close()
			return
		}
	}
}

func (l *multiListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case err := <-l.errs:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *multiListener) Close() error {
	var errs []error
	l.once.Do(func() {
		close(l.done)
		for _, listener := range l.listeners {
			errs = append(errs, listener.Close())
		}
	})
	return errors.Join(errs...)
}

func (l *multiListener) Addr() net.Addr {
	return l.listeners[0].Addr()
}
