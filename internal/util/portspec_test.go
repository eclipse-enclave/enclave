// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import "testing"

func TestParsePortSpec(t *testing.T) {
	cases := []struct {
		in             string
		ip, host, cont string
		ok             bool
	}{
		{"3000", "", "3000", "3000", true},
		{"  3000  ", "", "3000", "3000", true},
		{"8080:80", "", "8080", "80", true},
		{"127.0.0.1:8080:80", "127.0.0.1", "8080", "80", true},
		{"0.0.0.0:3000:3000", "0.0.0.0", "3000", "3000", true},
		{"*:3000:3000", "*", "3000", "3000", true},
		{"[::1]:8080:80", "[::1]", "8080", "80", true},
		{"127.0.0.1:3000", "127.0.0.1", "3000", "3000", true},
		{"", "", "", "", false},
		{"abc", "", "", "", false},
		{"1:2:3", "", "", "", false},
		{"3000:", "", "", "", false},
		{"8080:notaport", "", "", "", false},
	}
	for _, c := range cases {
		ip, host, cont, ok := ParsePortSpec(c.in)
		if ok != c.ok || ip != c.ip || host != c.host || cont != c.cont {
			t.Errorf("ParsePortSpec(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.in, ip, host, cont, ok, c.ip, c.host, c.cont, c.ok)
		}
	}
}

func TestSplitPortHostIP(t *testing.T) {
	cases := []struct {
		in       string
		ip, rest string
		hasIP    bool
	}{
		{"8080:80", "", "8080:80", false},
		{"3000", "", "3000", false},
		{"127.0.0.1:8080:80", "127.0.0.1", "8080:80", true},
		{"[::1]:8080:80", "[::1]", "8080:80", true},
		{"*:8080:80", "*", "8080:80", true},
	}
	for _, c := range cases {
		ip, rest, hasIP := SplitPortHostIP(c.in)
		if ip != c.ip || rest != c.rest || hasIP != c.hasIP {
			t.Errorf("SplitPortHostIP(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, ip, rest, hasIP, c.ip, c.rest, c.hasIP)
		}
	}
}

func TestSplitPortMapping(t *testing.T) {
	cases := []struct {
		in         string
		host, cont string
		ok         bool
	}{
		{"3000", "3000", "3000", true},
		{"8080:80", "8080", "80", true},
		{"127.0.0.1:8080:80", "8080", "80", true},
		{"", "", "", false},
		{"abc", "", "", false},
	}
	for _, c := range cases {
		host, cont, ok := SplitPortMapping(c.in)
		if ok != c.ok || host != c.host || cont != c.cont {
			t.Errorf("SplitPortMapping(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, host, cont, ok, c.host, c.cont, c.ok)
		}
	}
}

func TestPortMappingState(t *testing.T) {
	cases := []struct {
		name                          string
		ports                         []string
		target                        string
		wantHost, wantCont, wantExact bool
	}{
		{"exact bare port", []string{"3000"}, "3000", true, true, true},
		{"exact remap", []string{"3000:3000"}, "3000", true, true, true},
		{"host only", []string{"8080:80"}, "8080", true, false, false},
		{"container only", []string{"8080:80"}, "80", false, true, false},
		{"host-ip discarded", []string{"127.0.0.1:3000:3000"}, "3000", true, true, true},
		{"malformed skipped", []string{"bogus", "8080:80"}, "8080", true, false, false},
		{"no match", []string{"8080:80"}, "9090", false, false, false},
		{"empty", nil, "3000", false, false, false},
	}
	for _, c := range cases {
		host, cont, exact := PortMappingState(c.ports, c.target)
		if host != c.wantHost || cont != c.wantCont || exact != c.wantExact {
			t.Errorf("PortMappingState(%v, %q) = (%v,%v,%v), want (%v,%v,%v)",
				c.ports, c.target, host, cont, exact, c.wantHost, c.wantCont, c.wantExact)
		}
	}
}
