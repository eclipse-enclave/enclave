// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"strings"
	"testing"
)

func TestIsBuildNetworkDNSFailure(t *testing.T) {
	for _, output := range []string{
		"Err:1 http://deb.debian.org/debian trixie InRelease\n  Temporary failure resolving 'deb.debian.org'",
		"curl: (6) Could not resolve host: github.com",
		"go: module golang.org/x/vuln: Get \"https://proxy.golang.org/...\": dial tcp: lookup proxy.golang.org: no such host",
		"fetch https://dl-cdn.alpinelinux.org/alpine/v3.20/main/x86_64/APKINDEX.tar.gz\nERROR: ... temporary error (try again later)\nunable to resolve host",
		// The tool installs run through npm, so its errno spelling must count
		// as a DNS failure or the step most likely to hit a DNS-broken build
		// network is the one that misses the remedy.
		"npm error network request to https://registry.npmjs.org/@anthropic-ai%2fsandbox-runtime failed, reason: getaddrinfo ENOTFOUND registry.npmjs.org",
		"npm error network request failed, reason: getaddrinfo EAI_AGAIN registry.npmjs.org",
	} {
		if !IsBuildNetworkDNSFailure(output) {
			t.Errorf("expected DNS failure for %q", output)
		}
	}
	for _, output := range []string{
		"curl: (28) Operation timed out after 60001 milliseconds with 0 bytes received",
		"ERROR: process \"/bin/sh -c false\" did not complete successfully: exit code: 1",
		"",
	} {
		if IsBuildNetworkDNSFailure(output) {
			t.Errorf("did not expect DNS failure for %q", output)
		}
	}
}

func TestIsTransientNetworkFailure(t *testing.T) {
	for _, output := range []string{
		"curl: (28) Operation timed out after 60001 milliseconds with 0 bytes received",
		"read tcp 10.0.2.100:44210->151.101.1.6:443: read: connection reset by peer",
		"Temporary failure resolving 'deb.debian.org'",
		"go install govulncheck: transient network failure; retrying (attempt 2/5) in 5s\ngo install govulncheck: failed after 5 attempt(s)",
		"curl https://x/y: attempt 3 timed out after 900s",
		"npm ERR! network request failed, reason: read ECONNRESET",
	} {
		if !IsTransientNetworkFailure(output) {
			t.Errorf("expected transient network failure for %q", output)
		}
	}
	for _, output := range []string{
		"curl: (22) The requested URL returned error: 404",
		"sha256sum: WARNING: 1 computed checksum did NOT match",
		"ERROR: process \"/bin/sh -c exit 1\" did not complete successfully: exit code: 1",
	} {
		if IsTransientNetworkFailure(output) {
			t.Errorf("did not expect transient network failure for %q", output)
		}
	}
}

func TestBuildFailureHint(t *testing.T) {
	if hint := BuildFailureHint("Temporary failure resolving 'deb.debian.org'"); !strings.Contains(hint, "network transfer failed or timed out") {
		t.Fatalf("expected network hint, got %q", hint)
	}
	if hint := BuildFailureHint("exit code: 1"); hint != "" {
		t.Fatalf("expected no hint for a generic failure, got %q", hint)
	}
}
