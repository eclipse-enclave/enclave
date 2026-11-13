// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"slices"
	"strings"
	"testing"
)

const (
	proxyUID    = "105"
	sessionUID  = "1000"
	governedSet = "enclave_governed"
	allowedSet  = "enclave_allowed"
)

// outboundPacket is the first packet of a new outbound connection from the
// shared network namespace. sets lists the ipsets that hold its destination.
type outboundPacket struct {
	uid      string
	protocol string
	port     string
	dst      string
	sets     []string
}

// outputVerdict evaluates the recorded nat and filter OUTPUT rules of engine
// for packet: nat first, as the kernel does for locally generated packets,
// then filter.
func outputVerdict(t *testing.T, calls []firewallCall, engine string, packet outboundPacket) string {
	t.Helper()
	natRules, filterRules, policy := outputRules(calls, engine)
	iface, local := "eth0", packet.dst == "127.0.0.1"
	for _, rule := range natRules {
		if verdict, ok := matchOutput(t, rule, packet, iface, local); ok {
			if verdict == "REDIRECT" {
				packet.dst, packet.sets, packet.port = "127.0.0.1", nil, rule[slices.Index(rule, "--to-ports")+1]
				iface, local = "lo", true
			}
			break
		}
	}
	for _, rule := range filterRules {
		if verdict, ok := matchOutput(t, rule, packet, iface, local); ok {
			return verdict
		}
	}
	return policy
}

func outputRules(calls []firewallCall, engine string) (nat [][]string, filter [][]string, policy string) {
	policy = "ACCEPT"
	for _, call := range calls {
		if call.engine != engine {
			continue
		}
		args := call.args
		table := "filter"
		if len(args) > 2 && args[0] == "-t" {
			table, args = args[1], args[2:]
		}
		if len(args) < 2 || args[1] != "OUTPUT" {
			continue
		}
		switch {
		case table == "nat" && args[0] == "-F":
			nat = nil
		case table == "nat" && args[0] == "-A":
			nat = append(nat, args[2:])
		case table == "filter" && args[0] == "-P":
			policy = args[2]
		case table == "filter" && args[0] == "-F":
			filter = nil
		case table == "filter" && args[0] == "-A":
			filter = append(filter, args[2:])
		}
	}
	return nat, filter, policy
}

func matchOutput(t *testing.T, rule []string, packet outboundPacket, iface string, local bool) (string, bool) {
	t.Helper()
	matches := true
	verdict := ""
	for i := 0; i < len(rule); i++ {
		arg := rule[i]
		next := func() string {
			i++
			return rule[i]
		}
		switch arg {
		case "-m", "--to-ports", "--to-destination":
			next()
		case "--state":
			state := next()
			matches = matches && strings.Contains(state, "NEW")
		case "-o":
			value := next()
			matches = matches && iface == value
		case "-p":
			value := next()
			matches = matches && packet.protocol == value
		case "--dport":
			value := next()
			matches = matches && packet.port == value
		case "-d":
			value := next()
			matches = matches && packet.dst == value
		case "--match-set":
			set := next()
			if next() != "dst" {
				t.Fatalf("unsupported set match in %v", rule)
			}
			matches = matches && slices.Contains(packet.sets, set)
		case "--uid-owner":
			value := next()
			matches = matches && packet.uid == value
		case "--dst-type":
			value := next()
			matches = matches && value == "LOCAL" && local
		case "-j":
			verdict = next()
		default:
			t.Fatalf("unsupported output rule %v", rule)
		}
	}
	return verdict, matches
}

// With request filters, the governed addresses are reachable only through
// the proxy: HTTP/3 over UDP 443, other ports, and anything not redirected
// with a token of the agent's own would bypass the filter.
func TestGatewayFirewallConfinesGovernedAddressesToTheProxy(t *testing.T) {
	calls, err := runGatewayFirewall(t, "setup_firewall && setup_kernel_network && setup_firewall",
		"PROXY_ENABLED=true", "ENCLAVE_FILTER_DIR=/etc/enclave-filters")
	if err != nil {
		t.Fatal(err)
	}
	governed := []string{allowedSet, governedSet}
	allowed := []string{allowedSet}
	for _, tc := range []struct {
		name   string
		packet outboundPacket
		want   string
	}{
		{"session https to a governed host goes to the proxy", outboundPacket{sessionUID, "tcp", "443", "142.250.1.1", governed}, "ACCEPT"},
		{"session http to a governed host goes to the proxy", outboundPacket{sessionUID, "tcp", "80", "142.250.1.1", governed}, "ACCEPT"},
		{"session http3 to a governed host", outboundPacket{sessionUID, "udp", "443", "142.250.1.1", governed}, "REJECT"},
		{"session other tcp port to a governed host", outboundPacket{sessionUID, "tcp", "8443", "142.250.1.1", governed}, "REJECT"},
		{"session udp to a governed host", outboundPacket{sessionUID, "udp", "53", "142.250.1.1", governed}, "REJECT"},
		{"proxy https to a governed host", outboundPacket{proxyUID, "tcp", "443", "142.250.1.1", governed}, "ACCEPT"},
		{"proxy http to a governed host", outboundPacket{proxyUID, "tcp", "80", "142.250.1.1", governed}, "ACCEPT"},
		{"proxy http3 to a governed host", outboundPacket{proxyUID, "udp", "443", "142.250.1.1", governed}, "REJECT"},
		{"proxy other port to a governed host", outboundPacket{proxyUID, "tcp", "8443", "142.250.1.1", governed}, "REJECT"},
		{"session http3 to another allowlisted host", outboundPacket{sessionUID, "udp", "443", "140.82.1.1", allowed}, "ACCEPT"},
		{"session ssh to another allowlisted host", outboundPacket{sessionUID, "tcp", "22", "140.82.1.1", allowed}, "ACCEPT"},
		{"session udp to an unlisted address", outboundPacket{sessionUID, "udp", "443", "203.0.113.1", nil}, "DROP"},
		{"dnsmasq to its resolver", outboundPacket{proxyUID, "udp", "53", "1.1.1.1", nil}, "ACCEPT"},
		{"session to the resolver", outboundPacket{sessionUID, "udp", "53", "1.1.1.1", nil}, "DROP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := outputVerdict(t, calls, "iptables", tc.packet); got != tc.want {
				t.Fatalf("verdict = %s, want %s", got, tc.want)
			}
		})
	}
	// IPv6 egress stays closed in restricted mode, governed or not.
	if got := outputVerdict(t, calls, "ip6tables", outboundPacket{sessionUID, "udp", "443", "2001:db8::1", governed}); got != "DROP" {
		t.Fatalf("IPv6 verdict = %s, want DROP", got)
	}
}

// A reload re-runs setup_firewall on a live namespace. In every pass, after
// OUTPUT is flushed under a DROP policy, the proxy redirects and the governed
// rejects must be in place before any rule accepts allowlisted addresses.
func TestGatewayFirewallReloadOrdersRedirectsBeforeAllowlist(t *testing.T) {
	for _, filters := range []bool{false, true} {
		env := []string{"PROXY_ENABLED=true", "ENCLAVE_IDE_BRIDGE_PORTS=9800"}
		if filters {
			env = append(env, "ENCLAVE_FILTER_DIR=/etc/enclave-filters")
		}
		calls, err := runGatewayFirewall(t, lockinFirewallMocks+"setup_firewall && setup_kernel_network && setup_firewall", env...)
		if err != nil {
			t.Fatal(err)
		}
		passes := 0
		redirected, rejected, flushed := false, false, false
		for _, call := range calls {
			if call.engine != "iptables" {
				continue
			}
			rule := strings.Join(call.args, " ")
			switch {
			case rule == "-F OUTPUT":
				passes++
				redirected, rejected, flushed = false, false, true
			case rule == "-t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports "+proxyHTTPSPort:
				redirected = true
			case strings.Contains(rule, "--match-set "+governedSet+" dst -j REJECT"):
				rejected = true
			case flushed && strings.HasPrefix(rule, "-A OUTPUT") && strings.HasSuffix(rule, "-j ACCEPT") &&
				!strings.Contains(rule, "-o lo") && !strings.Contains(rule, "--state") && !strings.Contains(rule, "--dport 53") &&
				!strings.Contains(rule, "--dst-type LOCAL") && !strings.Contains(rule, governedSet):
				if !redirected || (filters && !rejected) {
					t.Fatalf("filters=%v pass %d: %q accepts egress before the redirects and governed rejects", filters, passes, rule)
				}
			}
		}
		if passes != 2 {
			t.Fatalf("filters=%v: %d setup passes, want 2", filters, passes)
		}
	}
}

func TestGatewayFirewallFiltersFailClosed(t *testing.T) {
	t.Run("REJECT unavailable falls back to DROP", func(t *testing.T) {
		const reject = "-A OUTPUT -m set --match-set enclave_governed dst -j REJECT"
		calls, err := runGatewayFirewall(t, "setup_firewall", "PROXY_ENABLED=true", "ENCLAVE_FILTER_DIR=/etc/enclave-filters",
			"FAIL_ENGINE=iptables", "FAIL_RULE="+reject)
		if err != nil {
			t.Fatal(err)
		}
		// The mock records the rule it fails. The kernel never installed it.
		calls = slices.DeleteFunc(calls, func(c firewallCall) bool { return strings.Join(c.args, " ") == reject })
		packet := outboundPacket{sessionUID, "udp", "443", "142.250.1.1", []string{allowedSet, governedSet}}
		if got := outputVerdict(t, calls, "iptables", packet); got != "DROP" {
			t.Fatalf("verdict = %s, want DROP", got)
		}
	})
	for name, env := range map[string][]string{
		"without the proxy":     {"PROXY_ENABLED=false"},
		"nat flush fails":       {"PROXY_ENABLED=true", "FAIL_ENGINE=iptables", "FAIL_RULE=-t nat -F OUTPUT"},
		"redirect fails":        {"PROXY_ENABLED=true", "FAIL_ENGINE=iptables", "FAIL_RULE=-t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports " + proxyHTTPSPort},
		"governed accept fails": {"PROXY_ENABLED=true", "FAIL_ENGINE=iptables", "FAIL_RULE=-A OUTPUT -p tcp --dport 443 -m set --match-set enclave_governed dst -m owner --uid-owner 105 -j ACCEPT"},
	} {
		t.Run(name, func(t *testing.T) {
			env = append(env, "ENCLAVE_FILTER_DIR=/etc/enclave-filters")
			if _, err := runGatewayFirewall(t, "if setup_firewall; then exit 0; else exit 1; fi", env...); err == nil {
				t.Fatal("firewall installation reported success")
			}
		})
	}
}

func TestGatewayIPSetsWithFilters(t *testing.T) {
	for _, filters := range []bool{false, true} {
		env := []string{}
		if filters {
			env = append(env, "ENCLAVE_FILTER_DIR=/etc/enclave-filters")
		}
		out, err := runGatewayDNSMasq(t, `ipset() { printf 'ipset %s\n' "$*"; }
create_ipset
atomic_swap_ipset`, env...)
		if err != nil {
			t.Fatalf("filters=%v: %v\n%s", filters, err, out)
		}
		for _, want := range []string{"create enclave_governed hash:net -exist", "flush enclave_governed", "swap enclave_governed_reload enclave_governed"} {
			if strings.Contains(out, want) != filters {
				t.Fatalf("filters=%v: output %q contains %q = %v", filters, out, want, !filters)
			}
		}
		if !strings.Contains(out, "swap enclave_allowed_reload enclave_allowed") {
			t.Fatalf("allowlist set not swapped:\n%s", out)
		}
	}
}
