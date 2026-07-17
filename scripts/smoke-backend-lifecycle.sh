#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Live smoke tests for the isolation-backend store/auth lifecycle: session
# auth survival across foreground/background/exec, reset and ephemeral store
# handling, feature auth, and restricted networking. Requires a Docker daemon
# and network access; builds the claude tool image on first use and runs real
# containers.
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
SMOKE_ROOT=${SMOKE_ROOT:-$HOME/.enclave-smoke}
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
AUTH="$STATE_ROOT/tools/claude/auth"
GHAUTH="$STATE_ROOT/features/github-cli/auth"

step_sanity() {
  out=$(run_tty "$P1" shell --allow-all-network -- -c 'echo smoke-sanity-ok')
  echo "$out" | grep -q smoke-sanity-ok || fail "sanity: shell run did not echo (output: $out)"
  pass "sanity foreground shell run"
}

step_fg_auth_sync() {
  out=$(run_tty "$P1" shell --allow-all-network -- -c "
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
  out=$(run_tty "$P2" shell --allow-all-network -- -c '
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
  run_tty "$P1" run --background --name smokebg --allow-all-network >/dev/null
  local name="enclave-claude-${H1}-smokebg"
  docker ps --format '{{.Names}}' | grep -qx "$name" || fail "bg: container $name not running"
  docker exec "$name" bash -c "printf '%s' '$CRED400' > ~/.claude/.credentials.json" || fail "bg: exec write failed"
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  docker ps -a --format '{{.Names}}' | grep -qx "$name" && fail "bg: container not removed by stop"
  got=$(storecat "$AUTH" .credentials.json)
  [ "$got" = "$CRED400" ] || fail "bg: stop finalize did not reconcile newer cred (got: $got)"
  pass "background stop finalized auth (cred400 reconciled) and removed container"
}

step_exec_sync() {
  run_tty "$P1" run --background --name smokexec --allow-all-network >/dev/null
  local name="enclave-claude-${H1}-smokexec"
  docker ps --format '{{.Names}}' | grep -qx "$name" || fail "exec: container $name not running"
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
  run_tty "$P1" run --background --name smokeps --allow-all-network >/dev/null
  local name="enclave-claude-${H1}-smokeps"
  "$CLI_BIN" ps 2>/dev/null | grep -q smokeps || fail "ps: session not listed"
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  pass "ps lists background session"
}

step_persisted_env() {
  out=$(SMOKE_TOKEN=tokenA run_tty "$P1" shell --allow-all-network --pass-env SMOKE_TOKEN -- -c 'echo "token=$SMOKE_TOKEN"')
  echo "$out" | grep -q "token=tokenA" || fail "env: pass-env did not inject ($out)"
  storehas "$ENV1" env || fail "env: persisted env file missing"
  storecat "$ENV1" env | grep -q "SMOKE_TOKEN=tokenA" || fail "env: token not persisted"
  # New process without the host var: injected from the persisted env store.
  out=$(run_tty "$P1" shell --allow-all-network --pass-env SMOKE_TOKEN -- -c 'echo "token=$SMOKE_TOKEN"')
  echo "$out" | grep -q "token=tokenA" || fail "env: persisted token not re-injected ($out)"
  pass "persisted env survives across runs"
}

step_reset_auth() {
  out=$(run_tty "$P1" shell --allow-all-network --reset-auth -- -c '
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
  before=$(docker volume ls -q | sort)
  out=$(run_tty "$P1" shell --allow-all-network --ephemeral -- -c 'echo eph-ok')
  echo "$out" | grep -q eph-ok || fail "ephemeral: run failed ($out)"
  sleep 2
  after=$(docker volume ls -q | sort)
  [ "$before" = "$after" ] || fail "ephemeral: volume set changed: $(diff <(echo "$before") <(echo "$after"))"
  pass "ephemeral session ran with a unique config store and created no Docker volumes"
}

step_restart_stopped() {
  run_tty "$P1" run --background --name smokerestart --allow-all-network >/dev/null
  local name="enclave-claude-${H1}-smokerestart"
  docker stop -t 2 "$name" >/dev/null 2>&1 || fail "restart: docker stop failed"
  out=$(run_tty "$P1" run --background --name smokerestart --allow-all-network)
  echo "$out" | grep -q "Removing stopped container" || fail "restart: pre-start removal not logged ($out)"
  docker ps --format '{{.Names}}' | grep -qx "$name" || fail "restart: session not running again"
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

ALL="sanity fg_auth_sync cross_project bg_stop_finalize exec_sync ps persisted_env reset_auth ephemeral restart_stopped network_status restricted_network"
steps=${*:-$ALL}
for s in $steps; do
  echo "=== step: $s ==="
  "step_$s"
done
echo "ALL SMOKE STEPS PASSED"
