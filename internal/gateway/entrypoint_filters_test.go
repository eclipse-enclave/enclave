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
	"strings"
	"testing"
)

// filterEntrypointEnv prepares a mounted filter dir, a proxy data dir, and a
// PATH whose chown records its arguments instead of changing owners.
type filterEntrypointEnv struct {
	src, data, chownLog string
	path                string
}

func newFilterEntrypointEnv(t *testing.T, files map[string]string) filterEntrypointEnv {
	t.Helper()
	root := t.TempDir()
	env := filterEntrypointEnv{
		src:      filepath.Join(root, "mounted"),
		data:     filepath.Join(root, "proxy"),
		chownLog: filepath.Join(root, "chown.log"),
	}
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{env.src, env.data, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(env.src, name), []byte(content), 0o644); err != nil { // #nosec G306 -- the copy must tighten it.
			t.Fatal(err)
		}
	}
	stubs := map[string]string{
		"chown":                 "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CHOWN_LOG\"\nexit \"${CHOWN_EXIT:-0}\"\n",
		"enclave-gateway-proxy": "#!/bin/sh\nexit 0\n",
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil { // #nosec G306 -- test executable.
			t.Fatal(err)
		}
	}
	env.path = "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
	return env
}

func (e filterEntrypointEnv) run(t *testing.T, invocation string, extra ...string) (string, error) {
	t.Helper()
	env := append([]string{e.path, "CHOWN_LOG=" + e.chownLog, "DATA=" + e.data, "ENCLAVE_FILTER_DIR=" + e.src}, extra...)
	return runGatewayDNSMasq(t, invocation, env...)
}

func validFilterFiles() map[string]string {
	return map[string]string{
		"gws.policy.json":     `{"filter":"gws"}`,
		"gws.credential.json": `{"refreshToken":"secret"}`,
	}
}

func TestEntrypointCopiesFiltersIntoPrivateProxyData(t *testing.T) {
	env := newFilterEntrypointEnv(t, validFilterFiles())

	out, err := env.run(t, `PROXY_ENABLED=true
prepare_proxy_filters "$DATA"
sh -c 'printf "child=%s\n" "$ENCLAVE_FILTER_DIR"'`)
	if err != nil {
		t.Fatalf("prepare_proxy_filters failed: %v\n%s", err, out)
	}

	dst := filepath.Join(env.data, "filters")
	if !strings.Contains(out, "child="+dst+"\n") {
		t.Fatalf("ENCLAVE_FILTER_DIR is not exported as %s:\n%s", dst, out)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("%s mode = %o, want 700", dst, info.Mode().Perm())
	}
	for name, content := range validFilterFiles() {
		path := filepath.Join(dst, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
		if data, _ := os.ReadFile(path); string(data) != content {
			t.Errorf("%s = %q, want %q", path, data, content)
		}
	}
	chowned, err := os.ReadFile(env.chownLog)
	if err != nil || strings.TrimSpace(string(chowned)) != "-R gateway:gateway "+dst {
		t.Fatalf("chown calls = %q, %v, want the copy handed to the proxy user", chowned, err)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("output leaks credential content:\n%s", out)
	}
}

func TestEntrypointWithoutFiltersLeavesProxyDataAlone(t *testing.T) {
	env := newFilterEntrypointEnv(t, nil)

	out, err := env.run(t, `PROXY_ENABLED=true
ENCLAVE_FILTER_DIR=
prepare_proxy_filters "$DATA"`)
	if err != nil {
		t.Fatalf("prepare_proxy_filters failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(env.data, "filters")); !os.IsNotExist(err) {
		t.Fatalf("a filter copy exists without filters: %v", err)
	}
}

// A filter dir that is set but unusable fails closed.
func TestEntrypointRejectsUnusableFilterDir(t *testing.T) {
	for name, tc := range map[string]struct {
		files      map[string]string
		invocation string
		extra      []string
		message    string
	}{
		"missing":         {invocation: `rm -rf "$ENCLAVE_FILTER_DIR"`, message: "Request filter directory missing"},
		"empty":           {message: "Request filter directory is empty"},
		"unexpected file": {files: map[string]string{"gws.policy.json": "{}", "install.sh": "#!/bin/sh"}, message: "Unexpected entry in request filter directory: install.sh"},
		"hidden file":     {files: map[string]string{"gws.policy.json": "{}", ".gws.policy.json.swp": "x"}, message: "Unexpected entry"},
		"symlink":         {files: validFilterFiles(), invocation: `ln -s /etc/passwd "$ENCLAVE_FILTER_DIR/other.policy.json"`, message: "not a regular file: other.policy.json"},
		"subdirectory":    {files: validFilterFiles(), invocation: `mkdir "$ENCLAVE_FILTER_DIR/x.policy.json"`, message: "not a regular file"},
		"chown fails":     {files: validFilterFiles(), extra: []string{"CHOWN_EXIT=1"}, message: "Cannot hand request filters to gateway"},
		"proxy disabled":  {files: validFilterFiles(), invocation: "PROXY_ENABLED=false", message: "Request filters need the proxy, aborting"},
	} {
		t.Run(name, func(t *testing.T) {
			env := newFilterEntrypointEnv(t, tc.files)
			out, err := env.run(t, `PROXY_ENABLED=true
`+tc.invocation+`
if prepare_proxy_filters "$DATA"; then printf 'UNEXPECTED success\n'; fi`, tc.extra...)
			if err != nil || strings.Contains(out, "UNEXPECTED") || !strings.Contains(out, tc.message) {
				t.Fatalf("want a failure naming %q, got %v:\n%s", tc.message, err, out)
			}
		})
	}
}

func TestEntrypointProxyRuntimeDataFailsClosedOnFilterError(t *testing.T) {
	env := newFilterEntrypointEnv(t, nil)

	out, err := env.run(t, `PROXY_ENABLED=true
PROXY_DATA_DIR="$DATA"
if prepare_proxy_runtime_data; then printf 'UNEXPECTED success\n'; fi`)
	if err != nil || strings.Contains(out, "UNEXPECTED") || !strings.Contains(out, "Request filter directory is empty") {
		t.Fatalf("want prepare_proxy_runtime_data to fail, got %v:\n%s", err, out)
	}
}
