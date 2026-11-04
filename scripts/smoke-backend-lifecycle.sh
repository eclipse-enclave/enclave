#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Live smoke tests for the isolation-backend store/auth lifecycle: session
# auth survival across foreground/background/exec, reset and ephemeral store
# handling, feature auth, restricted networking, and the network log of
# unrestricted sessions. Requires a Docker daemon (or podman, with
# ENGINE=podman) and network access; builds the claude tool and gateway images
# on first use and runs real containers.
#
# Lifecycle steps run unrestricted with --disable-gateway so they do not depend
# on the gateway; the network steps cover it.
#
# DESTRUCTIVE within its scope: exercises --reset-auth against the shared
# claude auth store and writes fake credentials there. Run it only on a
# machine whose host-directory claude/github-cli auth stores (under
# ~/.local/state/enclave/) hold no real credentials.
#
# Usage: scripts/smoke-backend-lifecycle.sh [step...]   (no args = all steps)
set -u

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
CLI_BIN=${CLI_BIN:-$REPO_ROOT/bin/enclave}
ENGINE=${ENGINE:-docker}
# Outside home: enclave refuses to mount hidden home directories. Stable across
# invocations, so steps run separately share their projects.
SMOKE_ROOT=${SMOKE_ROOT:-${TMPDIR:-/tmp}/enclave-smoke-$(id -u)}
P1=$SMOKE_ROOT/project1
P2=$SMOKE_ROOT/project2

CRED300='{"claudeAiOauth":{"expiresAt":300}}'
CRED400='{"claudeAiOauth":{"expiresAt":400}}'
CRED500='{"claudeAiOauth":{"expiresAt":500}}'
GHHOSTS='github.com: {user: smoke}'

pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*"; exit 1; }

[ -x "$CLI_BIN" ] || fail "enclave binary not found at $CLI_BIN (run make build)"
mkdir -p "$P1" "$P2"
echo smoke1 > "$P1/README.md"
echo smoke2 > "$P2/README.md"

# run_tty <dir> <enclave args...> — run enclave under a pty, capture output.
run_tty() {
  local dir=$1; shift
  local cmd
  cmd=$(printf '%q ' "$CLI_BIN" "$@")
  (cd "$dir" && script -qec "$cmd" /dev/null 2>&1)
}

# Persistent stores are host directories under the XDG state root, read
# and written directly from the host filesystem — no Docker volumes.
storecat() { cat "$1/$2" 2>/dev/null; }
storehas() { [ -e "$1/$2" ]; }

hash_for() { (cd "$1" && "$CLI_BIN" info --tool claude 2>/dev/null | awk '/Project Hash:/ {print $3}'); }

H1=$(hash_for "$P1")
[ -n "$H1" ] || fail "could not resolve project hash for $P1"
STATE_ROOT="${XDG_STATE_HOME:-$HOME/.local/state}/enclave"
ENV1="$STATE_ROOT/projects/${H1}/claude/env"
NETLOG1="$STATE_ROOT/projects/${H1}/claude/logs/network.log"
AUTH="$STATE_ROOT/tools/claude/auth/default"
GHAUTH="$STATE_ROOT/features/github-cli/auth"

step_baseline() {
  out=$(run_tty "$P1" shell --allow-all-network --disable-gateway -- -c 'echo smoke-baseline-ok')
  echo "$out" | grep -q smoke-baseline-ok || fail "baseline: shell run did not echo (output: $out)"
  pass "baseline foreground shell run"
}

step_fg_auth_sync() {
  out=$(run_tty "$P1" shell --allow-all-network --disable-gateway -- -c "
    echo \"securestorage=\$CLAUDE_SECURESTORAGE_CONFIG_DIR\"
    printf '%s' '$CRED300' > ~/.claude/.credentials.json
    mkdir -p ~/.claude/.config/gh
    printf '%s' '$GHHOSTS' > ~/.claude/.config/gh/hosts.yml
    echo wrote-creds
  ")
  echo "$out" | grep -q "wrote-creds" || fail "fg: session did not write creds ($out)"
  echo "$out" | grep -q "securestorage=/home/agent/.enclave-auth" || fail "fg: securestorage env missing ($out)"
  got=$(storecat "$AUTH" .credentials.json)
  [ "$got" = "$CRED300" ] || fail "fg: shared auth not synced post-run (got: $got)"
  gotgh=$(storecat "$GHAUTH" hosts.yml)
  [ "$gotgh" = "$GHHOSTS" ] || fail "fg: gh feature auth not synced post-run (got: $gotgh)"
  pass "foreground run synced shared auth + gh feature auth on exit (auto-remove path)"
}

step_cross_project() {
  # The onboarding marker may live in config.json (fresh store: written by the
  # OnAuthReady hook) or in .claude.json (steady state: the entrypoint links
  # config-dir auth files into the shared auth mount and claude reads
  # ~/.claude.json). Assert the functional outcome, not one specific file.
  out=$(run_tty "$P2" shell --allow-all-network --disable-gateway -- -c '
    cat ~/.claude/config.json 2>/dev/null
    cat ~/.claude.json 2>/dev/null
    echo ---
    cat ~/.enclave-auth/.credentials.json 2>/dev/null
  ')
  echo "$out" | grep -q "Credentials: Subscription/session is present" || fail "cross: shared session not detected ($out)"
  echo "$out" | grep -q hasCompletedOnboarding || fail "cross: onboarding marker missing ($out)"
  echo "$out" | grep -q '"expiresAt":300' || fail "cross: shared credential not visible in second project ($out)"
  pass "second project sees shared session; onboarding marker present"
}

step_bg_stop_finalize() {
  run_tty "$P1" run --background --name smokebg --allow-all-network --disable-gateway >/dev/null
  local name="enclave-claude-${H1}-smokebg"
  "$ENGINE" ps --format '{{.Names}}' | grep -qx "$name" || fail "bg: container $name not running"
  "$ENGINE" exec "$name" bash -c "printf '%s' '$CRED400' > ~/.claude/.credentials.json" || fail "bg: exec write failed"
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  "$ENGINE" ps -a --format '{{.Names}}' | grep -qx "$name" && fail "bg: container not removed by stop"
  got=$(storecat "$AUTH" .credentials.json)
  [ "$got" = "$CRED400" ] || fail "bg: stop finalize did not reconcile newer cred (got: $got)"
  pass "background stop finalized auth (cred400 reconciled) and removed container"
}

step_exec_sync() {
  run_tty "$P1" run --background --name smokexec --allow-all-network --disable-gateway >/dev/null
  local name="enclave-claude-${H1}-smokexec"
  "$ENGINE" ps --format '{{.Names}}' | grep -qx "$name" || fail "exec: container $name not running"
  run_tty "$P1" exec --name smokexec -- bash -c "printf '%s' '$CRED500' > ~/.claude/.credentials.json && echo exec-done" | grep -q exec-done || fail "exec: command failed"
  got=$(storecat "$AUTH" .credentials.json)
  [ "$got" = "$CRED500" ] || fail "exec: credentials not synced immediately after exec (got: $got)"
  # The exec'd command's exit status must survive the backend seam to the CLI.
  run_tty "$P1" exec --name smokexec -- bash -c 'exit 7' >/dev/null
  code=$?
  [ "$code" = 7 ] || fail "exec: exit status not preserved (got $code, want 7)"
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  pass "exec synced credentials immediately and preserved the command's exit status"
}

step_ps() {
  run_tty "$P1" run --background --name smokeps --allow-all-network --disable-gateway >/dev/null
  local name="enclave-claude-${H1}-smokeps"
  "$CLI_BIN" ps 2>/dev/null | grep -q smokeps || fail "ps: session not listed"
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  pass "ps lists background session"
}

step_persisted_env() {
  out=$(SMOKE_TOKEN=tokenA run_tty "$P1" shell --allow-all-network --disable-gateway --pass-env SMOKE_TOKEN -- -c 'echo "token=$SMOKE_TOKEN"')
  echo "$out" | grep -q "token=tokenA" || fail "env: pass-env did not inject ($out)"
  storehas "$ENV1" env || fail "env: persisted env file missing"
  storecat "$ENV1" env | grep -q "SMOKE_TOKEN=tokenA" || fail "env: token not persisted"
  # New process without the host var: injected from the persisted env store.
  out=$(run_tty "$P1" shell --allow-all-network --disable-gateway --pass-env SMOKE_TOKEN -- -c 'echo "token=$SMOKE_TOKEN"')
  echo "$out" | grep -q "token=tokenA" || fail "env: persisted token not re-injected ($out)"
  pass "persisted env survives across runs"
}

step_reset_auth() {
  out=$(run_tty "$P1" shell --allow-all-network --disable-gateway --reset-auth -- -c '
    ls ~/.claude/.credentials.json 2>&1
    ls ~/.enclave-auth/.credentials.json 2>&1
  ')
  echo "$out" | grep -c "No such file" | grep -q 2 || fail "reset: auth files still present in session ($out)"
  storehas "$AUTH" .credentials.json && fail "reset: shared auth file survived --reset-auth"
  # Reset drops previously persisted values; the same run may legitimately
  # re-persist secrets that are live in the host environment (GH_TOKEN etc.),
  # so assert the old token is gone rather than the file being absent.
  if storehas "$ENV1" env; then
    storecat "$ENV1" env | grep -q SMOKE_TOKEN && fail "reset: persisted SMOKE_TOKEN survived --reset-auth"
  fi
  pass "--reset-auth cleared config+shared auth files and dropped persisted values"
}

step_ephemeral() {
  # Stores are host directories, so an ephemeral session must not create
  # any Docker volume. Assert the volume set is unchanged across the run.
  before=$("$ENGINE" volume ls -q | sort)
  out=$(run_tty "$P1" shell --allow-all-network --disable-gateway --ephemeral -- -c 'echo eph-ok')
  echo "$out" | grep -q eph-ok || fail "ephemeral: run failed ($out)"
  sleep 2
  after=$("$ENGINE" volume ls -q | sort)
  [ "$before" = "$after" ] || fail "ephemeral: volume set changed: $(diff <(echo "$before") <(echo "$after"))"
  pass "ephemeral session ran with a unique config store and created no Docker volumes"
}

step_restart_stopped() {
  run_tty "$P1" run --background --name smokerestart --allow-all-network --disable-gateway >/dev/null
  local name="enclave-claude-${H1}-smokerestart"
  "$ENGINE" stop -t 2 "$name" >/dev/null 2>&1 || fail "restart: $ENGINE stop failed"
  out=$(run_tty "$P1" run --background --name smokerestart --allow-all-network --disable-gateway)
  echo "$out" | grep -q "Removing stopped container" || fail "restart: pre-start removal not logged ($out)"
  "$ENGINE" ps --format '{{.Names}}' | grep -qx "$name" || fail "restart: session not running again"
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  pass "stopped session container replaced on restart"
}

step_network_status() {
  out=$(cd "$P1" && "$CLI_BIN" network status 2>&1)
  echo "$out" | grep -q "no running gateways matched" || fail "network: unexpected status output ($out)"
  pass "network status runs through the backend gateway manager"
}

step_restricted_network() {
  out=$(run_tty "$P1" shell -- -c '
    echo -n allowed=; curl -s -o /dev/null -w "%{http_code}" --max-time 15 https://api.anthropic.com/ || echo -n curl-fail; echo
    echo -n blocked=; curl -s -o /dev/null -w "%{http_code}" --max-time 8 https://example.com/ || echo -n curl-fail; echo
  ')
  echo "$out" | grep -q "allowed=[2345]" || fail "restricted: allowlisted domain unreachable ($out)"
  echo "$out" | grep -Eq "blocked=(000|curl-fail)" || fail "restricted: blocked domain was reachable ($out)"
  pass "restricted networking enforced through the gateway"
}

# netlog_size — current byte size of project1's claude network log (0 if absent).
netlog_size() { wc -c 2>/dev/null < "$NETLOG1" || echo 0; }

# curl_example <enclave args...> — curl https://example.com from a session in
# project1 and print "reached=<status>" or "reached=curl-fail".
curl_example() {
  run_tty "$P1" shell "$@" -- -c '
    echo -n reached=; curl -s -o /dev/null -w "%{http_code}" --max-time 15 https://example.com/ || echo -n curl-fail; echo
  '
}

step_unrestricted_network_log() {
  # Reads the bytes appended since the start; assumes the log stays below the
  # 32 MB rotation cap, which would truncate it at session start.
  local before new
  before=$(netlog_size)
  out=$(curl_example --allow-all-network)
  echo "$out" | grep -q "reached=[2345]" || fail "unrestricted: non-allowlisted domain unreachable through the observing gateway ($out)"
  new=$(tail -c +$((before + 1)) "$NETLOG1" 2>/dev/null)
  echo "$new" | grep '"type":"session"' -q || fail "unrestricted: no session marker written (new log lines: $new)"
  echo "$new" | grep '"domain":"example.com"' | grep -q '"rule":"unrestricted"' \
    || fail "unrestricted: no network event for example.com (new log lines: $new)"
  pass "unrestricted session reached a non-allowlisted domain and logged it"
}

step_disabled_gateway() {
  local before after
  before=$(netlog_size)
  out=$(curl_example --allow-all-network --disable-gateway)
  echo "$out" | grep -q "reached=[2345]" || fail "disabled gateway: domain unreachable ($out)"
  after=$(netlog_size)
  [ "$before" = "$after" ] || fail "disabled gateway: network log grew ($before -> $after bytes)"
  # Assumes the persisted network policy mode is restricted, like restricted_network.
  out=$(run_tty "$P1" shell --disable-gateway -- -c 'echo ran-restricted')
  echo "$out" | grep -q ran-restricted && fail "disabled gateway: restricted run with --disable-gateway was accepted ($out)"
  echo "$out" | grep -q "requires unrestricted network mode" || fail "disabled gateway: unexpected restricted-mode error ($out)"
  pass "--disable-gateway runs without a gateway or log and is rejected in restricted mode"
}

ALL="baseline fg_auth_sync cross_project bg_stop_finalize exec_sync ps persisted_env reset_auth ephemeral restart_stopped network_status restricted_network unrestricted_network_log disabled_gateway"
steps=${*:-$ALL}
for s in $steps; do
  echo "=== step: $s ==="
  "step_$s"
done
echo "ALL SMOKE STEPS PASSED"
