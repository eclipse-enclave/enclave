// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntrypointGatewayCAExportsCombinedCABundle(t *testing.T) {
	t.Parallel()

	systemBundlePath := "/etc/ssl/certs/ca-certificates.crt"
	systemBundle, err := os.ReadFile(systemBundlePath)
	if err != nil {
		t.Skipf("system CA bundle unavailable: %v", err)
	}

	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	tmpDir := filepath.Join(home, "tmp")
	fakeBin := filepath.Join(home, "bin")
	for _, dir := range []string{projectDir, tmpDir, fakeBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	gatewayCA := filepath.Join(home, "gateway.crt")
	gatewayCert := "-----BEGIN CERTIFICATE-----\nenclave-gateway-test\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(gatewayCA, []byte(gatewayCert), 0o644); err != nil {
		t.Fatalf("write gateway CA: %v", err)
	}

	updateCAPath := filepath.Join(fakeBin, "update-ca-certificates")
	updateCAScript := "#!/bin/sh\nprintf called > \"$HOME/update-ca-certificates.called\"\n"
	if err := os.WriteFile(updateCAPath, []byte(updateCAScript), 0o755); err != nil {
		t.Fatalf("write update-ca-certificates shim: %v", err)
	}

	entrypointPath := filepath.Join("..", "..", "entrypoint.sh")
	captureEnv := `{
printf 'SSL_CERT_FILE=%s\n' "${SSL_CERT_FILE:-}"
printf 'REQUESTS_CA_BUNDLE=%s\n' "${REQUESTS_CA_BUNDLE:-}"
printf 'NODE_EXTRA_CA_CERTS=%s\n' "${NODE_EXTRA_CA_CERTS:-}"
} > "$HOME/gateway-ca-env.out"`
	cmd := exec.Command("bash", entrypointPath, "bash", "-lc", captureEnv)
	cmd.Env = []string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TMPDIR=" + tmpDir,
		"TOOL=pi",
		"ENCLAVE_GATEWAY_CA_CERT_PATH=" + gatewayCA,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, string(out))
	}
	if _, err := os.Stat(filepath.Join(home, "update-ca-certificates.called")); err != nil {
		t.Fatalf("expected update-ca-certificates shim to run: %v", err)
	}

	env := readEntrypointEnvFile(t, filepath.Join(home, "gateway-ca-env.out"))
	sslCertFile := env["SSL_CERT_FILE"]
	if sslCertFile == "" {
		t.Fatal("expected SSL_CERT_FILE to be set")
	}
	if sslCertFile == gatewayCA {
		t.Fatalf("SSL_CERT_FILE points at gateway-only cert %q", gatewayCA)
	}
	if env["REQUESTS_CA_BUNDLE"] != sslCertFile {
		t.Fatalf("REQUESTS_CA_BUNDLE = %q, want %q", env["REQUESTS_CA_BUNDLE"], sslCertFile)
	}
	if env["NODE_EXTRA_CA_CERTS"] != gatewayCA {
		t.Fatalf("NODE_EXTRA_CA_CERTS = %q, want %q", env["NODE_EXTRA_CA_CERTS"], gatewayCA)
	}

	combinedBundle, err := os.ReadFile(sslCertFile)
	if err != nil {
		t.Fatalf("read combined CA bundle: %v", err)
	}
	if !strings.HasPrefix(string(combinedBundle), string(systemBundle)) {
		t.Fatal("combined CA bundle does not include the system CA bundle")
	}
	if !strings.HasSuffix(string(combinedBundle), gatewayCert) {
		t.Fatal("combined CA bundle does not include the gateway CA")
	}
}

func readEntrypointEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	env := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed env line %q", line)
		}
		env[key] = value
	}
	return env
}
