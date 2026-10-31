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
)

// Like the firewall harness, source only the shipping functions, not main.
func runGatewayDNSMasq(t *testing.T, invocation string, env ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "gateway.sh")
	if err := os.WriteFile(script, []byte(gatewayScriptFunctions(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `. "$1"
`+invocation, "gateway-dnsmasq-test", script)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"ENCLAVE_NET_LIB=" + filepath.Join(dir, "missing-net.sh"),
	}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestDNSMasqValidationDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		exit   string
	}{
		{name: "quiet success", output: "syntax check OK", exit: "0"},
		{name: "multiline failure", output: "cannot read config: Permission denied\n100% failed", exit: "3"},
		{name: "silent failure", exit: "3"},
		{name: "leading newline", output: "\ncannot read config: Permission denied", exit: "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := filepath.Join(dir, "dnsmasq stub")
			// The fixed production path is reassigned only after sourcing in this test.
			if err := os.WriteFile(stub, []byte(`#!/bin/sh
[ "$1" = --test ] && [ "$2" = '--conf-file=/tmp/config with spaces.conf' ] || exit 99
printf '%s\n' "$STUB_OUTPUT"
printf '%s\n' "$STUB_OUTPUT" >&2
exit "$STUB_EXIT"
`), 0o700); err != nil { // #nosec G306 -- test executable.
				t.Fatal(err)
			}
			out, err := runGatewayDNSMasq(t, `DNSMASQ_BIN="$STUB_BIN"
validate_dnsmasq_conf '/tmp/config with spaces.conf'`,
				"STUB_BIN="+stub, "STUB_OUTPUT="+tc.output, "STUB_EXIT="+tc.exit)
			if tc.exit == "0" {
				if err != nil || out != "" {
					t.Fatalf("successful validation must be quiet: %q, %v", out, err)
				}
				return
			}
			if err == nil || !strings.Contains(out, "dnsmasq config validation failed") {
				t.Fatalf("failure must retain status and generic message: %q, %v", out, err)
			}
			expected := strings.TrimLeft(tc.output+"\n"+tc.output, "\n")
			if expected != "" {
				expected += "\n"
			}
			if out != "[enclave-gateway] dnsmasq config validation failed\n"+expected {
				t.Fatalf("unexpected failure diagnostic: %q", out)
			}
		})
	}
}

func TestDNSMasqStartUsesPrivateExecutable(t *testing.T) {
	for _, logFile := range []string{"", "/var/log/enclave/dnsmasq.log"} {
		t.Run("log="+logFile, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "su-exec"), []byte("#!/bin/sh\nprintf '<%s>\\n' \"$@\"\n"), 0o700); err != nil { // #nosec G306 -- test executable.
				t.Fatal(err)
			}
			out, err := runGatewayDNSMasq(t, `DNSMASQ_LOG_FILE="$TEST_LOG"
start_dnsmasq
wait "$DNSMASQ_PID"`,
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_LOG="+logFile, "DNSMASQ_BIN=/ignored/environment/override")
			if err != nil {
				t.Fatalf("start: %v\n%s", err, out)
			}
			for _, arg := range []string{"dnsmasq:dnsmasq", "/usr/local/libexec/enclave/dnsmasq", "--keep-in-foreground", "--conf-file=/etc/dnsmasq.d/enclave.conf"} {
				if !strings.Contains(out, "<"+arg+">\n") {
					t.Errorf("missing argument %q in %s", arg, out)
				}
			}
			if strings.Contains(out, "<--log-queries>") != (logFile != "") {
				t.Errorf("unexpected query logging arguments: %s", out)
			}
			if logFile != "" && !strings.Contains(out, "<--log-facility="+logFile+">") {
				t.Errorf("missing log path: %s", out)
			}
		})
	}
}

func TestDNSMasqReloadValidationFailureDoesNotInstallPolicy(t *testing.T) {
	out, err := runGatewayDNSMasq(t, `read_bundle_generation() { printf 'generation\n'; }
render_and_validate_bundle() { return 1; }
install_bundle() { printf 'UNEXPECTED install\n'; }
atomic_swap_ipset() { printf 'UNEXPECTED swap\n'; }
setup_firewall() { printf 'UNEXPECTED firewall\n'; }
reload_children() { printf 'UNEXPECTED restart\n'; }
DNSMASQ_CONF=/nonexistent-test-config
ACTIVE_DOMAINS_FILE=/nonexistent-test-domains
reload_gateway`)
	if err == nil || !strings.Contains(out, "Reload validation failed") || strings.Contains(out, "UNEXPECTED") {
		t.Fatalf("invalid staging must return failure before policy changes: %q, %v", out, err)
	}
}

func TestDNSMasqInvalidReloadStillShutsDownFailClosed(t *testing.T) {
	out, err := runGatewayDNSMasq(t, `RELOAD_PENDING=1
reload_gateway() { return 1; }
shutdown_gateway() { printf 'shutdown code=%s reason=%s\n' "$1" "$2"; exit "$1"; }
monitor_loop`)
	if err == nil || !strings.Contains(out, "shutdown code=1 reason=Reload failed; shutting down gateway (fail-closed)") {
		t.Fatalf("expected fail-closed shutdown: %q, %v", out, err)
	}
}
