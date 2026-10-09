// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/docker"
)

func TestGatewayPublishedPorts(t *testing.T) {
	bindings := docker.PortMap{
		"3000/tcp": {{HostIP: "127.0.0.1", HostPort: "13000"}, {HostIP: "::1", HostPort: "13000"}},
		"5353/udp": {{HostIP: "127.0.0.1", HostPort: "15353"}},
		"1455":     {{HostIP: "127.0.0.1", HostPort: "1455"}},
		"9229/tcp": nil,
	}
	if got := gatewayPublishedPorts(bindings); got != "1455/tcp,3000/tcp,5353/udp" {
		t.Fatalf("published container ports = %q", got)
	}
	if got := gatewayPublishedPorts(nil); got != "" {
		t.Fatalf("unpublished session ports = %q", got)
	}
}

type firewallCall struct {
	engine string
	args   []string
}

func runGatewayFirewall(t *testing.T, invocation string, env ...string) ([]firewallCall, error) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "gateway-entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// Source the shipping functions without starting gateway services on the host.
	source, ok := strings.CutSuffix(string(data), "\nmain \"$@\"\n")
	if !ok {
		t.Fatal("gateway entrypoint main invocation not found")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "gateway.sh")
	if err := os.WriteFile(scriptPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "firewall.log")
	cmd := exec.Command("sh", "-c", `. "$1"
firewall_mock() {
    engine="$1"
    shift
    printf '%s\t%s\n' "$engine" "$*" >> "$FW_LOG"
    if [ "$engine" = "${FAIL_ENGINE:-}" ] && [ "$*" = "${FAIL_RULE:-}" ]; then
        return 1
    fi
}
iptables() { firewall_mock iptables "$@"; }
ip6tables() { firewall_mock ip6tables "$@"; }
sysctl() { if [ "$1" = "-n" ]; then printf '1\n'; fi; }
id() { printf '105\n'; }
UNIQUE_RESOLVERS=1.1.1.1
`+invocation, "gateway-firewall-test", scriptPath)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"ENCLAVE_NET_LIB=" + filepath.Join(dir, "missing-net.sh"),
		"FW_LOG=" + logPath,
	}, env...)
	out, runErr := cmd.CombinedOutput()
	if len(out) > 0 {
		t.Logf("gateway output: %s", out)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var calls []firewallCall
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		engine, args, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("invalid firewall call: %q", line)
		}
		calls = append(calls, firewallCall{engine: engine, args: strings.Fields(args)})
	}
	return calls, runErr
}

type inboundPacket struct {
	iface    string
	state    string
	protocol string
	port     string
}

// inputVerdict evaluates the recorded filter rules independently of bridge
// isolation, as if a peer can route directly to the gateway namespace.
func inputVerdict(t *testing.T, calls []firewallCall, engine string, packet inboundPacket) string {
	t.Helper()
	policy := "ACCEPT"
	var rules [][]string
	for _, call := range calls {
		args := call.args
		if call.engine != engine || len(args) < 2 || args[1] != "INPUT" {
			continue
		}
		switch args[0] {
		case "-P":
			policy = args[2]
		case "-F":
			rules = nil
		case "-A":
			rules = append(rules, args[2:])
		}
	}
	for _, rule := range rules {
		matches := true
		verdict := ""
		for i := 0; i < len(rule); i += 2 {
			switch rule[i] {
			case "-i":
				matches = matches && packet.iface == rule[i+1]
			case "-p":
				matches = matches && packet.protocol == rule[i+1]
			case "--dport":
				matches = matches && packet.port == rule[i+1]
			case "--state":
				matches = matches && containsState(rule[i+1], packet.state)
			case "-m":
				if rule[i+1] != "state" {
					t.Fatalf("unsupported match in rule %v", rule)
				}
			case "-j":
				verdict = rule[i+1]
			default:
				t.Fatalf("unsupported input rule %v", rule)
			}
		}
		if matches {
			return verdict
		}
	}
	return policy
}

func containsState(states string, state string) bool {
	for _, candidate := range strings.Split(states, ",") {
		if candidate == state {
			return true
		}
	}
	return false
}

func TestGatewayFirewallInboundPolicy(t *testing.T) {
	for _, proxy := range []string{"true", "false"} {
		t.Run("proxy="+proxy, func(t *testing.T) {
			calls, err := runGatewayFirewall(t, "if ! setup_firewall || ! setup_kernel_network || ! setup_firewall; then exit 1; fi",
				"PROXY_ENABLED="+proxy,
				"ENCLAVE_GATEWAY_PUBLISHED_PORTS=1455/tcp,3000/tcp,5353/udp",
				"ENCLAVE_LOOPBACK_PORTS=1455",
				"ENCLAVE_IDE_BRIDGE_PORTS=9800",
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, engine := range []string{"iptables", "ip6tables"} {
				policy := "ACCEPT"
				for _, call := range calls {
					if call.engine == engine && len(call.args) == 3 && call.args[0] == "-P" && call.args[1] == "INPUT" {
						policy = call.args[2]
						if policy != "DROP" {
							t.Fatal("temporarily opened the input policy")
						}
					}
					if call.engine == engine && strings.Join(call.args, " ") == "-F INPUT" && policy != "DROP" {
						t.Fatal("flushed input rules before establishing a default-drop policy")
					}
				}
				for _, tc := range []struct {
					name     string
					packet   inboundPacket
					accepted bool
				}{
					{"unpublished listener", inboundPacket{"eth0", "NEW", "tcp", "9999"}, false},
					{"dns udp", inboundPacket{"eth0", "NEW", "udp", "53"}, false},
					{"dns tcp", inboundPacket{"eth0", "NEW", "tcp", "53"}, false},
					{"http proxy", inboundPacket{"eth0", "NEW", "tcp", "8080"}, false},
					{"tls proxy", inboundPacket{"eth0", "NEW", "tcp", "8443"}, false},
					{"published tcp", inboundPacket{"eth0", "NEW", "tcp", "3000"}, true},
					{"host port is not container port", inboundPacket{"eth0", "NEW", "tcp", "13000"}, false},
					{"published udp", inboundPacket{"eth0", "NEW", "udp", "5353"}, true},
					{"udp does not open tcp", inboundPacket{"eth0", "NEW", "tcp", "5353"}, false},
					{"tcp does not open udp", inboundPacket{"eth0", "NEW", "udp", "3000"}, false},
					{"oauth callback", inboundPacket{"eth0", "NEW", "tcp", "1455"}, true},
					{"outbound IDE bridge is not inbound", inboundPacket{"eth0", "NEW", "tcp", "9800"}, false},
					{"local dns", inboundPacket{"lo", "NEW", "udp", "53"}, true},
					{"local proxy", inboundPacket{"lo", "NEW", "tcp", "8443"}, true},
					{"established reply", inboundPacket{"eth0", "ESTABLISHED", "tcp", "40000"}, true},
					{"related reply", inboundPacket{"eth0", "RELATED", "icmp", ""}, true},
				} {
					t.Run(engine+"/"+tc.name, func(t *testing.T) {
						if got := inputVerdict(t, calls, engine, tc.packet); (got == "ACCEPT") != tc.accepted {
							t.Fatalf("inbound verdict = %s, want accepted=%v", got, tc.accepted)
						}
					})
				}
			}
		})
	}
}

func TestGatewayFirewallWithoutPublishedPorts(t *testing.T) {
	calls, err := runGatewayFirewall(t, "setup_firewall; setup_kernel_network",
		"ENCLAVE_LOOPBACK_PORTS=1455")
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"iptables", "ip6tables"} {
		for _, port := range []string{"53", "1455", "3000", "8080", "8443", "9999"} {
			packet := inboundPacket{"eth0", "NEW", "tcp", port}
			if got := inputVerdict(t, calls, engine, packet); got != "DROP" {
				t.Fatalf("%s accepted unpublished port %s", engine, port)
			}
		}
	}
}

func TestGatewayFirewallInputErrorsPropagate(t *testing.T) {
	for _, engine := range []string{"iptables", "ip6tables"} {
		for _, rule := range []string{
			"-P INPUT DROP", "-F INPUT", "-A INPUT -i lo -j ACCEPT",
			"-A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT",
			"-A INPUT -p tcp --dport 3000 -j ACCEPT",
		} {
			t.Run(engine+"/"+rule, func(t *testing.T) {
				_, err := runGatewayFirewall(t, "if setup_firewall && setup_kernel_network; then exit 0; else exit 1; fi",
					"ENCLAVE_GATEWAY_PUBLISHED_PORTS=3000/tcp",
					"FAIL_ENGINE="+engine, "FAIL_RULE="+rule)
				if err == nil {
					t.Fatal("firewall installation reported success after an input rule failed")
				}
			})
		}
	}
}

func TestGatewayFirewallRejectsInvalidPublishedPorts(t *testing.T) {
	for _, port := range []string{"3000", "3000/any", "0/tcp", "65536/udp", "1:65535/tcp", "3000 /tcp", "3000/tcp/udp"} {
		t.Run(port, func(t *testing.T) {
			_, err := runGatewayFirewall(t, "if setup_firewall; then exit 0; else exit 1; fi",
				"ENCLAVE_GATEWAY_PUBLISHED_PORTS="+port)
			if err == nil {
				t.Fatal("firewall accepted an invalid published port")
			}
		})
	}
}

func TestGatewayFirewallLockdownClosesInbound(t *testing.T) {
	calls, err := runGatewayFirewall(t, "setup_firewall; setup_kernel_network; enforce_fail_closed_lockdown",
		"ENCLAVE_GATEWAY_PUBLISHED_PORTS=3000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"iptables", "ip6tables"} {
		for _, packet := range []inboundPacket{
			{"eth0", "NEW", "tcp", "3000"},
			{"eth0", "ESTABLISHED", "tcp", "40000"},
			{"lo", "NEW", "tcp", "8443"},
		} {
			if got := inputVerdict(t, calls, engine, packet); got != "DROP" {
				t.Fatalf("%s input verdict after lockdown = %s for %+v", engine, got, packet)
			}
		}
	}
}
