// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strings"

	"enclave/internal/peergroup"
)

// InterfaceResolver maps a local IPv4 address to the interface that holds it.
type InterfaceResolver func(addr netip.Addr) (string, error)

// Rules is the rendered hub filter table for one revision.
type Rules struct {
	Revision peergroup.Revision
	// Restore is iptables-restore input for the filter table.
	Restore string
	// Warnings name members whose hub address no interface holds; they get no rules.
	Warnings []string
}

// RenderRules loads and verifies current like the proxy (without parsing
// secrets) and renders INPUT, OUTPUT and FORWARD for the hub namespace in the
// CR-A4-3 shape; the supervisor adds the loopback accepts. On refusal,
// Rules.Revision is the refused revision when current names one.
func RenderRules(snapshotDir, group string, gen peergroup.Generation, hubPort int, resolve InterfaceResolver) (Rules, error) {
	refused := Rules{Revision: currentRevision(snapshotDir)}
	st, err := loadSnapshot(snapshotDir, group, gen, false)
	if err != nil {
		return refused, err
	}
	refused.Revision = st.revision
	if hubPort != 0 && hubPort != st.hubPort {
		return refused, fmt.Errorf("snapshot %d: hub port %d, hub listens on %d", st.revision, st.hubPort, hubPort)
	}
	if resolve == nil {
		resolve = LocalInterface
	}
	out := Rules{Revision: st.revision}
	var in, outRules []string
	for _, name := range slices.Sorted(maps.Keys(st.members)) {
		m := st.members[name]
		ifname, err := resolve(m.hub)
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("member %s: %v; no rules rendered", name, err))
			continue
		}
		if !validInterfaceName(ifname) {
			return refused, fmt.Errorf("member %s: interface name %q is not safe to render", name, ifname)
		}
		in = append(in, fmt.Sprintf("-A INPUT -i %s -s %s/32 -d %s/32 -p tcp --dport %d -j ACCEPT", ifname, m.gateway, m.hub, st.hubPort))
		if m.AdapterPort != 0 {
			outRules = append(outRules, fmt.Sprintf("-A OUTPUT -o %s -s %s/32 -d %s/32 -p tcp --dport %d -j ACCEPT", ifname, m.hub, m.gateway, m.AdapterPort))
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# enclave-a2a-hub group %s revision %d\n", st.group, st.revision)
	b.WriteString("*filter\n:INPUT DROP [0:0]\n:FORWARD DROP [0:0]\n:OUTPUT DROP [0:0]\n")
	b.WriteString("-A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n")
	for _, r := range in {
		b.WriteString(r + "\n")
	}
	b.WriteString("-A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n")
	for _, r := range outRules {
		b.WriteString(r + "\n")
	}
	b.WriteString("COMMIT\n")
	out.Restore = b.String()
	return out, nil
}

// LocalInterface finds the interface holding addr; names are never cached
// because they change across hub restart and reconnect (P1-D finding 6).
func LocalInterface(addr netip.Addr) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip, ok := netip.AddrFromSlice(ipnet.IP); ok && ip.Unmap() == addr {
				return ifc.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no interface holds %s", addr)
}

func validInterfaceName(name string) bool {
	if name == "" || len(name) > 15 {
		return false
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}
