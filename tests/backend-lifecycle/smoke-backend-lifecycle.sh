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
# unrestricted sessions. Runs under tmt with rootless Podman.
#
# Lifecycle steps run unrestricted with --disable-gateway so they do not depend
# on the gateway; the network steps cover it.
#
# shellcheck source=/dev/null
. /usr/share/beakerlib/beakerlib.sh || exit 1
rlJournalStart || exit 1

case "${ENCLAVE_TMT_SHARED_PODMAN_DATA_DIR:-}" in
  /*) ;;
  *) echo "shared Podman data directory must be absolute" >&2; exit 1 ;;
esac
test_root=$(mktemp -d /var/tmp/enclave-lifecycle.XXXXXX) || exit 1
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/home" "$test_root/config/enclave" "$test_root/state" \
  "$test_root/cache" || exit 1
printf '{"backend":"podman","tool":"claude"}\n' > "$test_root/config/enclave/config.json"
printf '[user]\n\tname = Enclave Smoke Test\n\temail = smoke@example.invalid\n' > "$test_root/home/.gitconfig"
export HOME="$test_root/home"
export XDG_CONFIG_HOME="$test_root/config"
export XDG_STATE_HOME="$test_root/state"
export XDG_CACHE_HOME="$test_root/cache"
export XDG_DATA_HOME="$ENCLAVE_TMT_SHARED_PODMAN_DATA_DIR"
CLI_BIN=/usr/bin/enclave
PROJECTS_ROOT="$test_root/smoke"
PROJECT=$PROJECTS_ROOT/project1
OTHER_PROJECT=$PROJECTS_ROOT/project2

FOREGROUND_CREDENTIALS='{"claudeAiOauth":{"expiresAt":300}}'
BACKGROUND_CREDENTIALS='{"claudeAiOauth":{"expiresAt":400}}'
EXEC_CREDENTIALS='{"claudeAiOauth":{"expiresAt":500}}'
GITHUB_HOSTS='github.com: {user: smoke}'

fail() { rlFail "$*"; exit 1; }

[ -x "$CLI_BIN" ] || fail "enclave binary not found at $CLI_BIN"
mkdir -p "$PROJECT" "$OTHER_PROJECT"
echo smoke1 > "$PROJECT/README.md"
echo smoke2 > "$OTHER_PROJECT/README.md"

# run_tty <dir> <enclave args...> — run enclave under a pty, capture output.
run_tty() {
  local dir=$1; shift
  local cmd
  cmd=$(printf '%q ' "$CLI_BIN" "$@")
  (
    cd "$dir" || exit
    if [ "${ENCLAVE_SMOKE_TRACE:-}" = 1 ]; then
      set -o pipefail
      SHELL=/bin/bash script -qec "$cmd" /dev/null 2>&1 | tee /dev/stderr
    else
      SHELL=/bin/bash script -qec "$cmd" /dev/null 2>&1
    fi
  )
}

# Persistent stores are host directories under the XDG state root.
read_store_file() { cat "$1/$2" 2>/dev/null; }
store_file_exists() { [ -e "$1/$2" ]; }

project_hash_for() { (cd "$1" && "$CLI_BIN" info --tool claude 2>/dev/null | awk '/Project Hash:/ {print $3}'); }

PROJECT_HASH=$(project_hash_for "$PROJECT")
[ -n "$PROJECT_HASH" ] || fail "could not resolve project hash for $PROJECT"
STATE_ROOT="$XDG_STATE_HOME/enclave"
ENV_STORE="$STATE_ROOT/projects/${PROJECT_HASH}/claude/env"
NETWORK_LOG="$STATE_ROOT/projects/${PROJECT_HASH}/claude/logs/network.log"
AUTH_STORE="$STATE_ROOT/tools/claude/auth/default"
GITHUB_AUTH_STORE="$STATE_ROOT/features/github-cli/auth"

step_baseline() {
  out=$(ENCLAVE_SMOKE_TRACE=1 run_tty "$PROJECT" shell --allow-all-network --disable-gateway -- -c 'echo smoke-baseline-ok')
  echo "$out" | grep -q smoke-baseline-ok || fail "baseline: shell run did not echo (output: $out)"
  rlPass "baseline foreground shell run"
}

step_filesystem_isolation() {
  local host_file project_file output status
  local config_file="$XDG_CONFIG_HOME/enclave/config.json"
  [ -f "$config_file" ] || fail "filesystem: host enclave config not found at $config_file"
  host_file=$(mktemp "$PROJECTS_ROOT/host-only.XXXXXX") || fail "filesystem: cannot create host canary"
  project_file=$(mktemp "$PROJECT/.filesystem-check.XXXXXX") || {
    rm -f "$host_file"
    fail "filesystem: cannot create project canary"
  }
  printf 'host-only\n' > "$host_file"
  chmod 644 "$host_file"
  output=$(run_tty "$PROJECT" shell --allow-all-network --disable-gateway -- -c '
    if [ -e "$1" ] || [ -L "$1" ]; then
      echo "host-only file is visible"
      exit 1
    fi
    if [ -e "$2" ] || [ -L "$2" ]; then
      echo "host enclave config is visible"
      exit 1
    fi
    printf "container-write\n" > "$3"
  ' bash "$host_file" "$config_file" "$(basename "$project_file")")
  status=$?
  if [ "$status" -ne 0 ] || [ "$(cat "$project_file")" != "container-write" ] ||
      [ "$(cat "$host_file")" != "host-only" ]; then
    rm -f "$host_file" "$project_file"
    fail "filesystem: isolation or project write check failed (exit $status, output: $output)"
  fi
  rm -f "$host_file" "$project_file"
  rlPass "project is writable; host-only file and enclave config are inaccessible"
}

step_foreground_auth_sync() {
  out=$(run_tty "$PROJECT" shell --allow-all-network --disable-gateway -- -c "
    echo \"securestorage=\$CLAUDE_SECURESTORAGE_CONFIG_DIR\"
    printf '%s' '$FOREGROUND_CREDENTIALS' > ~/.claude/.credentials.json
    mkdir -p ~/.claude/.config/gh
    printf '%s' '$GITHUB_HOSTS' > ~/.claude/.config/gh/hosts.yml
    echo wrote-creds
  ")
  echo "$out" | grep -q "wrote-creds" || fail "fg: session did not write creds ($out)"
  echo "$out" | grep -q "securestorage=/home/agent/.enclave-auth" || fail "fg: securestorage env missing ($out)"
  got=$(read_store_file "$AUTH_STORE" .credentials.json)
  rlAssertEquals "foreground credentials synced" "$FOREGROUND_CREDENTIALS" "$got" || return 1
  gotgh=$(read_store_file "$GITHUB_AUTH_STORE" hosts.yml)
  rlAssertEquals "GitHub credentials synced" "$GITHUB_HOSTS" "$gotgh" || return 1
  rlPass "foreground run synced shared auth + gh feature auth on exit (auto-remove path)"
}

step_cross_project_auth() {
  # The onboarding marker may live in config.json (fresh store: written by the
  # OnAuthReady hook) or in .claude.json (steady state: the entrypoint links
  # config-dir auth files into the shared auth mount and claude reads
  # ~/.claude.json). Assert the functional outcome, not one specific file.
  out=$(run_tty "$OTHER_PROJECT" shell --allow-all-network --disable-gateway -- -c '
    cat ~/.claude/config.json 2>/dev/null
    cat ~/.claude.json 2>/dev/null
    echo ---
    cat ~/.enclave-auth/.credentials.json 2>/dev/null
  ')
  echo "$out" | grep -q "Credentials: Subscription/session is present" || fail "cross: shared session not detected ($out)"
  echo "$out" | grep -q hasCompletedOnboarding || fail "cross: onboarding marker missing ($out)"
  echo "$out" | grep -q '"expiresAt":300' || fail "cross: shared credential not visible in second project ($out)"
  rlPass "second project sees shared session; onboarding marker present"
}

step_background_stop_auth() {
  run_tty "$PROJECT" run --background --name smokebg --allow-all-network --disable-gateway >/dev/null
  local name="enclave-claude-${PROJECT_HASH}-smokebg"
  podman ps --format '{{.Names}}' | grep -qx "$name" || fail "bg: container $name not running"
  podman exec "$name" bash -c "printf '%s' '$BACKGROUND_CREDENTIALS' > ~/.claude/.credentials.json" || fail "bg: exec write failed"
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  podman ps -a --format '{{.Names}}' | grep -qx "$name" && fail "bg: container not removed by stop"
  got=$(read_store_file "$AUTH_STORE" .credentials.json)
  rlAssertEquals "background credentials synced on stop" "$BACKGROUND_CREDENTIALS" "$got" || return 1
  rlPass "background stop finalized auth (cred400 reconciled) and removed container"
}

step_exec_auth_sync() {
  run_tty "$PROJECT" run --background --name smokexec --allow-all-network --disable-gateway >/dev/null
  local name="enclave-claude-${PROJECT_HASH}-smokexec"
  podman ps --format '{{.Names}}' | grep -qx "$name" || fail "exec: container $name not running"
  run_tty "$PROJECT" exec --name smokexec -- bash -c "printf '%s' '$EXEC_CREDENTIALS' > ~/.claude/.credentials.json && echo exec-done" | grep -q exec-done || fail "exec: command failed"
  got=$(read_store_file "$AUTH_STORE" .credentials.json)
  rlAssertEquals "exec credentials synced immediately" "$EXEC_CREDENTIALS" "$got" || return 1
  # The exec'd command's exit status must survive the backend seam to the CLI.
  run_tty "$PROJECT" exec --name smokexec -- bash -c 'exit 7' >/dev/null
  code=$?
  rlAssertEquals "exec exit status preserved" 7 "$code" || return 1
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  rlPass "exec synced credentials immediately and preserved the command's exit status"
}

step_list_sessions() {
  run_tty "$PROJECT" run --background --name smokeps --allow-all-network --disable-gateway >/dev/null
  local name="enclave-claude-${PROJECT_HASH}-smokeps"
  "$CLI_BIN" ps 2>/dev/null | grep -q smokeps || fail "ps: session not listed"
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  rlPass "ps lists background session"
}

step_persistent_environment() {
  out=$(SMOKE_TOKEN=tokenA run_tty "$PROJECT" shell --allow-all-network --disable-gateway --pass-env SMOKE_TOKEN -- -c 'echo "token=$SMOKE_TOKEN"')
  echo "$out" | grep -q "token=tokenA" || fail "env: pass-env did not inject ($out)"
  rlAssertExists "$ENV_STORE/env" || return 1
  rlAssertGrep "SMOKE_TOKEN=tokenA" "$ENV_STORE/env" || return 1
  # New process without the host var: injected from the persisted env store.
  out=$(run_tty "$PROJECT" shell --allow-all-network --disable-gateway --pass-env SMOKE_TOKEN -- -c 'echo "token=$SMOKE_TOKEN"')
  echo "$out" | grep -q "token=tokenA" || fail "env: persisted token not re-injected ($out)"
  rlPass "persisted env survives across runs"
}

step_reset_auth() {
  out=$(run_tty "$PROJECT" shell --allow-all-network --disable-gateway --reset-auth -- -c '
    ls ~/.claude/.credentials.json 2>&1
    ls ~/.enclave-auth/.credentials.json 2>&1
  ')
  echo "$out" | grep -c "No such file" | grep -q 2 || fail "reset: auth files still present in session ($out)"
  rlAssertNotExists "$AUTH_STORE/.credentials.json" || return 1
  # Reset drops previously persisted values; the same run may legitimately
  # re-persist secrets that are live in the host environment (GH_TOKEN etc.),
  # so assert the old token is gone rather than the file being absent.
  if store_file_exists "$ENV_STORE" env; then
    read_store_file "$ENV_STORE" env | grep -q SMOKE_TOKEN && fail "reset: persisted SMOKE_TOKEN survived --reset-auth"
  fi
  rlPass "--reset-auth cleared config+shared auth files and dropped persisted values"
}

step_ephemeral() {
  # Stores are host directories, so an ephemeral session must not create
  # any Podman volume. Assert the volume set is unchanged across the run.
  before=$(podman volume ls -q | sort)
  out=$(run_tty "$PROJECT" shell --allow-all-network --disable-gateway --ephemeral -- -c 'echo eph-ok')
  echo "$out" | grep -q eph-ok || fail "ephemeral: run failed ($out)"
  sleep 2
  after=$(podman volume ls -q | sort)
  [ "$before" = "$after" ] || fail "ephemeral: volume set changed: $(diff <(echo "$before") <(echo "$after"))"
  rlPass "ephemeral session ran with a unique config store and created no Podman volumes"
}

step_restart_stopped() {
  run_tty "$PROJECT" run --background --name smokerestart --allow-all-network --disable-gateway >/dev/null
  local name="enclave-claude-${PROJECT_HASH}-smokerestart"
  podman stop -t 2 "$name" >/dev/null 2>&1 || fail "restart: podman stop failed"
  out=$(run_tty "$PROJECT" run --background --name smokerestart --allow-all-network --disable-gateway)
  echo "$out" | grep -q "Removing stopped container" || fail "restart: pre-start removal not logged ($out)"
  podman ps --format '{{.Names}}' | grep -qx "$name" || fail "restart: session not running again"
  "$CLI_BIN" stop "$name" >/dev/null 2>&1
  rlPass "stopped session container replaced on restart"
}

step_network_status() {
  out=$(cd "$PROJECT" && "$CLI_BIN" network status 2>&1)
  echo "$out" | grep -q "no running gateways matched" || fail "network: unexpected status output ($out)"
  rlPass "network status runs through the backend gateway manager"
}

step_restricted_network() {
  out=$(run_tty "$PROJECT" shell -- -c '
    echo -n allowed=; curl -s -o /dev/null -w "%{http_code}" --max-time 15 https://api.anthropic.com/ || echo -n curl-fail; echo
    echo -n blocked=; curl -s -o /dev/null -w "%{http_code}" --max-time 8 https://example.com/ || echo -n curl-fail; echo
  ')
  echo "$out" | grep -q "allowed=[2345]" || fail "restricted: allowlisted domain unreachable ($out)"
  echo "$out" | grep -Eq "blocked=(000|curl-fail)" || fail "restricted: blocked domain was reachable ($out)"
  rlPass "restricted networking enforced through the gateway"
}

# netlog_size — current byte size of project1's claude network log (0 if absent).
netlog_size() { wc -c 2>/dev/null < "$NETWORK_LOG" || echo 0; }

# curl_example <enclave args...> — curl https://example.com from a session in
# project1 and print "reached=<status>" or "reached=curl-fail".
curl_example() {
  run_tty "$PROJECT" shell "$@" -- -c '
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
  new=$(tail -c +$((before + 1)) "$NETWORK_LOG" 2>/dev/null)
  echo "$new" | grep '"type":"session"' -q || fail "unrestricted: no session marker written (new log lines: $new)"
  echo "$new" | grep '"domain":"example.com"' | grep -q '"rule":"unrestricted"' \
    || fail "unrestricted: no network event for example.com (new log lines: $new)"
  rlPass "unrestricted session reached a non-allowlisted domain and logged it"
}

step_disabled_gateway() {
  local before after
  before=$(netlog_size)
  out=$(curl_example --allow-all-network --disable-gateway)
  echo "$out" | grep -q "reached=[2345]" || fail "disabled gateway: domain unreachable ($out)"
  after=$(netlog_size)
  [ "$before" = "$after" ] || fail "disabled gateway: network log grew ($before -> $after bytes)"
  # Assumes the persisted network policy mode is restricted, like restricted_network.
  out=$(run_tty "$PROJECT" shell --disable-gateway -- -c 'echo ran-restricted')
  echo "$out" | grep -q ran-restricted && fail "disabled gateway: restricted run with --disable-gateway was accepted ($out)"
  echo "$out" | grep -q "requires unrestricted network mode" || fail "disabled gateway: unexpected restricted-mode error ($out)"
  rlPass "--disable-gateway runs without a gateway or log and is rejected in restricted mode"
}

step_unrestricted_hostname() {
  local args
  for args in "--allow-all-network" "--allow-all-network --disable-gateway"; do
    # shellcheck disable=SC2086
    out=$(run_tty "$PROJECT" shell $args -- -c 'echo "hostname=$(hostname)"')
    echo "$out" | grep -q "hostname=enclave-project1" || fail "hostname: $args session lost its hostname ($out)"
  done
  rlPass "unrestricted sessions keep their hostname with and without a gateway"
}

# Steps in one test share the temporary projects and authentication stores.
status=0
for step in "$@"; do
  rlPhaseStartTest "$step"
  if ! rlRun "( step_$step )" 0 "$step"; then
    status=1
  fi
  rlPhaseEnd
  [ "$status" -eq 0 ] || break
done
rlJournalPrintText
rlJournalEnd
exit "$status"
