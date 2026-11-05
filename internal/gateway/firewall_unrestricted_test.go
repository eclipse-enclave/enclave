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
	"strconv"
	"strings"
	"testing"

	"enclave/internal/model"
)

var (
	proxyHTTPPort  = strconv.Itoa(model.GatewayProxyHTTPPort)
	proxyHTTPSPort = strconv.Itoa(model.GatewayProxyHTTPSPort)
)

// TestUnrestrictedGatewayFirewall pins the rules an observing gateway
// installs: TCP 80/443 redirected to the proxy for IPv4 and IPv6, the
// published, loopback and IDE bridge forwarding of restricted mode plus the
// local loopback redirect rootless podman needs, and nothing that filters
// traffic, disables IPv6 or takes over DNS.
func TestUnrestrictedGatewayFirewall(t *testing.T) {
	calls, err := runGatewayFirewall(t, loopbackForwardingMocks+"setup_observing_network",
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
		line := call.String()
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
			"-t nat -A OUTPUT -p tcp --dport 80 -j REDIRECT --to-ports " + proxyHTTPPort,
			"-t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports " + proxyHTTPSPort,
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
		"FAIL_RULE=-t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports "+proxyHTTPSPort,
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

// deadProxy starts and reaps a child so PROXY_PID names a process that is gone.
const deadProxy = `true & PROXY_PID=$!; wait "$PROXY_PID"; `

// missingIPv4HTTPRedirect makes the IPv4 port 80 redirect absent: deleting and
// checking it both fail, as iptables does for a rule that is not installed.
const missingIPv4HTTPRedirect = `iptables() {
    firewall_mock iptables "$@"
    case "$*" in
        "-t nat -D OUTPUT -p tcp --dport 80 "*|"-t nat -C OUTPUT -p tcp --dport 80 "*) return 1 ;;
    esac
}
`

func (c firewallCall) String() string {
	return c.engine + " " + strings.Join(c.args, " ")
}

func callLines(calls []firewallCall) []string {
	lines := make([]string, 0, len(calls))
	for _, call := range calls {
		lines = append(lines, call.String())
	}
	return lines
}

func assertCalls(t *testing.T, calls []firewallCall, want []string) {
	t.Helper()
	if got := callLines(calls); !slices.Equal(got, want) {
		t.Fatalf("firewall calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A dead proxy in an observing gateway removes the web redirects so the
// session's traffic keeps flowing unlogged, touches nothing else, and keeps
// the gateway running without acting on the dead proxy again.
func TestUnrestrictedGatewayProxyDeathFailsOpen(t *testing.T) {
	calls, err := runGatewayFirewall(t, deadProxy+`GATEWAY_MODE=unrestricted
monitor_tick
firewall_mock marker second-tick
monitor_tick
firewall_mock marker alive`,
		"PROXY_ENABLED=true",
	)
	if err != nil {
		t.Fatalf("observing gateway exited after proxy death: %v", err)
	}
	assertCalls(t, calls, []string{
		"iptables -t nat -D OUTPUT -p tcp --dport 80 -j REDIRECT --to-ports " + proxyHTTPPort,
		"iptables -t nat -D OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports " + proxyHTTPSPort,
		"ip6tables -t nat -D OUTPUT -p tcp --dport 80 -j REDIRECT --to-ports " + proxyHTTPPort,
		"ip6tables -t nat -D OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports " + proxyHTTPSPort,
		"marker second-tick",
		"marker alive",
	})
}

// The gateway log tells the user that web traffic is no longer logged.
func TestUnrestrictedGatewayFailOpenReportsDegradedState(t *testing.T) {
	calls, err := runGatewayFirewall(t, deadProxy+`GATEWAY_MODE=unrestricted; firewall_mock log "$(monitor_tick | tr '\n' ' ')"`, "PROXY_ENABLED=true")
	if err != nil {
		t.Fatal(err)
	}
	out := callLines(calls)[len(calls)-1]
	if !strings.Contains(out, "Proxy exited unexpectedly") || !strings.Contains(out, "no longer logged") {
		t.Fatalf("degraded state not reported: %q", out)
	}
}

// A redirect that was never installed must not stop the remaining redirects
// from being removed, and is not reported as a failure.
func TestUnrestrictedGatewayFailOpenToleratesMissingRedirect(t *testing.T) {
	calls, err := runGatewayFirewall(t, missingIPv4HTTPRedirect+deadProxy+`GATEWAY_MODE=unrestricted; firewall_mock log "$(monitor_tick | tr '\n' ' ')"`,
		"PROXY_ENABLED=true",
	)
	if err != nil {
		t.Fatalf("observing gateway exited when a redirect was missing: %v", err)
	}
	lines := callLines(calls)
	assertCalls(t, calls[:len(calls)-1], []string{
		"iptables -t nat -D OUTPUT -p tcp --dport 80 -j REDIRECT --to-ports " + proxyHTTPPort,
		"iptables -t nat -C OUTPUT -p tcp --dport 80 -j REDIRECT --to-ports " + proxyHTTPPort,
		"iptables -t nat -D OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports " + proxyHTTPSPort,
		"ip6tables -t nat -D OUTPUT -p tcp --dport 80 -j REDIRECT --to-ports " + proxyHTTPPort,
		"ip6tables -t nat -D OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports " + proxyHTTPSPort,
	})
	if out := lines[len(lines)-1]; !strings.Contains(out, "no longer logged") || strings.Contains(out, "Failed") {
		t.Fatalf("missing redirect reported as a failure: %q", out)
	}
}

// A redirect that survives its removal still points at the dead proxy; the
// gateway says so instead of claiming traffic flows unlogged.
func TestUnrestrictedGatewayFailOpenReportsStuckRedirect(t *testing.T) {
	calls, err := runGatewayFirewall(t, deadProxy+`GATEWAY_MODE=unrestricted; firewall_mock log "$(monitor_tick | tr '\n' ' ')"`,
		"PROXY_ENABLED=true",
		"FAIL_ENGINE=ip6tables",
		"FAIL_RULE=-t nat -D OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports "+proxyHTTPSPort,
	)
	if err != nil {
		t.Fatalf("observing gateway exited when a redirect could not be removed: %v", err)
	}
	out := callLines(calls)[len(calls)-1]
	if !strings.Contains(out, "Failed to remove ip6tables redirect for port 443") || strings.Contains(out, "no longer logged") {
		t.Fatalf("stuck redirect not reported: %q", out)
	}
}

// A dead proxy in a restricted gateway still locks the namespace down.
func TestRestrictedGatewayProxyDeathLocksDown(t *testing.T) {
	calls, err := runGatewayFirewall(t, deadProxy+`DNSMASQ_PID=$$; monitor_tick`, "PROXY_ENABLED=true")
	if err == nil {
		t.Fatal("restricted gateway kept running after proxy death")
	}
	lines := callLines(calls)
	for _, want := range []string{"iptables -P OUTPUT DROP", "ip6tables -P OUTPUT DROP"} {
		if !slices.Contains(lines, want) {
			t.Errorf("missing lockdown rule %q in %q", want, lines)
		}
	}
}

// loopbackForwardingMocks resolve the gateway's own address and record socat
// loopback proxies as firewall calls.
const loopbackForwardingMocks = lockinFirewallMocks + `
socat() { :; }
enclave_resolve_bind_ip() { ip="${BIND_IP-10.88.0.5}"; [ -n "$ip" ] && printf '%s\n' "$ip"; }
enclave_start_socat_loopback_proxy() { firewall_mock socat "$@"; }
`

func loopbackDNAT(chain, match, port string) string {
	return "iptables -t nat -A " + chain + " -p tcp " + match + "--dport " + port + " -j DNAT --to-destination 127.0.0.1:" + port
}

func hasLocalLoopbackDNAT(lines []string) bool {
	return slices.ContainsFunc(lines, func(l string) bool {
		return strings.Contains(l, "-A OUTPUT") && strings.Contains(l, "DNAT --to-destination 127.0.0.1")
	})
}

func countSocat(lines []string) int {
	n := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "socat ") {
			n++
		}
	}
	return n
}

// Rootless podman's port forwarder dials the container address from inside
// the namespace, so its connections pass nat OUTPUT instead of PREROUTING.
func TestUnrestrictedGatewayRedirectsLocalConnectionsToLoopbackListener(t *testing.T) {
	calls, err := runGatewayFirewall(t, loopbackForwardingMocks+"setup_observing_network",
		"ENCLAVE_LOOPBACK_PORTS=1455,1456",
		"PROXY_ENABLED=false",
	)
	if err != nil {
		t.Fatal(err)
	}
	lines := callLines(calls)
	for _, port := range []string{"1455", "1456"} {
		for _, want := range []string{
			loopbackDNAT("PREROUTING", "", port),
			loopbackDNAT("OUTPUT", "-d 10.88.0.5 ", port),
		} {
			if !slices.Contains(lines, want) {
				t.Errorf("missing %q in %q", want, lines)
			}
		}
	}
	if countSocat(lines) != 0 {
		t.Errorf("socat fallback started although DNAT succeeded: %q", lines)
	}
}

func TestUnrestrictedGatewayFallsBackToSocatWithoutLocalRedirect(t *testing.T) {
	for _, tc := range []struct {
		name           string
		env            []string
		socat          int
		triedLocalDNAT bool
	}{
		{"rule fails", []string{"FAIL_ENGINE=iptables", "FAIL_RULE=" + strings.TrimPrefix(loopbackDNAT("OUTPUT", "-d 10.88.0.5 ", "1455"), "iptables ")}, 1, true},
		// The socat fallback resolves the same address, so it is disabled too.
		{"no address", []string{"BIND_IP="}, 0, false},
		{"IPv6 address", []string{"BIND_IP=fd00::5"}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, err := runGatewayFirewall(t, loopbackForwardingMocks+"setup_observing_network",
				append([]string{"ENCLAVE_LOOPBACK_PORTS=1455", "PROXY_ENABLED=false"}, tc.env...)...,
			)
			if err != nil {
				t.Fatal(err)
			}
			lines := callLines(calls)
			if !slices.Contains(lines, loopbackDNAT("PREROUTING", "", "1455")) {
				t.Errorf("PREROUTING redirect dropped: %q", lines)
			}
			if socatCalls := countSocat(lines); socatCalls != tc.socat {
				t.Errorf("socat fallback ran %d times, want %d: %q", socatCalls, tc.socat, lines)
			}
			if hasLocalLoopbackDNAT(lines) != tc.triedLocalDNAT {
				t.Errorf("OUTPUT DNAT attempted = %v, want %v: %q", !tc.triedLocalDNAT, tc.triedLocalDNAT, lines)
			}
		})
	}
}

// A restricted gateway keeps its pinned loopback forwarding: PREROUTING only.
func TestRestrictedGatewayInstallsNoLocalLoopbackRedirect(t *testing.T) {
	calls, err := runGatewayFirewall(t, loopbackForwardingMocks+"setup_firewall",
		"ENCLAVE_LOOPBACK_PORTS=1455",
		"PROXY_ENABLED=false",
	)
	if err != nil {
		t.Fatal(err)
	}
	if lines := callLines(calls); hasLocalLoopbackDNAT(lines) {
		t.Errorf("restricted gateway redirected local connections: %q", lines)
	}
}

// The gateway takes its network mode from its container environment, set when
// the container is created, never from the bundle on the host: a bundle that
// claims unrestricted must not turn a restricted gateway into an observer.
// Anything but unrestricted is restricted, and the proxy inherits the mode the
// entrypoint decided on.
func TestGatewayModeComesFromEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		envMode  string
		metaMode string
		want     string
	}{
		{"environment unrestricted", "unrestricted", "", "unrestricted"},
		{"environment restricted", "restricted", "", "restricted"},
		{"environment unset", "", "", "restricted"},
		{"unknown value", "observe", "", "restricted"},
		{"padded value", " unrestricted", "", "restricted"},
		{"bundle claims unrestricted, environment unset", "", "unrestricted", "restricted"},
		{"bundle claims unrestricted, environment restricted", "restricted", "unrestricted", "restricted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			if tc.metaMode != "" {
				meta := `{"generation":"g1","mode":"` + tc.metaMode + `"}`
				if err := os.WriteFile(filepath.Join(configDir, "meta.json"), []byte(meta), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			env := []string{"ENCLAVE_GATEWAY_CONFIG_DIR=" + configDir}
			if tc.envMode != "" {
				env = append(env, "ENCLAVE_GATEWAY_NETWORK_MODE="+tc.envMode)
			}
			calls, err := runGatewayFirewall(t, `read_gateway_mode; firewall_mock mode "$GATEWAY_MODE" "$(sh -c 'printf %s "$ENCLAVE_GATEWAY_NETWORK_MODE"')"`, env...)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := strings.Join(calls[len(calls)-1].args, " "), tc.want+" "+tc.want; got != want {
				t.Fatalf("gateway mode, proxy mode = %q, want %q", got, want)
			}
		})
	}
}
