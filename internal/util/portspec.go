// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"net"
	"strings"
)

// ParsePortSpec parses a Docker-style publish spec into its parts. The optional
// "/proto" suffix is not handled here (callers assume tcp). Accepted forms:
//
//	"3000"                -> ("", "3000", "3000", true)
//	"8080:80"             -> ("", "8080", "80",  true)
//	"0:80"                -> ("", "0", "80", true)          // OS-assigned host port
//	"127.0.0.1:8080:80"   -> ("127.0.0.1", "8080", "80", true)
//	"127.0.0.1:0:80"      -> ("127.0.0.1", "0", "80", true) // OS-assigned host port
//	"0.0.0.0:8080:80"     -> ("0.0.0.0", "8080", "80", true)
//	"[::1]:8080:80"       -> ("[::1]", "8080", "80", true)
//
// A host port of "0" is Docker's sentinel for "assign a free host port at
// runtime"; the actual port is discoverable afterwards via the container's
// published-port bindings. "0" is only accepted in the host-port position: a
// bare "0" or a "0" container port stays invalid.
//
// hostIP is empty when the spec omits it; callers decide the default (the run
// path defaults to loopback). ok is false for malformed specs.
func ParsePortSpec(value string) (hostIP, hostPort, containerPort string, ok bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", "", false
	}
	ip, rest, hasIP := SplitPortHostIP(value)
	if !hasIP {
		ip = ""
	}
	if IsPortNumber(rest) {
		return ip, rest, rest, true
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 2 || !isHostPort(parts[0]) || !IsPortNumber(parts[1]) {
		return "", "", "", false
	}
	return ip, parts[0], parts[1], true
}

// isHostPort reports whether s is valid in the host-port position of a publish
// spec. This is a real port number or Docker's "0" sentinel requesting an
// OS-assigned host port.
func isHostPort(s string) bool {
	return s == "0" || IsPortNumber(s)
}

// SplitPortMapping returns the host and container ports of a publish spec,
// discarding the optional host-IP. ok is false for malformed specs.
func SplitPortMapping(value string) (hostPort, containerPort string, ok bool) {
	_, hostPort, containerPort, ok = ParsePortSpec(value)
	return hostPort, containerPort, ok
}

// PortMappingState reports whether target appears among ports as a host port,
// as a container port, and as an exact host==container==target mapping. Specs
// that fail to parse are skipped.
func PortMappingState(ports []string, target string) (hasHost, hasContainer, hasExact bool) {
	for _, port := range ports {
		host, container, ok := SplitPortMapping(port)
		if !ok {
			continue
		}
		if host == target {
			hasHost = true
		}
		if container == target {
			hasContainer = true
		}
		if host == target && container == target {
			hasExact = true
		}
	}
	return hasHost, hasContainer, hasExact
}

// SplitPortHostIP separates an optional leading host-IP from a Docker -p value.
// Returns (ip, rest, hasIP). For inputs like "127.0.0.1:8080:80" it returns
// ("127.0.0.1", "8080:80", true); for "8080:80" it returns ("", "8080:80", false).
// IPv6 host-IPs (e.g. "[::1]:8080:80") are recognized by the leading '['.
func SplitPortHostIP(value string) (string, string, bool) {
	if strings.HasPrefix(value, "[") {
		// IPv6 in brackets: [addr]:rest
		end := strings.Index(value, "]")
		if end > 0 && end+1 < len(value) && value[end+1] == ':' {
			return value[:end+1], value[end+2:], true
		}
		return "", value, false
	}
	// IPv4 host-IP: dotted quad followed by ':'.
	first := strings.IndexByte(value, ':')
	if first < 0 {
		return "", value, false
	}
	candidate := value[:first]
	if candidate == "*" || looksLikeIPv4(candidate) {
		return candidate, value[first+1:], true
	}
	return "", value, false
}

// IsUnspecifiedHostIP reports whether ip is a wildcard / "bind to all
// interfaces" host IP. Accepts the bracketed IPv6 form ("[::]", "[::0]",
// "[0:0:0:0:0:0:0:0]"), the IPv4 form ("0.0.0.0"), and Docker's "*" alias.
// All non-zero IPv6 forms (e.g. "[::ffff:0.0.0.0]") that net.ParseIP reports
// as unspecified are also rejected so that an attacker cannot smuggle a
// non-loopback binding past the loopback clamp by using an alternate
// canonicalization of "::".
func IsUnspecifiedHostIP(ip string) bool {
	if ip == "*" {
		return true
	}
	candidate := ip
	if strings.HasPrefix(candidate, "[") && strings.HasSuffix(candidate, "]") {
		candidate = candidate[1 : len(candidate)-1]
	}
	parsed := net.ParseIP(candidate)
	if parsed == nil {
		return false
	}
	return parsed.IsUnspecified()
}

// looksLikeIPv4 reports whether s is a dotted-quad IPv4 literal. We don't need
// full validation here — the goal is to distinguish an IP-prefixed binding
// from a plain "hostPort:containerPort" form.
func looksLikeIPv4(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
