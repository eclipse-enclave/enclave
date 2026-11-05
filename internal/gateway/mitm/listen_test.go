// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
)

// The gateway's NAT rules redirect session traffic to the loopback address, so
// the proxy must accept there. It must not accept on the session's other
// addresses: a published port would otherwise expose it to the host.
func TestRunListensOnlyOnLoopback(t *testing.T) {
	for _, mode := range []string{model.NetworkModeRestricted, model.NetworkModeUnrestricted} {
		t.Run(mode, func(t *testing.T) {
			httpPort, httpsPort := freeLoopbackPort(t), freeLoopbackPort(t)
			startProxy(t, Options{
				HTTPPort:    httpPort,
				HTTPSPort:   httpsPort,
				NetworkMode: mode,
				TLSStore:    tlsstore.New(t.TempDir()),
			})

			external := nonLoopbackAddr(t)
			for _, port := range []int{httpPort, httpsPort} {
				assertAccepts(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if ipv6LoopbackAvailable() {
					assertAccepts(t, net.JoinHostPort("::1", strconv.Itoa(port)))
				}
				if external != "" {
					assertRefuses(t, net.JoinHostPort(external, strconv.Itoa(port)))
				}
			}
		})
	}
}

func startProxy(t *testing.T, opts Options) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	opts.OnReady = func() { close(ready) }
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() error after shutdown = %v", err)
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run() error = %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not become ready")
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func ipv6LoopbackAvailable() bool {
	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// nonLoopbackAddr returns an IPv4 address of this host that is not loopback,
// or "" when it has none.
func nonLoopbackAddr(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("list interface addresses: %v", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		return ipNet.IP.String()
	}
	t.Log("no non-loopback IPv4 address; skipping the external-address check")
	return ""
}

func assertAccepts(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Errorf("dial %s: %v, want the proxy to accept", addr, err)
		return
	}
	_ = conn.Close()
}

func assertRefuses(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Errorf("dial %s succeeded, want the proxy to refuse non-loopback connections", addr)
	}
}
