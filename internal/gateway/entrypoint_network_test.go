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

	"enclave/internal/model"
)

func gatewayNetworkScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../gateway-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	const main = "\nmain \"$@\"\n"
	if !strings.HasSuffix(string(data), main) {
		t.Fatal("gateway entrypoint main invocation changed")
	}
	return strings.TrimSuffix(string(data), main)
}

func TestGatewayFirewallKeepsMediationInUnrestrictedMode(t *testing.T) {
	for _, tc := range []struct {
		name, mode, proxy string
		owner, wantError  bool
	}{
		{"restricted", "restricted", "true", true, false},
		{"unrestricted mediated", "unrestricted", "true", true, false},
		{"unrestricted proxy disabled", "unrestricted", "false", true, true},
		{"unrestricted owner unavailable", "unrestricted", "true", false, true},
		{"invalid mode", "invalid", "true", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "iptables.calls")
			if err := os.WriteFile(filepath.Join(dir, "enclave-gateway-proxy"), []byte("#!/bin/sh\nprintf '%s\\n' \"$TEST_MODE\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			script := gatewayNetworkScript(t) + `
iptables() {
    printf '%s\n' "$*" >> "$TEST_CALLS"
    if [ "$*" = '-m owner -h' ] && [ "$TEST_OWNER" != true ]; then return 1; fi
}
id() { case "$2" in dnsmasq) echo 990;; gateway) echo 991;; *) return 1;; esac; }
setup_loopback_forwarding() { :; }
setup_ide_bridge() { :; }
UNIQUE_RESOLVERS=9.9.9.9
PROXY_ENABLED="$TEST_PROXY"
setup_firewall
`
			command := exec.Command("sh", "-c", script)
			owner := "false"
			if tc.owner {
				owner = "true"
			}
			command.Env = append(os.Environ(), "TEST_CALLS="+calls, "TEST_MODE="+tc.mode, "TEST_PROXY="+tc.proxy, "TEST_OWNER="+owner, "PATH="+dir+":"+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("firewall error = %v, want error %t: %s", err, tc.wantError, output)
			}
			data, readErr := os.ReadFile(calls)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			rules := string(data)
			wideAccept := strings.Contains(rules, "-A OUTPUT -j ACCEPT\n")
			if tc.wantError {
				if wideAccept {
					t.Fatalf("failed setup permitted unrestricted egress: %s", rules)
				}
				return
			}
			for _, required := range []string{
				"-P OUTPUT DROP\n",
				"-t nat -A OUTPUT -p tcp --dport 80 -m owner --uid-owner 991 -j RETURN\n",
				"-t nat -A OUTPUT -p tcp --dport 443 -m owner --uid-owner 991 -j RETURN\n",
				"-t nat -A OUTPUT -p tcp --dport 80 -j REDIRECT --to-ports 8080\n",
				"-t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports 8443\n",
			} {
				if !strings.Contains(rules, required) {
					t.Errorf("missing mediation rule %q: %s", required, rules)
				}
			}
			unrestricted := tc.mode == "unrestricted"
			if wideAccept != unrestricted {
				t.Errorf("arbitrary destinations permitted = %t, want %t: %s", wideAccept, unrestricted, rules)
			}
			ipset := strings.Contains(rules, "-A OUTPUT -m set --match-set enclave_allowed dst -j ACCEPT\n")
			if ipset == unrestricted {
				t.Errorf("destination allowlist rule = %t in %s mode: %s", ipset, tc.mode, rules)
			}
		})
	}
}

func TestGatewayUnrestrictedDNSResolverAndBundleState(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active")
	staging := filepath.Join(dir, "staging")
	rollback := filepath.Join(dir, "rollback")
	for _, path := range []string{active, staging, rollback} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"dnsmasq.conf": "no-resolv\nno-poll\nserver=9.9.9.9\n",
		"domains.txt":  "",
		"denied.txt":   "",
		"meta.json":    `{"mode":"unrestricted"}`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(staging, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rollback, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := gatewayNetworkScript(t) + `
DNSMASQ_CONF="$TEST_DIR/active/dnsmasq.conf"
ACTIVE_DOMAINS_FILE="$TEST_DIR/active/domains.txt"
ACTIVE_DENIED_FILE="$TEST_DIR/active/denied.txt"
ACTIVE_META_FILE="$TEST_DIR/active/meta.json"
install_bundle "$TEST_DIR/staging"
load_resolvers_from_conf "$DNSMASQ_CONF"
[ "$UNIQUE_RESOLVERS" = 9.9.9.9 ] || exit 92
printf '%s\n' wrong > "$ACTIVE_META_FILE"
printf '%s\n' wrong > "$ACTIVE_DENIED_FILE"
restore_bundle "$TEST_DIR/rollback"
`
	command := exec.Command("sh", "-c", script)
	command.Env = append(os.Environ(), "TEST_DIR="+dir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bundle state failed: %v: %s", err, output)
	}
	for name, want := range files {
		data, err := os.ReadFile(filepath.Join(active, name))
		if err != nil || string(data) != want {
			t.Errorf("active %s = %q, %v, want %q", name, data, err, want)
		}
	}
}

func TestGatewayReloadRejectsDestinationModeSwitch(t *testing.T) {
	for _, activeMode := range []string{"restricted", "unrestricted"} {
		t.Run(activeMode, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"bundle", "staging"} {
				if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			files := map[string]string{
				"dnsmasq.conf": "no-resolv\nno-poll\nserver=9.9.9.9\n",
				"domains.txt":  "",
				"denied.txt":   "",
				"meta.json":    "unrestricted",
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(dir, "bundle", name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "active-meta"), []byte(activeMode), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "enclave-gateway-proxy"), []byte("#!/bin/sh\ncat \"$2\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			script := gatewayNetworkScript(t) + `
GATEWAY_CONFIG_DNSMASQ="$TEST_DIR/bundle/dnsmasq.conf"
GATEWAY_CONFIG_DOMAINS="$TEST_DIR/bundle/domains.txt"
GATEWAY_CONFIG_DENIED="$TEST_DIR/bundle/denied.txt"
GATEWAY_CONFIG_META="$TEST_DIR/bundle/meta.json"
ACTIVE_META_FILE="$TEST_DIR/active-meta"
validate_dnsmasq_conf() { :; }
render_and_validate_bundle "$TEST_DIR/staging"
`
			command := exec.Command("sh", "-c", script)
			command.Env = append(os.Environ(), "TEST_DIR="+dir, "PATH="+dir+":"+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if (err == nil) != (activeMode == "unrestricted") {
				t.Fatalf("same-mode reload vs mode switch error = %v: %s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(dir, "active-meta"))
			if err != nil || string(data) != activeMode {
				t.Fatalf("validation changed active destination mode: %q, %v", data, err)
			}
		})
	}
}

func TestGatewayLockdownRemovesUnrestrictedFirewallAllowance(t *testing.T) {
	script := gatewayNetworkScript(t) + `
iptables() { printf 'ipv4 %s\n' "$*"; }
ip6tables() { printf 'ipv6 %s\n' "$*"; }
enforce_fail_closed_lockdown
`
	output, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("lockdown failed: %v: %s", err, output)
	}
	for _, family := range []string{"ipv4", "ipv6"} {
		for _, rule := range []string{"-F OUTPUT", "-P OUTPUT DROP"} {
			if !strings.Contains(string(output), family+" "+rule+"\n") {
				t.Errorf("lockdown missing %s %s: %s", family, rule, output)
			}
		}
	}
	if strings.Contains(string(output), "ACCEPT") {
		t.Fatalf("lockdown retained broad egress allowance: %s", output)
	}
}

func TestGatewayEntrypointInstallsActiveBundleAtReportedPaths(t *testing.T) {
	script := gatewayNetworkScript(t)
	for name, path := range map[string]string{
		"DNSMASQ_CONF":        model.GatewayActiveDNSMasqPath,
		"ACTIVE_DOMAINS_FILE": model.GatewayActiveDomainsPath,
		"ACTIVE_META_FILE":    model.GatewayActiveMetaPath,
	} {
		if !strings.Contains(script, "\n"+name+"=\""+path+"\"\n") {
			t.Errorf("gateway entrypoint %s no longer installs at the path the proxy reports: %s", name, path)
		}
	}
}
