// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateLockin regenerates the gateway lock-in snapshots. Run:
//
//	go test ./internal/gateway/ -run TestLockin -update-lockin
var updateLockin = flag.Bool("update-lockin", false, "rewrite the gateway lock-in golden files")

// lockinFirewallMocks log the IPv6 sysctls (the IPv4 route_localnet writes
// depend on the host's /proc layout) and resolve the IDE bridge host so its
// NAT rules are recorded.
const lockinFirewallMocks = `
sysctl() {
    if [ "$1" = "-n" ]; then printf '1\n'; return 0; fi
    case "$*" in *ipv6*) firewall_mock sysctl "$@" ;; esac
}
enclave_resolve_docker_host_ipv4() { printf '172.17.0.1\n'; }
`

// TestLockinRestrictedGatewayFirewall pins the complete firewall and NAT rule
// sequence a restricted gateway installs at startup and on fail-closed
// lockdown.
func TestLockinRestrictedGatewayFirewall(t *testing.T) {
	env := []string{
		"ENCLAVE_GATEWAY_PUBLISHED_PORTS=1455/tcp,3000/tcp,5353/udp",
		"ENCLAVE_LOOPBACK_PORTS=1455",
		"ENCLAVE_IDE_BRIDGE_PORTS=9800",
	}
	for _, tc := range []struct {
		name       string
		invocation string
		env        []string
	}{
		{"startup-proxy", "setup_firewall && setup_kernel_network", []string{"PROXY_ENABLED=true"}},
		{"startup-no-proxy", "setup_firewall && setup_kernel_network", []string{"PROXY_ENABLED=false"}},
		{"lockdown", "enforce_fail_closed_lockdown", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, err := runGatewayFirewall(t, lockinFirewallMocks+tc.invocation, append(append([]string{}, env...), tc.env...)...)
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			for _, call := range calls {
				out.WriteString(call.engine + " " + strings.Join(call.args, " ") + "\n")
			}
			assertLockinGolden(t, "restricted-firewall-"+tc.name, out.String())
		})
	}
}

func assertLockinGolden(t *testing.T, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", "lockin", name+".golden")
	if *updateLockin {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- fixed testdata path.
	if err != nil {
		t.Fatalf("read golden %s (regenerate with -update-lockin): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("%s drifted from %s (regenerate with -update-lockin only for an intended change)\n--- got ---\n%s", name, path, got)
	}
}
