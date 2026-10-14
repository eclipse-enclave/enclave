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

	"enclave/internal/protectedauth"
)

func TestGatewayOAuthSetupRequiresMediationAndPrivateMounts(t *testing.T) {
	for _, tc := range []struct {
		name, adapter, proxy           string
		mounted, capability, wantError bool
		owner                          string
	}{
		{"disabled", "", "false", false, false, false, "1000"},
		{"supported", "claude", "true", true, true, false, "1000"},
		{"no-proxy", "claude", "false", true, true, true, "1000"},
		{"unsupported", "unknown", "true", true, true, true, "1000"},
		{"missing-mounts", "claude", "true", false, true, true, "1000"},
		{"no-capability", "claude", "true", true, false, true, "1000"},
		{"root-workload", "claude", "true", true, true, true, "0"},
		{"proxy-UID-collision", "claude", "true", true, true, true, "100"},
		{"DNS-UID-collision", "claude", "true", true, true, true, "101"},
		{"missing-UID", "claude", "true", true, true, true, ""},
		{"invalid-UID", "claude", "true", true, true, true, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.mounted {
				for _, name := range []string{"grant", "runtime", "view"} {
					if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(root, "identity.lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(gatewayNetworkScript(t), "/oauth/", root+"/") + `
setcap() { printf '%s\n' "$*" > "$TEST_CAPS"; [ "$TEST_CAPABILITY" = true ]; }
stat() { printf '%s\n' "$TEST_OWNER"; }
id() { case "$2" in gateway) echo 100 ;; dnsmasq) echo 101 ;; *) return 1 ;; esac; }
PROXY_ENABLED="$TEST_PROXY"
prepare_oauth_broker
`
			capability := "false"
			if tc.capability {
				capability = "true"
			}
			caps := filepath.Join(root, "caps")
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "ENCLAVE_OAUTH_GITHUB=", "ENCLAVE_OAUTH_GITHUB_IDENTITY=", "ENCLAVE_OAUTH_BROKER="+tc.adapter, "TEST_PROXY="+tc.proxy, "TEST_CAPABILITY="+capability, "TEST_CAPS="+caps, "TEST_OWNER="+tc.owner)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("setup error=%v, want error%t: %s", err, tc.wantError, output)
			}
			data, err := os.ReadFile(caps)
			if tc.name == "supported" && (err != nil || string(data) != "cap_dac_override,cap_chown=ep /usr/local/bin/enclave-gateway-proxy\n") {
				t.Fatalf("proxy file capabilities=%q, %v", data, err)
			}
			if tc.adapter == "" && !os.IsNotExist(err) {
				t.Fatal("ordinary gateway gained broker capabilities")
			}
			if tc.wantError && tc.name != "no-capability" && !os.IsNotExist(err) {
				t.Fatal("rejected gateway enabled broker capabilities")
			}
		})
	}
}

// The host names the selected tool; the gateway must start for every tool
// that can run protected.
func TestGatewayOAuthSetupAcceptsEveryProtectedTool(t *testing.T) {
	for _, tool := range protectedauth.Tools() {
		t.Run(tool.Name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"grant", "runtime", "view"} {
				if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "identity.lock"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(gatewayNetworkScript(t), "/oauth/", root+"/") + `
setcap() { :; }
stat() { echo 1000; }
id() { case "$2" in gateway) echo 100 ;; dnsmasq) echo 101 ;; *) return 1 ;; esac; }
PROXY_ENABLED=true
prepare_oauth_broker
`
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "ENCLAVE_OAUTH_GITHUB=", "ENCLAVE_OAUTH_GITHUB_IDENTITY=", "ENCLAVE_OAUTH_BROKER="+tool.Name)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("gateway refused protected %s: %v: %s", tool.Name, err, output)
			}
		})
	}
}

func TestGatewayGitHubSetupRequiresPrivateFeatureMounts(t *testing.T) {
	for _, tc := range []struct {
		name, mode, identity, owner string
		mounted, wantError          bool
	}{
		{"enabled", "1", "github", "1000", true, false},
		{"missing mounts", "1", "github", "1000", false, true},
		{"foreign view owner", "1", "github", "1001", true, true},
		{"missing identity", "1", "", "1000", true, true},
		{"orphan identity", "", "github", "1000", true, true},
		{"unsupported mode", "2", "github", "1000", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			roots := []string{root}
			if tc.mounted {
				roots = append(roots, filepath.Join(root, "features", "github-cli"))
			}
			for _, mountRoot := range roots {
				for _, dir := range []string{"grant", "runtime", "view"} {
					if err := os.MkdirAll(filepath.Join(mountRoot, dir), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(mountRoot, "identity.lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(gatewayNetworkScript(t), "/oauth/", root+"/") + `
setcap() { echo enabled > "$TEST_CAPS"; }
stat() { case "$3" in */features/github-cli/view) echo "$TEST_GITHUB_OWNER" ;; *) echo 1000 ;; esac; }
id() { case "$2" in gateway) echo 100 ;; dnsmasq) echo 101 ;; *) return 1 ;; esac; }
PROXY_ENABLED=true
prepare_oauth_broker
`
			caps := filepath.Join(root, "caps")
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "ENCLAVE_OAUTH_BROKER=claude", "ENCLAVE_OAUTH_GITHUB="+tc.mode,
				"ENCLAVE_OAUTH_GITHUB_IDENTITY="+tc.identity, "TEST_GITHUB_OWNER="+tc.owner, "TEST_CAPS="+caps)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("GitHub mount setup error=%v wantError=%v: %s", err, tc.wantError, output)
			}
			_, err = os.Stat(caps)
			if tc.wantError && !os.IsNotExist(err) {
				t.Fatal("invalid feature mounts received broker capabilities")
			}
		})
	}
}

func TestProtectedGatewayDisablesRawDNSAudit(t *testing.T) {
	for _, adapter := range []string{"", "claude"} {
		t.Run("adapter-"+adapter, func(t *testing.T) {
			root := t.TempDir()
			script := gatewayNetworkScript(t) + `
mkdir() { :; }
touch() { :; }
chmod() { :; }
chown() { :; }
NETWORK_LOG_FILE="$TEST_DIR/network.log"
PROXY_LOG_FILE="$TEST_DIR/proxy.log"
DNS_AUDIT_LOG_FILE="$TEST_DIR/dns-audit.log"
init_log_files
if [ -n "$ENCLAVE_OAUTH_BROKER" ]; then
 [ -z "$DNSMASQ_LOG_FILE" ]
else
 [ "$DNSMASQ_LOG_FILE" = /var/log/enclave/dnsmasq.log ]
fi
`
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "ENCLAVE_OAUTH_BROKER="+adapter, "TEST_DIR="+root)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("DNS audit setup=%v: %s", err, output)
			}
		})
	}
}
