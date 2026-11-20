#!/bin/bash
# Copyright (C) 2026 Codesweep and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Live smoke tests for gVisor sessions (--runtime runsc) on rootless podman:
# the session runs on gVisor's kernel, restricted egress gives the same results
# as under podman's default runtime, background sessions work with ps, exec and
# stop, --admin sessions get sudo while other sessions do not, unrestricted
# sessions keep their hostname with and without a gateway, and project files
# stay owned by the host user.
#
# Requires podman, runsc where podman finds it (see the gVisor section of
# docs/backends.md), and network access. Builds the tool and gateway images on
# first use and runs real containers.
#
# Usage: scripts/smoke-gvisor.sh [step...]   (no args = all steps)
# Environment: CLI_BIN (default bin/enclave), TOOL (default claude), SMOKE_ROOT
# (default ${TMPDIR:-/tmp}/enclave-gvisor-smoke-<uid>).
set -u

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
CLI_BIN=${CLI_BIN:-$REPO_ROOT/bin/enclave}
TOOL=${TOOL:-claude}
# Outside home: enclave refuses to mount hidden home directories. Stable across
# invocations, so steps run separately share the project.
SMOKE_ROOT=${SMOKE_ROOT:-${TMPDIR:-/tmp}/enclave-gvisor-smoke-$(id -u)}
PROJECT=$SMOKE_ROOT/project
DEFAULT_RUNTIME=(--backend podman --tool "$TOOL")
RUNSC=(--backend podman --runtime runsc --tool "$TOOL")

pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*"; exit 1; }

[ -x "$CLI_BIN" ] || fail "enclave binary not found at $CLI_BIN (run make build)"
command -v podman >/dev/null || fail "podman not found"
mkdir -p "$PROJECT"
echo gvisor-smoke > "$PROJECT/README.md"

# run_in <dir> <enclave args...> — run enclave without a terminal and capture
# its output; returns enclave's exit status.
run_in() {
  local dir=$1; shift
  (cd "$dir" && "$CLI_BIN" "$@" </dev/null 2>&1) | tr -d '\r'
  return "${PIPESTATUS[0]}"
}

HASH=$(cd "$PROJECT" && "$CLI_BIN" info "${DEFAULT_RUNTIME[@]}" 2>/dev/null | awk '/Project Hash:/ {print $3}')
[ -n "$HASH" ] || fail "could not resolve the project hash for $PROJECT"
NETLOG="${XDG_STATE_HOME:-$HOME/.local/state}/enclave/projects/$HASH/$TOOL/logs/network.log"

# netlog_size — current byte size of the project's network log (0 if absent).
netlog_size() { wc -c 2>/dev/null < "$NETLOG" || echo 0; }

step_kernel() {
  local out
  out=$(run_in "$PROJECT" shell "${RUNSC[@]}" -- -c 'echo "kernel=$(uname -r)"')
  echo "$out" | grep -q 'kernel=.*-gvisor' || fail "kernel: the runsc session does not run on gVisor ($out)"
  out=$(run_in "$PROJECT" shell "${DEFAULT_RUNTIME[@]}" -- -c 'echo "kernel=$(uname -r)"')
  echo "$out" | grep -q 'kernel=' || fail "kernel: the default-runtime session did not start ($out)"
  echo "$out" | grep -q 'kernel=.*-gvisor' && fail "kernel: the default-runtime session reports gVisor ($out)"
  pass "runsc sessions run on gVisor's kernel, default sessions do not"
}

# Each probe prints "probe <name>=open|closed|missing"; only allowed-domain may
# be open in a restricted session.
EGRESS_PROBES='
probe() {
  local name=$1; shift
  if ! command -v "$1" >/dev/null; then echo "probe $name=missing"; return; fi
  if "$@" >/dev/null 2>&1; then echo "probe $name=open"; else echo "probe $name=closed"; fi
}
probe allowed-domain curl -sS --max-time 15 -o /dev/null https://github.com/
probe other-domain curl -sS --max-time 8 -o /dev/null https://example.com/
probe direct-ip-http curl -sS --fail --max-time 8 -o /dev/null http://1.1.1.1/
probe direct-ip-https curl -sSk --fail --max-time 8 -o /dev/null https://1.1.1.1/
probe direct-ip-853 timeout 8 bash -c "</dev/tcp/1.1.1.1/853"
probe outside-dns-udp dig +time=3 +tries=1 @1.1.1.1 example.com
probe outside-dns-tcp dig +tcp +time=3 +tries=1 @1.1.1.1 example.com
probe ipv6 curl -6 -sS --max-time 8 -o /dev/null https://github.com/
'

egress_results() {
  run_in "$PROJECT" shell "$@" -- -c "$EGRESS_PROBES" | grep '^probe ' | sort
}

step_egress() {
  local default runsc before new i
  default=$(egress_results "${DEFAULT_RUNTIME[@]}")
  before=$(netlog_size)
  runsc=$(egress_results "${RUNSC[@]}")
  [ "$(echo "$runsc" | grep -c '^probe ')" = 8 ] || fail "egress: the runsc session did not report every probe ($runsc)"
  [ "$default" = "$runsc" ] || fail "egress: runsc differs from the default runtime: $(diff <(echo "$default") <(echo "$runsc") | grep '^[<>]' | tr '\n' ' ')"
  echo "$runsc" | grep -qx 'probe allowed-domain=open' || fail "egress: the allowlisted domain is unreachable ($runsc)"
  echo "$runsc" | grep -v 'allowed-domain' | grep -v '=closed$' | grep -q . && fail "egress: a blocked path is reachable or a probe tool is missing ($runsc)"
  # dnsmasq's log reaches the network log through the gateway's audit
  # translator, which can lag the session's exit.
  for i in 1 2 3 4 5; do
    new=$(tail -c +$((before + 1)) "$NETLOG" 2>/dev/null)
    echo "$new" | grep '"domain":"example.com"' | grep -q '"verdict":"deny"' && break
    [ "$i" = 5 ] && fail "egress: the network log has no denied example.com lookup (new log lines: $new)"
    sleep 1
  done
  pass "restricted egress under runsc matches the default runtime and is logged"
}

step_background() {
  local name="enclave-${TOOL}-${HASH}-gvisorbg" out code
  "$CLI_BIN" stop --backend podman "$name" >/dev/null 2>&1
  run_in "$PROJECT" run "${RUNSC[@]}" --background --name gvisorbg >/dev/null
  podman ps --format '{{.Names}}' | grep -qx "$name" || fail "background: container $name not running"
  [ "$(podman inspect --format '{{.OCIRuntime}}' "$name")" = runsc ] || fail "background: $name does not run under runsc"
  (cd "$PROJECT" && "$CLI_BIN" ps --backend podman --json --tool "$TOOL" --name gvisorbg) | tr -d ' \n' | grep -q '"runtime":"runsc"' \
    || fail "background: ps --json does not record runtime runsc"
  out=$(run_in "$PROJECT" exec --backend podman --tool "$TOOL" --name gvisorbg -- bash -c 'echo "kernel=$(uname -r)"')
  echo "$out" | grep -q 'kernel=.*-gvisor' || fail "background: exec did not reach the gVisor session ($out)"
  run_in "$PROJECT" exec --backend podman --tool "$TOOL" --name gvisorbg -- bash -c 'exit 7' >/dev/null
  code=$?
  [ "$code" = 7 ] || fail "background: exec exit status not preserved (got $code, want 7)"
  "$CLI_BIN" stop --backend podman "$name" >/dev/null 2>&1
  podman ps -a --format '{{.Names}}' | grep -qx "$name" && fail "background: container not removed by stop"
  pass "background runsc session works with ps --json, exec and stop"
}

# --admin grants sudo for apt and dpkg only; an admin shell runs its arguments
# as a command, so it takes "bash -c" where other shells take "-c".
step_admin() {
  local out
  out=$(run_in "$PROJECT" shell "${RUNSC[@]}" --admin -- bash -c 'if sudo -n apt-get --version >/dev/null 2>&1; then echo sudo=works; else echo sudo=denied; fi')
  echo "$out" | grep -q 'sudo=works' || fail "admin: sudo apt-get fails in a runsc --admin session ($out)"
  out=$(run_in "$PROJECT" shell "${RUNSC[@]}" -- -c 'if sudo -n apt-get --version >/dev/null 2>&1; then echo sudo=works; else echo sudo=denied; fi')
  echo "$out" | grep -q 'sudo=denied' || fail "admin: sudo works in a runsc session without --admin ($out)"
  pass "runsc --admin sessions get sudo apt-get, other runsc sessions do not"
}

step_unrestricted() {
  local args out
  for args in "--allow-all-network" "--allow-all-network --disable-gateway"; do
    # shellcheck disable=SC2086
    out=$(run_in "$PROJECT" shell "${RUNSC[@]}" $args -- -c '
      echo "kernel=$(uname -r)"
      echo "hostname=$(hostname)"
      echo -n reached=; curl -s -o /dev/null -w "%{http_code}" --max-time 15 https://example.com/ || echo -n curl-fail; echo
    ')
    echo "$out" | grep -q 'kernel=.*-gvisor' || fail "unrestricted: $args session does not run on gVisor ($out)"
    echo "$out" | grep -q 'hostname=enclave-project' || fail "unrestricted: $args session lost its hostname ($out)"
    echo "$out" | grep -q 'reached=[2345]' || fail "unrestricted: $args session cannot reach example.com ($out)"
  done
  pass "unrestricted runsc sessions reach any domain and keep their hostname, with and without a gateway"
}

step_mounts() {
  local out
  rm -f "$PROJECT/owner-check" "$PROJECT/readonly-check"
  out=$(run_in "$PROJECT" shell "${RUNSC[@]}" -- -c 'touch owner-check && echo wrote')
  echo "$out" | grep -q wrote || fail "mounts: cannot write to the project ($out)"
  [ "$(stat -c %u "$PROJECT/owner-check")" = "$(id -u)" ] || fail "mounts: a file created in the session is not owned by the host user"
  out=$(run_in "$PROJECT" shell "${RUNSC[@]}" --project-mount readonly -- -c 'if touch readonly-check 2>/dev/null; then echo project=writable; else echo project=readonly; fi')
  echo "$out" | grep -q 'project=readonly' || fail "mounts: a read-only project accepted a write ($out)"
  [ -e "$PROJECT/readonly-check" ] && fail "mounts: a write to the read-only project reached the host"
  pass "project files are owned by the host user, and read-only projects reject writes"
}

ALL="kernel egress background admin unrestricted mounts"
steps=${*:-$ALL}
for s in $steps; do
  echo "=== step: $s ==="
  "step_$s"
done
echo "ALL GVISOR SMOKE STEPS PASSED"
