// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import "regexp"

// buildDNSFailurePattern matches name resolution failing inside a build step.
// On some Docker BuildKit setups the default build network cannot reach the
// host's resolver, and this is the only symptom; a host that lost connectivity
// shows the same text, which is why callers also check the engine.
var buildDNSFailurePattern = regexp.MustCompile(`(?i)(` +
	`Temporary failure (resolving|in name resolution)` +
	`|Could not resolve host` +
	`|Could not resolve '` +
	`|no such host` +
	`|Name or service not known` +
	`|server misbehaving` +
	`|unable to resolve host` +
	`|Name does not resolve` +
	// npm and other Node tools report a failed lookup as a bare getaddrinfo
	// errno. The tool installs run through npm, so without these a DNS-broken
	// build network would miss the ENCLAVE_BUILD_NETWORK=host remedy on the
	// step most likely to hit it.
	`|getaddrinfo (ENOTFOUND|EAI_AGAIN)` +
	`)`)

// buildTransientNetworkPattern matches transfers that failed or timed out
// during a build step. Mirrors the shell-side classifier in
// runtime-assets/build-scripts/lib/common.sh.
var buildTransientNetworkPattern = regexp.MustCompile(`(?i)(` +
	`i/o timeout` +
	`|TLS handshake timeout` +
	`|timed out` +
	`|network is unreachable` +
	`|connection reset by peer` +
	`|connection refused` +
	`|proxyconnect tcp` +
	`|dial tcp` +
	`|unexpected EOF` +
	`|context deadline exceeded` +
	`|Failed to fetch` +
	`|Could not connect to` +
	`|Unable to connect to` +
	`|transfer closed` +
	`|(Recv|Send) failure` +
	`|Hash Sum mismatch` +
	`|E(CONNRESET|TIMEDOUT|NOTFOUND|AI_AGAIN|CONNREFUSED|HOSTUNREACH|NETUNREACH)` +
	`|curl: \(([67]|1[68]|2[38]|35|5[2567]|92)\)` +
	`|returned error: (408|425|429|5[0-9][0-9])` +
	`|transient network failure` +
	`|attempt \d+ timed out` +
	`)`)

// IsBuildNetworkDNSFailure reports whether build output shows name resolution
// failing inside the build.
func IsBuildNetworkDNSFailure(output string) bool {
	return buildDNSFailurePattern.MatchString(output)
}

// IsTransientNetworkFailure reports whether build output shows a network
// transfer failing or timing out, including DNS failures.
func IsTransientNetworkFailure(output string) bool {
	return IsBuildNetworkDNSFailure(output) || buildTransientNetworkPattern.MatchString(output)
}

// BuildFailureHint returns a one-line explanation of a failed build's likely
// cause derived from its output, or "" when the output offers nothing beyond
// the engine error.
func BuildFailureHint(output string) string {
	if IsTransientNetworkFailure(output) {
		return "a network transfer failed or timed out during the build; check connectivity and rerun"
	}
	return ""
}
