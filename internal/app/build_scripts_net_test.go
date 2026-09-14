// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runNetHelper sources common.sh with fast retry settings and runs the snippet.
// The counter file lets fake commands count their invocations across retries.
func runNetHelper(t *testing.T, snippet string) (string, string, error) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "count")
	if err := os.WriteFile(counter, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	script := `. "$COMMON"
bump() { local n; n=$(<"$COUNTER"); echo $((n + 1)) > "$COUNTER"; }
` + snippet
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"COMMON=" + commonShScript(t),
		"COUNTER=" + counter,
		"ENCLAVE_NET_RETRIES=3",
		"ENCLAVE_NET_RETRY_DELAY_SECONDS=0",
	}
	out, err := cmd.CombinedOutput()
	count, readErr := os.ReadFile(counter)
	if readErr != nil {
		t.Fatalf("read counter: %v", readErr)
	}
	return string(out), strings.TrimSpace(string(count)), err
}

func TestEnclaveRetryRetriesTransientNetworkErrors(t *testing.T) {
	out, count, err := runNetHelper(t, `flaky() { bump; if [ "$(<"$COUNTER")" -lt 3 ]; then echo "curl: (56) Recv failure: Connection reset by peer" >&2; return 56; fi; echo payload; }
enclave_retry flaky -- flaky`)
	if err != nil {
		t.Fatalf("expected success after retries, got %v\n%s", err, out)
	}
	if count != "3" {
		t.Fatalf("expected 3 attempts, got %s\n%s", count, out)
	}
	if !strings.Contains(out, "payload") || !strings.Contains(out, "retrying (attempt 2/3)") {
		t.Fatalf("expected payload and retry notice, got:\n%s", out)
	}
}

func TestEnclaveRetryStopsOnNonRetryableError(t *testing.T) {
	out, count, err := runNetHelper(t, `bad() { bump; echo "curl: (22) The requested URL returned error: 404" >&2; return 22; }
enclave_retry bad -- bad`)
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 22 {
		t.Fatalf("expected the command's exit status 22, got %v\n%s", err, out)
	}
	if count != "1" {
		t.Fatalf("a 404 must not be retried, got %s attempts\n%s", count, out)
	}
	if !strings.Contains(out, "non-retryable") {
		t.Fatalf("expected non-retryable notice, got:\n%s", out)
	}
}

func TestEnclaveRetryGivesUpAfterConfiguredAttempts(t *testing.T) {
	out, count, err := runNetHelper(t, `down() { bump; echo "Temporary failure resolving 'deb.debian.org'" >&2; return 100; }
enclave_retry apt-get-update -- down`)
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if count != "3" {
		t.Fatalf("expected ENCLAVE_NET_RETRIES=3 attempts, got %s\n%s", count, out)
	}
	if !strings.Contains(out, "failed after 3 attempt(s)") {
		t.Fatalf("expected exhaustion notice, got:\n%s", out)
	}
}

func TestEnclaveRetryTimesOutStalledAttempts(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout(1) not available")
	}
	out, _, err := runNetHelper(t, `ENCLAVE_NET_RETRIES=2 ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS=1 enclave_retry slow -- sleep 30`)
	if err == nil {
		t.Fatalf("expected timeout failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "attempt 1 timed out after 1s") || !strings.Contains(out, "attempt 2 timed out") {
		t.Fatalf("expected both attempts to time out, got:\n%s", out)
	}
}

func TestEnclaveRetryRejectsInvalidSettings(t *testing.T) {
	out, _, err := runNetHelper(t, `ENCLAVE_NET_RETRIES=many enclave_retry x -- true`)
	if err == nil {
		t.Fatalf("expected settings validation failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "ENCLAVE_NET_RETRIES must be a non-negative integer") {
		t.Fatalf("expected validation message, got:\n%s", out)
	}
}

func TestEnclaveCurlBuffersStdoutUntilSuccess(t *testing.T) {
	// A fake curl writes a partial body then fails once; the caller must only
	// ever see the complete body from the successful attempt.
	out, count, err := runNetHelper(t, `curl() {
    bump
    local out="" prev=""
    for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
    if [ "$(<"$COUNTER")" -lt 2 ]; then printf 'partial' > "$out"; echo "curl: (18) transfer closed with outstanding read data remaining" >&2; return 18; fi
    printf 'full-body' > "$out"
}
v="$(enclave_curl https://example.invalid/version)"
echo "captured=[$v]"`)
	if err != nil {
		t.Fatalf("expected success, got %v\n%s", err, out)
	}
	if count != "2" {
		t.Fatalf("expected 2 attempts, got %s\n%s", count, out)
	}
	if !strings.Contains(out, "captured=[full-body]") {
		t.Fatalf("expected only the complete body, got:\n%s", out)
	}
}

func TestEnclaveCurlAddsTimeoutsAndFail(t *testing.T) {
	out, _, err := runNetHelper(t, `curl() { printf '%s\n' "$@"; }
enclave_curl -L -o /dev/null https://example.invalid/file`)
	if err != nil {
		t.Fatalf("expected success, got %v\n%s", err, out)
	}
	for _, want := range []string{"--fail", "--connect-timeout", "--speed-limit", "--speed-time", "-L", "https://example.invalid/file"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected curl argument %q, got:\n%s", want, out)
		}
	}
}

func TestEnclaveExportNetHelpersReachesChildBash(t *testing.T) {
	out, _, err := runNetHelper(t, `ENCLAVE_NET_RETRIES=7
enclave_export_net_helpers
bash -c 'declare -F enclave_curl >/dev/null && echo "child sees enclave_curl retries=$ENCLAVE_NET_RETRIES"'`)
	if err != nil {
		t.Fatalf("expected success, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "child sees enclave_curl retries=7") {
		t.Fatalf("expected exported helper and setting in child, got:\n%s", out)
	}
}

func TestTransientNetworkErrorPatterns(t *testing.T) {
	transient := []string{
		"E: Failed to fetch http://deb.debian.org/x  Temporary failure resolving 'deb.debian.org'",
		"go: golang.org/x/vuln@v1.1.4: Get \"https://proxy.golang.org/...\": dial tcp: i/o timeout",
		"curl: (28) Operation timed out after 60001 milliseconds with 0 bytes received",
		"curl: (6) Could not resolve host: github.com",
		"npm ERR! network request to https://registry.npmjs.org/x failed, reason: read ECONNRESET",
		"curl: (22) The requested URL returned error: 503",
	}
	permanent := []string{
		"curl: (22) The requested URL returned error: 404",
		"sha256sum: WARNING: 1 computed checksum did NOT match",
		"go: module github.com/x/y@v9.9.9: reading go.mod: not found",
	}
	for _, tc := range []struct {
		lines []string
		want  bool
	}{{transient, true}, {permanent, false}} {
		for _, line := range tc.lines {
			cmd := exec.Command("bash", "-c", `. "$COMMON"; f="$(mktemp)"; printf '%s\n' "$LINE" > "$f"; enclave_is_transient_network_error "$f"`)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "COMMON=" + commonShScript(t), "LINE=" + line}
			err := cmd.Run()
			if got := err == nil; got != tc.want {
				t.Errorf("transient(%q) = %v, want %v", line, got, tc.want)
			}
		}
	}
}
