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

func TestEntrypointGatewayCAWritesBundleAtFixedPath(t *testing.T) {
	t.Parallel()

	systemBundle, err := os.ReadFile("/etc/ssl/certs/ca-certificates.crt")
	if err != nil {
		t.Skipf("system CA bundle unavailable: %v", err)
	}

	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	fakeBin := filepath.Join(home, "bin")
	for _, dir := range []string{projectDir, fakeBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	updateCAScript := "#!/bin/sh\nprintf called > \"$HOME/update-ca-certificates.called\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "update-ca-certificates"), []byte(updateCAScript), 0o755); err != nil {
		t.Fatalf("write update-ca-certificates shim: %v", err)
	}

	gatewayCA := filepath.Join(home, "gateway.crt")
	gatewayCert := "-----BEGIN CERTIFICATE-----\nenclave-gateway-test\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(gatewayCA, []byte(gatewayCert), 0o644); err != nil {
		t.Fatalf("write gateway CA: %v", err)
	}
	// A stale bundle from an earlier start must be replaced, not appended to.
	bundlePath := filepath.Join(home, "ca", "ca-certificates.crt")
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o755); err != nil {
		t.Fatalf("mkdir bundle dir: %v", err)
	}
	if err := os.WriteFile(bundlePath, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale bundle: %v", err)
	}

	captureEnv := `{
printf 'SSL_CERT_FILE=%s\n' "${SSL_CERT_FILE:-}"
printf 'REQUESTS_CA_BUNDLE=%s\n' "${REQUESTS_CA_BUNDLE:-}"
printf 'NODE_EXTRA_CA_CERTS=%s\n' "${NODE_EXTRA_CA_CERTS:-}"
} > "$HOME/gateway-ca-env.out"`
	cmd := exec.Command("bash", filepath.Join("..", "..", "entrypoint.sh"), "bash", "-lc", captureEnv)
	cmd.Env = []string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=pi",
		"ENCLAVE_GATEWAY_CA_CERT_PATH=" + gatewayCA,
		"ENCLAVE_GATEWAY_CA_BUNDLE_PATH=" + bundlePath,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, string(out))
	}
	if _, err := os.Stat(filepath.Join(home, "update-ca-certificates.called")); err != nil {
		t.Fatalf("expected update-ca-certificates shim to run: %v", err)
	}

	env := readEntrypointEnvFile(t, filepath.Join(home, "gateway-ca-env.out"))
	if env["NODE_EXTRA_CA_CERTS"] != gatewayCA {
		t.Fatalf("NODE_EXTRA_CA_CERTS = %q, want %q", env["NODE_EXTRA_CA_CERTS"], gatewayCA)
	}
	if env["SSL_CERT_FILE"] != bundlePath {
		t.Fatalf("SSL_CERT_FILE = %q, want %q", env["SSL_CERT_FILE"], bundlePath)
	}
	if env["REQUESTS_CA_BUNDLE"] != bundlePath {
		t.Fatalf("REQUESTS_CA_BUNDLE = %q, want %q", env["REQUESTS_CA_BUNDLE"], bundlePath)
	}
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if string(bundle) != string(systemBundle)+gatewayCert {
		t.Fatal("bundle is not exactly the system bundle followed by the gateway CA")
	}
}

// The container environment points SSL_CERT_FILE and REQUESTS_CA_BUNDLE at the
// bundle before the entrypoint writes it. If the write fails, the entrypoint
// must not hand a missing trust store to the session.
func TestEntrypointGatewayCAUnsetsBundleVarsWhenWriteFails(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	fakeBin := filepath.Join(home, "bin")
	for _, dir := range []string{projectDir, fakeBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "update-ca-certificates"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write update-ca-certificates shim: %v", err)
	}

	gatewayCA := filepath.Join(home, "gateway.crt")
	if err := os.WriteFile(gatewayCA, []byte("-----BEGIN CERTIFICATE-----\nenclave-gateway-test\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatalf("write gateway CA: %v", err)
	}
	// A regular file where the bundle directory should be makes mkdir -p fail.
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	bundlePath := filepath.Join(blocker, "ca-certificates.crt")

	captureEnv := `{
printf 'SSL_CERT_FILE=%s\n' "${SSL_CERT_FILE-<unset>}"
printf 'REQUESTS_CA_BUNDLE=%s\n' "${REQUESTS_CA_BUNDLE-<unset>}"
printf 'NODE_EXTRA_CA_CERTS=%s\n' "${NODE_EXTRA_CA_CERTS:-}"
} > "$HOME/gateway-ca-env.out"`
	cmd := exec.Command("bash", filepath.Join("..", "..", "entrypoint.sh"), "bash", "-lc", captureEnv)
	cmd.Env = []string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=pi",
		"ENCLAVE_GATEWAY_CA_CERT_PATH=" + gatewayCA,
		"ENCLAVE_GATEWAY_CA_BUNDLE_PATH=" + bundlePath,
		"SSL_CERT_FILE=" + bundlePath,
		"REQUESTS_CA_BUNDLE=" + bundlePath,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, string(out))
	}

	env := readEntrypointEnvFile(t, filepath.Join(home, "gateway-ca-env.out"))
	for _, key := range []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE"} {
		if env[key] != "<unset>" {
			t.Fatalf("%s = %q, want it unset", key, env[key])
		}
	}
	if env["NODE_EXTRA_CA_CERTS"] != gatewayCA {
		t.Fatalf("NODE_EXTRA_CA_CERTS = %q, want %q", env["NODE_EXTRA_CA_CERTS"], gatewayCA)
	}
}
