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

	"enclave/internal/gateway/exchange"
	"enclave/internal/model"
)

func TestNativeStartupMountsAndCapabilityGate(t *testing.T) {
	log := withGatewayCLIStub(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	store := filepath.Join(dir, "canonical")
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"provider":{},"secrets":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &NativeAuthStart{ConfigFile: config, StoreDir: store, LockPath: filepath.Join(dir, "canonical.lock"), UID: 1000, GID: 1000}
	mounts, env, runtime, digest, err := prepareNativeAuth(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(runtime) }()
	if len(mounts) != 4 || !mounts[0].ReadOnly || mounts[0].Target != model.GatewayNativeAuthConfigPath {
		t.Fatal("native declaration not read-only")
	}
	for _, mount := range mounts[1:] {
		if mount.ReadOnly {
			t.Fatal("canonical store, lock or capability dir not writable")
		}
	}
	if !strings.Contains(strings.Join(env, "\n"), model.EnvRuntimeUID+"=1000") {
		t.Fatal("proxy not given host identity")
	}
	info, err := os.Stat(runtime)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("capability directory not private")
	}
	if err := checkNativeAuthStarted(runtime, digest, "session", "gw-pinned-id"); err == nil {
		t.Fatal("old image started without attestation")
	}
	calls := strings.Join(stubCalls(t, log), "\n")
	if !strings.Contains(calls, "stop --time 3 gw-pinned-id") {
		t.Fatal("failed capability did not stop pinned gateway")
	}
	if err := exchange.WriteCapability(runtime, digest, "session"); err != nil {
		t.Fatal(err)
	}
	if err := checkNativeAuthStarted(runtime, digest, "session", "gw-pinned-id"); err != nil {
		t.Fatal(err)
	}
	_, _, other, _, err := prepareNativeAuth(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(other) }()
	if runtime == other || exchange.CheckCapability(other, digest, "session") == nil {
		t.Fatal("stale capability reused on another start")
	}
	cfg.UID = 0
	if _, _, _, _, err := prepareNativeAuth(cfg, nil, nil); err == nil {
		t.Fatal("root native proxy accepted")
	}
}

func TestNativeIdentityRemapUsesContainerDatabasesOnly(t *testing.T) {
	for _, identity := range []string{"1001:2002", "0:2002", "bad:2002", "1001:", "53:2002"} {
		t.Run(identity, func(t *testing.T) {
			dir := t.TempDir()
			passwd := filepath.Join(dir, "passwd")
			group := filepath.Join(dir, "group")
			initialPasswd := "root:x:0:0:root:/root:/bin/sh\ngateway:x:101:102:gateway:/:/sbin/nologin\n"
			if err := os.WriteFile(passwd, []byte(initialPasswd), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(group, []byte("root:x:0:\ngateway:x:102:\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			source := strings.ReplaceAll(strings.ReplaceAll(gatewayScriptFunctions(t), "/etc/passwd", passwd), "/etc/group", group)
			script := filepath.Join(dir, "entrypoint.sh")
			if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			ids := strings.Split(identity, ":")
			cmd := exec.Command("sh", "-c", `. "$1"
id() {
 case "$1:$2" in
  -u:dnsmasq) echo 53 ;;
  -u:gateway) awk -F: '$1=="gateway" {print $3}' "$TEST_PASSWD" ;;
  -g:gateway) awk -F: '$1=="gateway" {print $4}' "$TEST_PASSWD" ;;
 esac
}
chown() { echo unexpected-chown; exit 91; }
chmod() { echo unexpected-chmod; exit 92; }
remap_native_proxy_identity`, "native-remap-test", script)
			cmd.Env = append(os.Environ(), model.EnvNativeAuthConfig+"=/native-config", model.EnvNativeAuthStore+"=/native", model.EnvNativeAuthLock+"=/lock", model.EnvNativeAuthRuntime+"=/runtime", model.EnvRuntimeUID+"="+ids[0], model.EnvRuntimeGID+"="+ids[1], "TEST_PASSWD="+passwd)
			out, err := cmd.CombinedOutput()
			data, readErr := os.ReadFile(passwd)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if identity == "1001:2002" {
				if err != nil || !strings.Contains(string(data), "gateway:x:1001:2002:") {
					t.Fatalf("host identity not remapped: %s %v", out, err)
				}
			} else if err == nil || string(data) != initialPasswd {
				t.Fatal("invalid identity changed container databases")
			}
		})
	}
	// Ordinary gateways need no host identity and do not touch account databases.
	out, err := runGatewayDNSMasq(t, "remap_native_proxy_identity", model.EnvNativeAuthConfig+"=")
	if err != nil {
		t.Fatalf("ordinary gateway changed: %s %v", out, err)
	}
}
