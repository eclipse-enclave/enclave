// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/docker"
	"enclave/internal/peergroup"
)

const (
	testHub      = "172.18.0.14"
	testBridge   = "eth0"
	workerJSON   = `{"schemaVersion":"1","generation":3,"active":true,"group":"review","member":"worker-1","hubAddress":"172.18.0.14","hubPort":41820,"adapterPort":41821,"dnsName":"a2a.enclave"}`
	orchJSON     = `{"schemaVersion":"1","generation":4,"active":true,"group":"review","member":"lead","hubAddress":"172.18.0.14","hubPort":41820,"adapterPort":0,"dnsName":"a2a.enclave"}`
	clearedJSON  = `{"schemaVersion":"1","generation":5,"active":false,"group":"review","member":"worker-1","hubAddress":"172.18.0.14","hubPort":41820,"adapterPort":41821,"dnsName":"a2a.enclave"}`
	publishedEnv = "ENCLAVE_GATEWAY_PUBLISHED_PORTS=3000/tcp,5353/udp"
	bystander    = "172.18.0.9"
)

// overlayPrelude points the sourced gateway at a test overlay and a fixed route.
const overlayPrelude = `A2A_OVERLAY_FILE="$OVERLAY_FILE"
enclave_route_interface_ipv4() { [ "$1" = 172.18.0.14 ] && printf 'eth0\n'; }
`

func writeOverlayFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "overlay.json")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func runOverlayFirewall(t *testing.T, overlay string, invocation string, env ...string) ([]firewallCall, string, error) {
	t.Helper()
	env = append(env, "OVERLAY_FILE="+writeOverlayFile(t, overlay))
	return runGatewayScript(t, overlayPrelude+invocation, env...)
}

func hubPacket(port string) filterPacket {
	return filterPacket{inIface: testBridge, source: testHub, state: "NEW", protocol: "tcp", port: port}
}

func toHubPacket(port string) filterPacket {
	return filterPacket{outIface: testBridge, dest: testHub, state: "NEW", protocol: "tcp", port: port}
}

func callsMentioning(calls []firewallCall, engine string, text string) []string {
	var found []string
	for _, call := range calls {
		joined := strings.Join(call.args, " ")
		if call.engine == engine && strings.Contains(joined, text) {
			found = append(found, joined)
		}
	}
	return found
}

func TestA2AOverlayWorkerRules(t *testing.T) {
	calls, out, err := runOverlayFirewall(t, workerJSON,
		"load_a2a_overlay && setup_firewall && setup_kernel_network && a2a_overlay_succeeded", publishedEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "A2A overlay applied (generation=3)") {
		t.Fatalf("missing applied marker: %q", out)
	}

	want := map[string][]string{
		"INPUT":      {"-A INPUT -i eth0 -s 172.18.0.14 -p tcp --dport 41821 -j ACCEPT", "-A INPUT -s 172.18.0.14 -j DROP"},
		"OUTPUT":     {"-A OUTPUT -d 172.18.0.14 -p tcp --dport 41820 -j ACCEPT"},
		"nat OUTPUT": {"-t nat -A OUTPUT -d 172.18.0.14 -j RETURN"},
	}
	got := map[string][]string{}
	for _, rule := range callsMentioning(calls, "iptables", testHub) {
		switch {
		case strings.HasPrefix(rule, "-t nat"):
			got["nat OUTPUT"] = append(got["nat OUTPUT"], rule)
		case strings.HasPrefix(rule, "-A INPUT"):
			got["INPUT"] = append(got["INPUT"], rule)
		default:
			got["OUTPUT"] = append(got["OUTPUT"], rule)
		}
	}
	for chain, rules := range want {
		if strings.Join(got[chain], "\n") != strings.Join(rules, "\n") {
			t.Errorf("%s rules = %q, want %q", chain, got[chain], rules)
		}
	}
	if v6 := callsMentioning(calls, "ip6tables", testHub); len(v6) != 0 {
		t.Errorf("IPv6 must get no A2A exception: %q", v6)
	}

	for _, tc := range []struct {
		name   string
		chain  string
		packet filterPacket
		want   string
	}{
		{"hub to adapter", "INPUT", hubPacket("41821"), "ACCEPT"},
		{"hub to other port", "INPUT", hubPacket("8080"), "DROP"},
		{"other source to adapter", "INPUT", filterPacket{inIface: testBridge, source: bystander, state: "NEW", protocol: "tcp", port: "41821"}, "DROP"},
		{"hub on other interface", "INPUT", filterPacket{inIface: "eth1", source: testHub, state: "NEW", protocol: "tcp", port: "41821"}, "DROP"},
		{"hub udp", "INPUT", filterPacket{inIface: testBridge, source: testHub, state: "NEW", protocol: "udp", port: "41821"}, "DROP"},
		{"hub to published tcp", "INPUT", hubPacket("3000"), "DROP"},
		{"hub to published udp", "INPUT", filterPacket{inIface: testBridge, source: testHub, state: "NEW", protocol: "udp", port: "5353"}, "DROP"},
		{"hub reply", "INPUT", filterPacket{inIface: testBridge, source: testHub, state: "ESTABLISHED", protocol: "tcp", port: "50000"}, "ACCEPT"},
		{"bystander to published tcp", "INPUT", filterPacket{inIface: testBridge, source: bystander, state: "NEW", protocol: "tcp", port: "3000"}, "ACCEPT"},
		{"bystander to published udp", "INPUT", filterPacket{inIface: testBridge, source: bystander, state: "NEW", protocol: "udp", port: "5353"}, "ACCEPT"},
		{"to hub port", "OUTPUT", toHubPacket("41820"), "ACCEPT"},
		{"to hub other port", "OUTPUT", toHubPacket("22"), "DROP"},
		{"hub port elsewhere", "OUTPUT", filterPacket{outIface: testBridge, dest: bystander, state: "NEW", protocol: "tcp", port: "41820"}, "DROP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := chainVerdict(t, calls, "iptables", "filter", tc.chain, tc.packet); got != tc.want {
				t.Fatalf("verdict = %s, want %s", got, tc.want)
			}
		})
	}

	// The exemption must precede the transparent proxy redirect.
	if got := chainVerdict(t, calls, "iptables", "nat", "OUTPUT", toHubPacket("80")); got != "RETURN" {
		t.Fatalf("nat verdict for hub on port 80 = %s, want RETURN", got)
	}
	if got := chainVerdict(t, calls, "iptables", "nat", "OUTPUT", filterPacket{dest: "203.0.113.1", protocol: "tcp", port: "80"}); got != "REDIRECT" {
		t.Fatalf("nat verdict for other traffic = %s, want REDIRECT", got)
	}
}

func TestA2AOverlayOrchestratorHasNoInputRule(t *testing.T) {
	calls, out, err := runOverlayFirewall(t, orchJSON, "load_a2a_overlay && setup_firewall && a2a_overlay_succeeded", publishedEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "A2A overlay applied (generation=4)") {
		t.Fatalf("missing applied marker: %q", out)
	}
	if rules := callsMentioning(calls, "iptables", "-A INPUT -s "+testHub); strings.Join(rules, "\n") != "-A INPUT -s 172.18.0.14 -j DROP" {
		t.Fatalf("orchestrator hub INPUT rules = %q", rules)
	}
	for _, tc := range []struct {
		name   string
		packet filterPacket
		want   string
	}{
		{"hub to adapter port", hubPacket("41821"), "DROP"},
		{"hub to published", hubPacket("3000"), "DROP"},
		{"hub reply", filterPacket{inIface: testBridge, source: testHub, state: "ESTABLISHED", protocol: "tcp", port: "50000"}, "ACCEPT"},
		{"bystander to published", filterPacket{inIface: testBridge, source: bystander, state: "NEW", protocol: "tcp", port: "3000"}, "ACCEPT"},
	} {
		if got := chainVerdict(t, calls, "iptables", "filter", "INPUT", tc.packet); got != tc.want {
			t.Errorf("%s: verdict = %s, want %s", tc.name, got, tc.want)
		}
	}
	if got := chainVerdict(t, calls, "iptables", "filter", "OUTPUT", toHubPacket("41820")); got != "ACCEPT" {
		t.Fatalf("orchestrator cannot reach hub: %s", got)
	}
}

func TestA2AOverlayAbsentChangesNothing(t *testing.T) {
	withoutFile, out, err := runOverlayFirewall(t, "", "load_a2a_overlay && setup_firewall && setup_kernel_network && a2a_overlay_succeeded", publishedEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "A2A overlay") {
		t.Fatalf("missing overlay logged a marker: %q", out)
	}
	baseline, err := runGatewayFirewall(t, "setup_firewall && setup_kernel_network", publishedEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(callsMentioning(baseline, "iptables", "-A INPUT -p tcp --dport 3000 -j ACCEPT")) != 1 {
		t.Fatal("baseline lacks the published port accept")
	}
	cleared, out, err := runOverlayFirewall(t, clearedJSON, "load_a2a_overlay && setup_firewall && setup_kernel_network && a2a_overlay_succeeded", publishedEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for name, calls := range map[string][]firewallCall{"absent": withoutFile, "cleared": cleared} {
		if len(calls) != len(baseline) {
			t.Fatalf("%s overlay changed the rule count: %d vs %d", name, len(calls), len(baseline))
		}
		for i := range baseline {
			if calls[i].engine != baseline[i].engine || strings.Join(calls[i].args, " ") != strings.Join(baseline[i].args, " ") {
				t.Fatalf("%s overlay changed rule %d: %v vs %v", name, i, calls[i], baseline[i])
			}
		}
	}
}

func TestA2AOverlayClearedRemovesRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(path, []byte(workerJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cleared := filepath.Join(dir, "cleared.json")
	if err := os.WriteFile(cleared, []byte(clearedJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	calls, out, err := runGatewayScript(t, overlayPrelude+`load_a2a_overlay && setup_firewall && a2a_overlay_succeeded || exit 1
mv "$CLEARED" "$OVERLAY_FILE"
load_a2a_overlay && setup_firewall && a2a_overlay_succeeded`,
		"OVERLAY_FILE="+path, "CLEARED="+cleared)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "A2A overlay applied (generation=3)\n") || !strings.Contains(out, "A2A overlay cleared (generation=5)\n") {
		t.Fatalf("missing markers: %q", out)
	}
	if got := chainVerdict(t, calls, "iptables", "filter", "INPUT", hubPacket("41821")); got != "DROP" {
		t.Fatalf("cleared overlay left inbound rule: %s", got)
	}
	if got := chainVerdict(t, calls, "iptables", "filter", "OUTPUT", toHubPacket("41820")); got != "DROP" {
		t.Fatalf("cleared overlay left outbound rule: %s", got)
	}
	if got := chainVerdict(t, calls, "iptables", "nat", "OUTPUT", toHubPacket("80")); got != "REDIRECT" {
		t.Fatalf("cleared overlay left nat exemption: %s", got)
	}
}

func TestA2AOverlayValidation(t *testing.T) {
	replace := strings.NewReplacer
	for _, tc := range []struct {
		name       string
		content    string
		generation string
	}{
		{"not json", "garbage", "unknown"},
		{"empty", " ", "unknown"},
		{"missing generation", strings.Replace(workerJSON, `"generation":3,`, "", 1), "unknown"},
		{"negative generation", strings.Replace(workerJSON, `"generation":3`, `"generation":-3`, 1), "unknown"},
		{"duplicate generation", strings.Replace(workerJSON, `"active":true`, `"active":true,"generation":9`, 1), "unknown"},
		{"escaped value", strings.Replace(workerJSON, `"review"`, `"re\"view"`, 1), "unknown"},
		{"schema version", strings.Replace(workerJSON, `"schemaVersion":"1"`, `"schemaVersion":"2"`, 1), "3"},
		{"active string", strings.Replace(workerJSON, `"active":true`, `"active":"yes"`, 1), "3"},
		{"hub address", replace(testHub, "172.18.0.256").Replace(workerJSON), "3"},
		{"hub address loopback", replace(testHub, "127.0.0.1").Replace(workerJSON), "3"},
		{"hub address injection", replace(testHub, "172.18.0.14 -j ACCEPT").Replace(workerJSON), "3"},
		{"hub port zero", strings.Replace(workerJSON, `"hubPort":41820`, `"hubPort":0`, 1), "3"},
		{"adapter port range", strings.Replace(workerJSON, `"adapterPort":41821`, `"adapterPort":70000`, 1), "3"},
		{"dns name", strings.Replace(workerJSON, `"a2a.enclave"`, `"a2a.enclave,1.2.3.4"`, 1), "3"},
		{"dns name newline", strings.Replace(workerJSON, `"a2a.enclave"`, `"a2a.enclave\nserver=/#/1.1.1.1"`, 1), "unknown"},
		{"oversized", workerJSON[:len(workerJSON)-1] + `,"pad":"` + strings.Repeat("x", 5000) + `"}`, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, out, err := runOverlayFirewall(t, tc.content, "load_a2a_overlay || exit 1; setup_firewall")
			if err == nil {
				t.Fatalf("invalid overlay accepted: %s", out)
			}
			if want := "A2A overlay validation failed (generation=" + tc.generation + ")"; strings.Count(out, "A2A overlay") != 1 || !strings.Contains(out, want) {
				t.Fatalf("output %q, want exactly %q", out, want)
			}
			if len(calls) != 0 {
				t.Fatalf("invalid overlay touched the firewall: %v", calls)
			}
		})
	}
	if _, out, err := runOverlayFirewall(t, replace(testHub, "bogus").Replace(clearedJSON), "load_a2a_overlay"); err != nil {
		t.Fatalf("inactive overlay must not need hub fields: %v %s", err, out)
	}
}

func TestA2AOverlayRouteFailureIsApplyFailure(t *testing.T) {
	_, out, err := runOverlayFirewall(t, workerJSON, `enclave_route_interface_ipv4() { return 1; }
load_a2a_overlay || exit 2
setup_firewall || { a2a_overlay_apply_failed; exit 1; }`)
	if err == nil || !strings.Contains(out, "A2A overlay apply failed (generation=3)") {
		t.Fatalf("expected apply failure: %q, %v", out, err)
	}
}

func TestA2AOverlayDNSRecord(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle")
	stage := filepath.Join(dir, "stage")
	for _, d := range []string{bundle, stage} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"dnsmasq.conf": "no-resolv\nserver=/example.com/1.1.1.1\naddress=/#/\n",
		"domains.txt":  "example.com\n",
		"meta.json":    `{"generation":"g1"}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		overlay string
		record  bool
	}{{workerJSON, true}, {clearedJSON, false}, {"", false}} {
		_, out, err := runOverlayFirewall(t, tc.overlay, `DNSMASQ_BIN=true
GATEWAY_CONFIG_DIR="$BUNDLE"
GATEWAY_CONFIG_DNSMASQ="$BUNDLE/dnsmasq.conf"
GATEWAY_CONFIG_DOMAINS="$BUNDLE/domains.txt"
GATEWAY_CONFIG_META="$BUNDLE/meta.json"
load_a2a_overlay && render_and_validate_bundle "$STAGE"`, "BUNDLE="+bundle, "STAGE="+stage)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		conf, err := os.ReadFile(filepath.Join(stage, "dnsmasq.conf"))
		if err != nil {
			t.Fatal(err)
		}
		hasRecord := strings.Contains(string(conf), "\nhost-record=a2a.enclave,172.18.0.14\n")
		if hasRecord != tc.record || strings.Count(string(conf), "host-record=") > 1 {
			t.Fatalf("overlay %q rendered:\n%s", tc.overlay, conf)
		}
		if !strings.HasPrefix(string(conf), files["dnsmasq.conf"]) {
			t.Fatalf("bundle config was not preserved:\n%s", conf)
		}
	}
}

// TestA2AOverlayDNSRecordWithRealDNSMasq checks that dnsmasq accepts the record
// next to the catch-all blackhole; skipped where dnsmasq is not installed.
func TestA2AOverlayDNSRecordWithRealDNSMasq(t *testing.T) {
	bin, err := exec.LookPath("dnsmasq")
	if err != nil {
		t.Skip("dnsmasq not installed")
	}
	conf := filepath.Join(t.TempDir(), "dnsmasq.conf")
	if err := os.WriteFile(conf, []byte("no-resolv\naddress=/#/\n\nhost-record=a2a.enclave,172.18.0.14\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "--test", "--conf-file="+conf).CombinedOutput(); err != nil {
		t.Fatalf("dnsmasq rejected the A2A record: %v\n%s", err, out)
	}
}

func reloadInvocation(failAt string) string {
	stubs := `DNSMASQ_CONF=/nonexistent-test-config
ACTIVE_DOMAINS_FILE=/nonexistent-test-domains
read_bundle_generation() { printf 'g7\n'; }
render_and_validate_bundle() { [ "$FAIL_AT" != render ]; }
install_bundle() { [ "$FAIL_AT" != install ]; }
restore_bundle() { :; }
atomic_swap_ipset() { [ "$FAIL_AT" != ipset ]; }
reload_children() { [ "$FAIL_AT" != children ]; }
`
	if failAt == "firewall" {
		stubs += "setup_firewall() { return 1; }\n"
	}
	return stubs + "reload_gateway"
}

func TestA2AOverlayReloadMarkers(t *testing.T) {
	for _, tc := range []struct {
		failAt string
		want   []string
	}{
		{"", []string{"A2A overlay applied (generation=3)", "Reload completed (generation=g7)"}},
		{"render", []string{"A2A overlay apply failed (generation=3)", "Reload validation failed (generation=g7)"}},
		{"install", []string{"A2A overlay apply failed (generation=3)", "Reload apply failed (generation=g7)"}},
		{"ipset", []string{"A2A overlay apply failed (generation=3)", "Reload ipset swap failed (generation=g7)"}},
		{"firewall", []string{"A2A overlay apply failed (generation=3)", "Reload apply failed (generation=g7)"}},
		{"children", []string{"A2A overlay apply failed (generation=3)", "Reload process restart failed (generation=g7)"}},
	} {
		t.Run("fail="+tc.failAt, func(t *testing.T) {
			_, out, err := runOverlayFirewall(t, workerJSON, reloadInvocation(tc.failAt), "FAIL_AT="+tc.failAt)
			if (err == nil) != (tc.failAt == "") {
				t.Fatalf("reload result %v for failure at %q\n%s", err, tc.failAt, out)
			}
			assertMarkerOrder(t, out, tc.want)
			if strings.Count(out, "A2A overlay") != 1 {
				t.Fatalf("want exactly one overlay marker: %q", out)
			}
		})
	}
}

func TestA2AOverlayReloadValidationFailureInstallsNothing(t *testing.T) {
	_, out, err := runOverlayFirewall(t, "{", `read_bundle_generation() { printf 'g7\n'; }
render_and_validate_bundle() { printf 'UNEXPECTED render\n'; }
setup_firewall() { printf 'UNEXPECTED firewall\n'; }
reload_gateway`)
	if err == nil || strings.Contains(out, "UNEXPECTED") {
		t.Fatalf("invalid overlay must fail before staging: %q, %v", out, err)
	}
	assertMarkerOrder(t, out, []string{"A2A overlay validation failed (generation=unknown)", "Reload validation failed (generation=g7)"})
}

func TestA2AOverlayReloadWithoutOverlayLogsNoMarker(t *testing.T) {
	_, out, err := runOverlayFirewall(t, "", reloadInvocation(""), "FAIL_AT=")
	if err != nil || strings.Contains(out, "A2A overlay") || !strings.Contains(out, "Reload completed (generation=g7)") {
		t.Fatalf("reload without overlay: %q, %v", out, err)
	}
}

func assertMarkerOrder(t *testing.T, out string, markers []string) {
	t.Helper()
	pos := -1
	for _, marker := range markers {
		i := strings.Index(out, "[enclave-gateway] "+marker+"\n")
		if i < 0 || i < pos {
			t.Fatalf("markers %q out of order or missing in %q", markers, out)
		}
		pos = i
	}
}

func TestRouteInterfaceIPv4(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   string
		ok     bool
	}{
		{"on link", "172.18.0.14 dev eth0 src 172.18.0.3 uid 0 \n    cache \n", "eth0", true},
		{"via router", "10.0.0.1 via 172.18.0.1 dev eth1 src 172.18.0.3 \n", "eth1", true},
		{"no route", "", "", false},
		{"hostile name", "172.18.0.14 dev eth0;rm src 172.18.0.3\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := "#!/bin/sh\n[ \"$1 $2 $3 $4\" = '-4 route get 172.18.0.14' ] || exit 2\nprintf '%s' \"$IP_OUTPUT\"\n"
			if err := os.WriteFile(filepath.Join(dir, "ip"), []byte(stub), 0o700); err != nil { // #nosec G306 -- test executable.
				t.Fatal(err)
			}
			netLib, err := filepath.Abs(filepath.Join("..", "..", "runtime-assets", "net.sh"))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", `. "$0"; enclave_route_interface_ipv4 172.18.0.14`, netLib)
			cmd.Env = []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "IP_OUTPUT=" + tc.output}
			out, err := cmd.Output()
			if got := strings.TrimSpace(string(out)); got != tc.want || (err == nil) != tc.ok {
				t.Fatalf("got %q (err %v), want %q ok=%v", got, err, tc.want, tc.ok)
			}
		})
	}
}

func TestOverlayLogOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		logs string
		done bool
		err  bool
	}{
		{"nothing yet", "[enclave-gateway] Reload requested (generation=g1)\n", false, false},
		{"applied", "[enclave-gateway] A2A overlay applied (generation=3)\n", true, false},
		{"cleared", "[enclave-gateway] A2A overlay cleared (generation=3)\n", true, false},
		{"other generation", "[enclave-gateway] A2A overlay applied (generation=13)\n[enclave-gateway] A2A overlay applied (generation=2)\n", false, false},
		{"validation failed", "[enclave-gateway] A2A overlay validation failed (generation=3)\n", true, true},
		{"apply failed", "[enclave-gateway] A2A overlay apply failed (generation=3)\n", true, true},
		{"unparseable", "[enclave-gateway] A2A overlay validation failed (generation=unknown)\n", true, true},
		{"other generation failed", "[enclave-gateway] A2A overlay apply failed (generation=2)\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done, err := OverlayLogOutcome(tc.logs, 3)
			if done != tc.done || (err != nil) != tc.err {
				t.Fatalf("done=%v err=%v, want done=%v err=%v", done, err, tc.done, tc.err)
			}
		})
	}
}

func TestOverlayMarkerFormatsMatchScript(t *testing.T) {
	script := gatewayScriptFunctions(t)
	if !strings.Contains(script, `log "A2A overlay $1 (generation=$A2A_OVERLAY_GENERATION)"`) {
		t.Fatal("gateway overlay marker format changed")
	}
	for _, marker := range []string{peergroup.OverlayAppliedMarker, peergroup.OverlayClearedMarker, peergroup.OverlayValidationFailedMarker, peergroup.OverlayApplyFailedMarker} {
		verb := strings.TrimSuffix(strings.TrimPrefix(marker, "A2A overlay "), " (generation=%s)")
		if verb == marker || !strings.Contains(script, `a2a_overlay_marker "`+verb+`"`) {
			t.Fatalf("script does not log %q", marker)
		}
	}
}

func stubOverlayEngine(t *testing.T, logs []string, running bool) *[]string {
	t.Helper()
	signals := &[]string{}
	calls := 0
	prevSignal, prevLogs, prevInspect := overlayContainerSignal, overlayContainerLogsSince, overlayContainerInspect
	prevTimeout, prevPoll := overlayReloadTimeout, overlayReloadPollInterval
	t.Cleanup(func() {
		overlayContainerSignal, overlayContainerLogsSince, overlayContainerInspect = prevSignal, prevLogs, prevInspect
		overlayReloadTimeout, overlayReloadPollInterval = prevTimeout, prevPoll
	})
	overlayReloadTimeout = 200 * time.Millisecond
	overlayReloadPollInterval = time.Millisecond
	overlayContainerSignal = func(_ context.Context, id string, signal string) error {
		*signals = append(*signals, id+" "+signal)
		return nil
	}
	overlayContainerLogsSince = func(context.Context, string, time.Time) (string, error) {
		i := min(calls, len(logs)-1)
		calls++
		return logs[i], nil
	}
	overlayContainerInspect = func(context.Context, string) (docker.InspectResponse, error) {
		return docker.InspectResponse{State: &docker.ContainerState{Running: running}}, nil
	}
	return signals
}

func TestReloadOverlay(t *testing.T) {
	signals := stubOverlayEngine(t, []string{"", "A2A overlay applied (generation=3)\n"}, true)
	if err := ReloadOverlay(context.Background(), "gw", 3); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*signals, ",") != "gw SIGHUP" {
		t.Fatalf("signals = %v", *signals)
	}

	stubOverlayEngine(t, []string{"A2A overlay apply failed (generation=3)\n"}, false)
	if err := ReloadOverlay(context.Background(), "gw", 3); err == nil || !strings.Contains(err.Error(), "apply failed") {
		t.Fatalf("failure marker not reported: %v", err)
	}

	stubOverlayEngine(t, []string{""}, false)
	if err := ReloadOverlay(context.Background(), "gw", 3); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("gateway exit not reported: %v", err)
	}

	stubOverlayEngine(t, []string{""}, true)
	if err := ReloadOverlay(context.Background(), "gw", 3); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout not reported: %v", err)
	}

	stubOverlayEngine(t, []string{""}, true)
	overlayContainerSignal = func(context.Context, string, string) error { return errors.New("boom") }
	if err := ReloadOverlay(context.Background(), "gw", 3); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("signal error not reported: %v", err)
	}
}

func TestA2AOverlayMount(t *testing.T) {
	if mounts := appendA2AOverlayMount(nil, ""); len(mounts) != 0 {
		t.Fatalf("non-A2A session got an overlay mount: %v", mounts)
	}
	dir := t.TempDir()
	mounts := appendA2AOverlayMount(nil, dir)
	if len(mounts) != 1 || mounts[0].Source != dir || mounts[0].Target != peergroup.GatewayOverlayDir || !mounts[0].ReadOnly || mounts[0].Type != docker.MountTypeBind {
		t.Fatalf("overlay mount = %+v", mounts)
	}

	base := StartConfig{NetworkName: "net"}
	if err := validateStartConfig(base); err != nil {
		t.Fatal(err)
	}
	withDir := base
	withDir.A2AOverlayDir = dir
	if err := validateStartConfig(withDir); err != nil {
		t.Fatal(err)
	}
	missing := base
	missing.A2AOverlayDir = filepath.Join(dir, "missing")
	if err := validateStartConfig(missing); err == nil {
		t.Fatal("missing overlay dir accepted")
	}
	file := base
	file.A2AOverlayDir = writeOverlayFile(t, workerJSON)
	if err := validateStartConfig(file); err == nil {
		t.Fatal("overlay file accepted as directory")
	}
}
