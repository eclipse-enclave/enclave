// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"strings"
	"testing"
)

// A bundle reload signals the running proxy instead of restarting it, so the
// proxy keeps its in-memory state.
func TestEntrypointReloadSignalsProxyInPlace(t *testing.T) {
	out, err := runGatewayDNSMasq(t, `PROXY_ENABLED=true
PROXY_READY_FILE="$DATA/proxy.ready"
sh -c 'trap "touch \"$1\"" HUP; touch "$1.started"; while :; do sleep 0.05; done' fake-proxy "$PROXY_READY_FILE" &
PROXY_PID=$!
proxy_before=$PROXY_PID
while [ ! -f "$PROXY_READY_FILE.started" ]; do sleep 0.05; done
start_dnsmasq() { sleep 30 & DNSMASQ_PID=$!; }
start_proxy() { printf 'UNEXPECTED restart\n'; return 1; }
if reload_children; then printf 'reloaded\n'; else printf 'reload failed\n'; fi
[ -f "$PROXY_READY_FILE" ] && printf 'proxy ready again\n'
is_pid_running "$proxy_before" && [ "$PROXY_PID" = "$proxy_before" ] && printf 'same proxy\n'
kill "$PROXY_PID" "$DNSMASQ_PID" 2>/dev/null || true`, "DATA="+t.TempDir())
	if err != nil {
		t.Fatalf("reload: %v\n%s", err, out)
	}
	for _, want := range []string{"reloaded\n", "proxy ready again\n", "same proxy\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "UNEXPECTED") {
		t.Errorf("reload restarted the proxy:\n%s", out)
	}
}

// A proxy that dies while reloading fails the reload, which shuts the gateway
// down fail-closed.
func TestEntrypointReloadFailsWhenProxyDies(t *testing.T) {
	out, err := runGatewayDNSMasq(t, `PROXY_ENABLED=true
PROXY_READY_FILE="$DATA/proxy.ready"
PROXY_LOG_FILE="$DATA/proxy.log"
: > "$PROXY_LOG_FILE"
sh -c 'trap "exit 1" HUP; touch "$1.started"; while :; do sleep 0.05; done' fake-proxy "$PROXY_READY_FILE" &
PROXY_PID=$!
while [ ! -f "$PROXY_READY_FILE.started" ]; do sleep 0.05; done
if reload_proxy; then printf 'UNEXPECTED success\n'; fi`, "DATA="+t.TempDir())
	if err != nil || strings.Contains(out, "UNEXPECTED") {
		t.Fatalf("want the reload to fail, got %v:\n%s", err, out)
	}
}
