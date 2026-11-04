// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package mitm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// Netfilter socket options that return the destination of a connection before
// NAT rewrote it (linux/netfilter_ipv4.h, linux/netfilter_ipv6/ip6_tables.h).
const (
	soOriginalDst     = 80
	ip6tSoOriginalDst = 80
)

// kernelOriginalDestination reads the destination a redirected connection had
// before the gateway's NAT rule sent it to the proxy.
func kernelOriginalDestination(conn net.Conn) (netip.AddrPort, error) {
	socket, ok := conn.(syscall.Conn)
	if !ok {
		return netip.AddrPort{}, errors.New("connection has no socket")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	local, _ := conn.LocalAddr().(*net.TCPAddr)
	ipv4 := local != nil && local.IP.To4() != nil
	var dst netip.AddrPort
	var lookupErr error
	if err := raw.Control(func(fd uintptr) {
		dst, lookupErr = socketOriginalDestination(int(fd), ipv4) // #nosec G115 -- file descriptors fit in int.
	}); err != nil {
		return netip.AddrPort{}, err
	}
	return dst, lookupErr
}

// socketOriginalDestination asks the conntrack table of the connection's
// family. An IPv4 connection on a dual-stack socket is tracked as IPv4.
func socketOriginalDestination(fd int, ipv4 bool) (netip.AddrPort, error) {
	if ipv4 {
		// The kernel fills a sockaddr_in; IPv6Mreq is a 20-byte buffer
		// that holds it.
		mreq, err := unix.GetsockoptIPv6Mreq(fd, unix.SOL_IP, soOriginalDst)
		if err != nil {
			return netip.AddrPort{}, fmt.Errorf("read IPv4 original destination: %w", err)
		}
		raw := mreq.Multiaddr
		port := binary.BigEndian.Uint16(raw[2:4])
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(raw[4:8])), port), nil
	}
	// The kernel fills a sockaddr_in6; IPv6MTUInfo starts with one.
	info, err := unix.GetsockoptIPv6MTUInfo(fd, unix.SOL_IPV6, ip6tSoOriginalDst)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("read IPv6 original destination: %w", err)
	}
	var port [2]byte
	binary.NativeEndian.PutUint16(port[:], info.Addr.Port)
	return netip.AddrPortFrom(netip.AddrFrom16(info.Addr.Addr), binary.BigEndian.Uint16(port[:])), nil
}
