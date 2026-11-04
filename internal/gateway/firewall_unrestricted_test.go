// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestUnrestrictedGatewayFirewall pins the rules an observing gateway
// installs: TCP 80/443 redirected to the proxy for IPv4 and IPv6, the
// published, loopback and IDE bridge forwarding of restricted mode, and
// nothing that filters traffic, disables IPv6 or takes over DNS.
func TestUnrestrictedGatewayFirewall(t *testing.T) {
	calls, err := runGatewayFirewall(t, lockinFirewallMocks+"setup_observing_network",
		"ENCLAVE_GATEWAY_PUBLISHED_PORTS=1455/tcp,3000/tcp",
		"ENCLAVE_LOOPBACK_PORTS=1455",
		"ENCLAVE_IDE_BRIDGE_PORTS=9800",
		"PROXY_ENABLED=true",
	)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, call := range calls {
		line := call.engine + " " + strings.Join(call.args, " ")
		out.WriteString(line + "\n")
		if call.engine == "sysctl" {
			t.Errorf("observing gateway changed an IPv6 setting: %s", line)
		}
		if slices.Contains(call.args, "-P") || slices.Contains(call.args, "-F") || slices.Contains(call.args, "DROP") || slices.Contains(call.args, "REJECT") {
			t.Errorf("observing gateway installed a filter: %s", line)
		}
		if slices.Contains(call.args, "INPUT") {
			t.Errorf("observing gateway touched inbound traffic: %s", line)
		}
	}
	for _, engine := range []string{"iptables", "ip6tables"} {
		for _, want := range []string{
			"-t nat -A OUTPUT -p tcp --dport 80 -j REDIRECT --to-ports 8080",
			"-t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports 8443",
		} {
			if !strings.Contains(out.String(), engine+" "+want+"\n") {
				t.Errorf("missing %s %s", engine, want)
			}
		}
	}
	assertLockinGolden(t, "unrestricted-firewall-startup", out.String())
}

// An IPv6 stack without NAT support leaves IPv6 web traffic unlogged rather
// than failing the gateway: logging must never cost connectivity.
func TestUnrestrictedGatewayFirewallToleratesMissingIPv6NAT(t *testing.T) {
	_, err := runGatewayFirewall(t, lockinFirewallMocks+"setup_observing_network",
		"PROXY_ENABLED=true",
		"FAIL_ENGINE=ip6tables",
		"FAIL_RULE=-t nat -A OUTPUT -p tcp --dport 80 -m owner --uid-owner 105 -j RETURN",
	)
	if err != nil {
		t.Fatalf("observing gateway failed without IPv6 NAT: %v", err)
	}
}

func TestUnrestrictedGatewayFirewallIPv4ErrorsPropagate(t *testing.T) {
	_, err := runGatewayFirewall(t, lockinFirewallMocks+"setup_observing_network",
		"PROXY_ENABLED=true",
		"FAIL_ENGINE=iptables",
		"FAIL_RULE=-t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports 8443",
	)
	if err == nil {
		t.Fatal("observing gateway ignored a failed IPv4 redirect")
	}
}

// A failing observing gateway must not lock the session's network down.
func TestUnrestrictedGatewayShutdownDoesNotLockDown(t *testing.T) {
	calls, err := runGatewayFirewall(t, `firewall_mock marker start; GATEWAY_MODE=unrestricted; shutdown_gateway 1 "proxy exited unexpectedly"`)
	if err == nil {
		t.Fatal("shutdown_gateway 1 exited successfully")
	}
	if len(calls) != 1 {
		t.Fatalf("observing gateway changed the firewall on shutdown: %+v", calls)
	}
}

func TestReadGatewayMode(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		meta string
		want string
	}{
		{"unrestricted", `{"generation": "1", "mode": "unrestricted"}`, "unrestricted"},
		{"restricted", `{"mode": "restricted"}`, "restricted"},
		{"no mode", `{"generation": "1"}`, "restricted"},
		{"unknown mode", `{"mode": "open"}`, "restricted"},
		{"missing meta", "", "restricted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := filepath.Join(dir, tc.name)
			if err := os.MkdirAll(configDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if tc.meta != "" {
				if err := os.WriteFile(filepath.Join(configDir, "meta.json"), []byte(tc.meta), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls, err := runGatewayFirewall(t, `read_gateway_mode; firewall_mock mode "$GATEWAY_MODE"`, "ENCLAVE_GATEWAY_CONFIG_DIR="+configDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || strings.Join(calls[0].args, " ") != tc.want {
				t.Fatalf("mode = %+v, want %s", calls, tc.want)
			}
		})
	}
}
