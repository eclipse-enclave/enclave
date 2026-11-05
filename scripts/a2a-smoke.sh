#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Live smoke test of A2A peer groups on a real engine: run-path rejections,
# an orchestrator plus two workers, plain sessions unchanged, host send,
# Ctrl-C and cancel, orchestrator stop with departure acknowledgments, the
# degraded path with a late acknowledgment, reconcile after kill -9 of a
# joining process, foreground worker signals and teardown with inventory
# deletion.
#
# It uses the host's real Claude login as enclave normally mounts it. With
# A2A_SMOKE_REAL=1 (default) it runs four small real Claude Code tasks with
# ANTHROPIC_MODEL (default haiku); A2A_SMOKE_REAL=0 skips them. Every group,
# session, container and network it creates is named after A2A_SMOKE_PREFIX
# (default a2a-smoke), and cleanup touches nothing else. The workers and the
# orchestrator stop run with copies of your config root (projects excluded)
# that set termination and acknowledgment timeouts; your config root is only
# read.
#
# Usage: ENGINE=docker|podman scripts/a2a-smoke.sh [step...]   (no args = all)
# Steps: rejections plain send ctrlc_cancel orchestrator_stop kill9_reconcile
# foreground_signal. The group is joined before the first step that needs it
# and torn down at the end.
# Exit status: 0 when every check passed, 1 otherwise.
set -u

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
CLI_BIN=${CLI_BIN:-$REPO_ROOT/bin/enclave}
ENGINE=${ENGINE:-docker}
PREFIX=${A2A_SMOKE_PREFIX:-a2a-smoke}
REAL=${A2A_SMOKE_REAL:-1}
export ANTHROPIC_MODEL=${ANTHROPIC_MODEL:-haiku}
SMOKE_ROOT=${SMOKE_ROOT:-${TMPDIR:-/tmp}/enclave-a2a-smoke-$(id -u)}
PROJECT=$SMOKE_ROOT/project
STATE_ROOT=${XDG_STATE_HOME:-$HOME/.local/state}/enclave
CONFIG_ROOT=${XDG_CONFIG_HOME:-$HOME/.config}/enclave
G=$PREFIX
W1=$PREFIX-w1
W2=$PREFIX-w2
ORCH=$PREFIX-orch
REC=$STATE_ROOT/a2a/$G/group.json
LONG_TASK="Use the Bash tool to run the command sleep 300. When it finishes, reply with the word done."
# Leaves a SIGTERM-ignoring sleep outside Claude's process tree, so only the adapter's SIGKILL ends it.
STUBBORN_TASK="Use the Bash tool to run exactly this command: (trap '' TERM HUP; setsid sleep 300 >/dev/null 2>&1 &); sleep 300. When it finishes, reply with the word done."
# W1 confirms within ACK_TIMEOUT, W2 only after both attempts of the orchestrator stop.
FAST_TERMINATION=2s
SLOW_TERMINATION=60s
ACK_TIMEOUT=15s
PROJECT_HASH=
FAILS=0
REAL_TASKS=0

pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*"; FAILS=$((FAILS + 1)); }
note() { echo "  $*"; }
die() { echo "ABORT: $*"; exit 1; }
check() { # check <description> <command...>
  local desc=$1
  shift
  if "$@"; then pass "$desc"; else fail "$desc"; fi
}

now() { date +%s.%N; }
since() { echo "$(now) - $1" | bc; }

enclave() { (cd "$PROJECT" && exec "$CLI_BIN" "$@"); }

# enclave_bg <log> <args...>: enclave in the background; BG_PID is the enclave
# process itself, not a wrapper shell, so signals sent to it reach enclave.
enclave_bg() {
  local log=$1
  shift
  (cd "$PROJECT" && exec "$CLI_BIN" "$@") </dev/null >"$log" 2>&1 &
  BG_PID=$!
}

# start <name> <role> [args...]: a background member of the group.
start() {
  local name=$1 role=$2
  shift 2
  enclave run --backend "$ENGINE" --tool claude --peer-group "$G" --name "$name" --role "$role" \
    --pass-env ANTHROPIC_MODEL --background "$@" </dev/null >"$SMOKE_ROOT/start-$name.log" 2>&1
}

# container <member>: the session container of a member of the group.
container() {
  "$ENGINE" ps -a --filter "label=enclave.a2a.group=$G" --filter "label=enclave.a2a.member=$1" --format '{{.Names}}' | head -1
}

rec() { jq -r "$1" "$REC"; }
store_of() { # store_of <member>: the member's A2A store from the cleanup inventory.
  jq -r --arg m "$1" '.entries[] | select(.member == $m and .state == "member") | .storePath' \
    "$STATE_ROOT/a2a/$G/inventory/$(rec .groupGeneration).json" | tail -1
}
latest_task() { find "$(store_of "$1")/tasks" -name '*.json' -printf '%T@ %p\n' | sort -n | tail -1 | cut -d' ' -f2; }

# mcp <container> <seconds> <tool-call-json...>: drive the in-session MCP server over stdio,
# one call every <seconds>, and print each response's text content as one JSON line.
mcp() {
  local c=$1 pause=$2 id=2
  shift 2
  {
    printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"a2a-smoke","version":"0"}}}'
    printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
    for call in "$@"; do
      printf '{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":%s}\n' "$id" "$call"
      id=$((id + 1))
      sleep "$pause"
    done
  } | "$ENGINE" exec -i "$c" enclave-a2a-agent mcp 2>/dev/null |
    jq -c 'select(.id != null and .id > 1) | .result.content[0].text | fromjson? // .'
}

snapshot_engine() { # snapshot_engine <file>: names of this run's containers and networks
  { "$ENGINE" ps -a --format '{{.Names}}'; "$ENGINE" network ls --format '{{.Name}}'; } | grep -E -- "-$PREFIX(-|\$)" | sort >"$1"
}

cleanup() {
  echo "--- cleanup"
  if [ -e "$REC" ]; then
    enclave stop --backend "$ENGINE" --peer-group "$G" >/dev/null 2>&1
  fi
  local names
  names=$("$ENGINE" ps -a --format '{{.Names}}' | grep -E -- "-$PREFIX-|^enclave-a2a-hub-$PREFIX\$")
  [ -n "$names" ] && echo "$names" | xargs "$ENGINE" rm -f >/dev/null 2>&1
  names=$("$ENGINE" network ls --format '{{.Name}}' | grep -E -- "-$PREFIX-")
  [ -n "$names" ] && echo "$names" | xargs "$ENGINE" network rm >/dev/null 2>&1
  enclave a2a reconcile --backend "$ENGINE" --peer-group "$G" >/dev/null 2>&1
  # The project exists only for this run, so its per-project state goes too.
  if [ -n "$PROJECT_HASH" ]; then
    rm -rf "${STATE_ROOT:?}/projects/$PROJECT_HASH" "${CONFIG_ROOT:?}/projects/$PROJECT_HASH"
  fi
  rm -rf "${SMOKE_ROOT:?}"/xdg-*
  snapshot_engine "$SMOKE_ROOT/after"
  if diff -q "$SMOKE_ROOT/before" "$SMOKE_ROOT/after" >/dev/null; then
    pass "no container or network of this run is left"
  else
    fail "left behind: $(diff "$SMOKE_ROOT/before" "$SMOKE_ROOT/after" | grep '^[<>]' | tr '\n' ' ')"
  fi
  echo "real Claude tasks: $REAL_TASKS"
  echo "failures: $FAILS"
}

preflight() {
  [ -x "$CLI_BIN" ] || die "enclave binary not found at $CLI_BIN (run make build)"
  command -v "$ENGINE" >/dev/null || die "$ENGINE not found"
  command -v jq >/dev/null || die "jq not found"
  command -v bc >/dev/null || die "bc not found"
  [ -e "$REC" ] && die "peer group $G exists; tear it down first: enclave stop --peer-group $G"
  mkdir -p "$PROJECT"
  echo "a2a smoke project" >"$PROJECT/README.md"
  PROJECT_HASH=$(enclave info --tool claude 2>/dev/null | awk '/Project Hash:/ {print $3}')
  [ -n "$PROJECT_HASH" ] || die "could not resolve the project hash of $PROJECT"
  snapshot_engine "$SMOKE_ROOT/before"
  trap cleanup EXIT
}

step_rejections() {
  echo "--- run-path rejections"
  local args msg out
  while IFS='|' read -r args msg; do
    # shellcheck disable=SC2086
    out=$(enclave run --backend "$ENGINE" --tool claude $args </dev/null 2>&1)
    if [ $? = 1 ] && echo "$out" | grep -q -- "$msg"; then pass "rejected: $args"; else fail "not rejected: $args ($out)"; fi
  done <<EOF
--peer-group $G|requires --name
--peer-group $G --name $W1 --backend qemu|qemu backend
--peer-group $G --name $W1 --allow-all-network|incompatible with --allow-all-network
--peer-group $G --name $W1 --no-yolo|needs yolo mode
--role worker|requires --peer-group
EOF
  check "no container or group state after rejections" test -z "$("$ENGINE" ps -a --format '{{.Names}}' | grep -- "-$PREFIX-")" -a ! -e "$REC"
}

# smoke_config <name> <json object>: a copy of the config root (projects excluded) with the object
# merged into config.json, at $SMOKE_ROOT/xdg-<name> for use as XDG_CONFIG_HOME.
smoke_config() {
  local dir=$SMOKE_ROOT/xdg-$1/enclave entry
  rm -rf "$SMOKE_ROOT/xdg-$1"
  mkdir -p "$dir"
  for entry in "$CONFIG_ROOT"/* "$CONFIG_ROOT"/.[!.]*; do
    [ -e "$entry" ] && [ "$(basename "$entry")" != projects ] && cp -rL "$entry" "$dir/"
  done
  [ -f "$dir/config.json" ] || echo '{}' >"$dir/config.json"
  jq --argjson add "$2" '. + $add' "$dir/config.json" >"$dir/config.json.new" && mv "$dir/config.json.new" "$dir/config.json"
}

step_join() {
  echo "--- join an orchestrator and two workers"
  local t
  smoke_config fast "{\"a2a_termination_timeout\": \"$FAST_TERMINATION\", \"a2a_departure_ack_timeout\": \"$ACK_TIMEOUT\"}"
  smoke_config slow "{\"a2a_termination_timeout\": \"$SLOW_TERMINATION\"}"
  t=$(now)
  if XDG_CONFIG_HOME=$SMOKE_ROOT/xdg-fast start "$W1" worker; then
    pass "worker $W1 starts ($FAST_TERMINATION termination timeout)"
  else
    fail "worker $W1 starts ($FAST_TERMINATION termination timeout)"
  fi
  note "first join (includes image checks): $(since "$t") s"
  t=$(now)
  if XDG_CONFIG_HOME=$SMOKE_ROOT/xdg-slow start "$W2" worker --project-mount readonly --a2a-description "slow-terminating worker"; then
    pass "worker $W2 starts (read-only project, $SLOW_TERMINATION termination timeout)"
  else
    fail "worker $W2 starts (read-only project, $SLOW_TERMINATION termination timeout)"
  fi
  note "second join: $(since "$t") s"
  check "orchestrator $ORCH starts" start "$ORCH" orchestrator --project-mount readonly
  [ -e "$REC" ] || die "no group record after the joins (see $SMOKE_ROOT/start-*.log)"

  local peers
  peers=$(enclave a2a peers --peer-group "$G" --json)
  check "peers --json: kind a2a-peers, three active members" \
    test "$(echo "$peers" | jq -r '[.schemaVersion, .kind, ([.members[] | select(.state == "active")] | length)] | join(" ")')" = "1 a2a-peers 3"
  local ps
  ps=$(enclave ps --backend "$ENGINE" --json | jq -c --arg g "$G" '[.[] | select(.a2a.group == $g) | .a2a]')
  check "ps --json: a2a objects for all members" test "$(echo "$ps" | jq length)" = 3
  check "ps --json: workers report port 41821, the orchestrator null" \
    test "$(echo "$ps" | jq -r '[.[] | "\(.role)=\(.port)"] | sort | join(" ")')" = "orchestrator=null worker=41821 worker=41821"
  check "ps --json: hub, revision and tri-states" \
    test "$(echo "$ps" | jq -r --arg h "enclave-a2a-hub-$G" '[.[] | (.hub.container == $h) and (.snapshotRevision > 0) and (.quarantine == "no") and (.departure == "no")] | all')" = true

  local cw1 cw2 corch
  cw1=$(container "$W1")
  cw2=$(container "$W2")
  corch=$(container "$ORCH")
  check "worker runs the adapter in serve mode" "$ENGINE" exec "$cw1" sh -c 'ps -eo args | grep -q "^enclave-a2a-agent serve"'
  check "worker uses the A2A image variant" bash -c "'$ENGINE' inspect -f '{{.Config.Image}}' '$cw1' | grep -q ':a2a-'"
  check "workers got their termination timeouts" \
    test "$("$ENGINE" exec "$cw1" printenv ENCLAVE_A2A_TERMINATION_TIMEOUT) $("$ENGINE" exec "$cw2" printenv ENCLAVE_A2A_TERMINATION_TIMEOUT)" = "2s 1m0s"
  for _ in $(seq 1 20); do
    "$ENGINE" exec "$corch" sh -c 'ps -eo args | grep -q "^enclave-a2a-agent mcp"' && break
    sleep 0.5
  done
  check "orchestrator Claude runs with the enclave-a2a MCP config" "$ENGINE" exec "$corch" sh -c 'ps -eo args | grep -q "^claude .*--mcp-config"'
  check "orchestrator Claude started the enclave-a2a MCP server" "$ENGINE" exec "$corch" sh -c 'ps -eo args | grep -q "^enclave-a2a-agent mcp"'
  check "orchestrator MCP server lists exactly the four tools" \
    test "$({
      printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"a2a-smoke","version":"0"}}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
      sleep 2
    } | "$ENGINE" exec -i "$corch" enclave-a2a-agent mcp 2>/dev/null | jq -r 'select(.id == 2) | [.result.tools[].name] | sort | join(",")')" = "cancel_task,get_task,list_peers,send_task"
  check "orchestrator and worker get the a2a-provenance skill" \
    "$ENGINE" exec "$corch" test -d /home/agent/.claude/skills/a2a-provenance -a -n "$("$ENGINE" exec "$cw1" ls /home/agent/.claude/skills/a2a-provenance 2>/dev/null)"
}

step_plain() {
  echo "--- a plain session next to the group"
  local name=$PREFIX-plain c hubs
  hubs=$("$ENGINE" ps -q --filter label=enclave.a2a.hub=true | wc -l)
  check "plain session starts" bash -c "cd '$PROJECT' && '$CLI_BIN' run --backend '$ENGINE' --tool claude --name '$name' --background </dev/null >'$SMOKE_ROOT/start-$name.log' 2>&1"
  c=$("$ENGINE" ps -a --filter "name=-$name\$" --format '{{.Names}}' | head -1)
  check "plain: no new hub" test "$("$ENGINE" ps -q --filter label=enclave.a2a.hub=true | wc -l)" = "$hubs"
  check "plain: no a2a labels" test "$("$ENGINE" inspect -f '{{json .Config.Labels}}' "$c" | jq '[keys[] | select(startswith("enclave.a2a"))] | length')" = 0
  check "plain: no ENCLAVE_A2A_* env" test "$("$ENGINE" exec "$c" env | grep -c '^ENCLAVE_A2A_')" = 0
  check "plain: no enclave-a2a-agent binary" test -z "$("$ENGINE" exec "$c" sh -c 'command -v enclave-a2a-agent')"
  check "plain: no enclave-a2a MCP server" bash -c "! '$ENGINE' exec -w /tmp '$c' claude mcp list 2>&1 | grep -q enclave-a2a"
  check "plain: no a2a-* skill" test "$("$ENGINE" exec "$c" sh -c 'ls -d /home/agent/.claude/skills/a2a-* 2>/dev/null | wc -l')" = 0
  check "plain: ps --json a2a is null" test "$(enclave ps --backend "$ENGINE" --json | jq -c --arg c "$c" '.[] | select(.name == $c) | .a2a')" = null
  local t
  t=$(now)
  check "plain session stops" bash -c "cd '$PROJECT' && '$CLI_BIN' stop --backend '$ENGINE' --name '$name' >/dev/null 2>&1"
  note "plain stop with a group present: $(since "$t") s"
}

step_send() {
  echo "--- host send"
  local out t
  t=$(now)
  out=$(enclave a2a send --backend "$ENGINE" --peer-group "$G" --json "$W1" "Reply with exactly the word pong and nothing else. Do not use tools.")
  local rc=$?
  REAL_TASKS=$((REAL_TASKS + 1))
  note "blocking send round trip: $(since "$t") s"
  check "send exits 0 with a completed task" test "$rc/$(echo "$out" | jq -r '"\(.kind) \(.state) \(.termination)"')" = "0/a2a-task TASK_STATE_COMPLETED confirmed"
  check "send returns the worker's answer" bash -c "echo '$out' | jq -r .message | grep -qi pong"
}

step_ctrlc_cancel() {
  echo "--- Ctrl-C during a blocking send, hub call close, task get and cancel"
  local hub=enclave-a2a-hub-$G rev0 pid child out task t n
  rev0=$(rec .committedRevision)
  enclave_bg "$SMOKE_ROOT/ctrlc.log" a2a send --backend "$ENGINE" --peer-group "$G" "$W1" "$LONG_TASK"
  pid=$BG_PID
  REAL_TASKS=$((REAL_TASKS + 1))
  sleep 6
  check "the host call runs in the hub as uid 65532" \
    bash -c "'$ENGINE' exec '$hub' ps -o user,args | grep '[e]nclave-a2a-hub call' | grep -qE '^ *(65532|nonroot)'"
  child=$(pgrep -P "$pid" -f "$ENGINE exec")
  if [ -n "$child" ]; then
    kill -9 "$child"
    t=$(now)
    for _ in $(seq 1 30); do
      n=$("$ENGINE" exec "$hub" ps -o args | grep -c '[e]nclave-a2a-hub call')
      [ "$n" -le 1 ] && break
      sleep 1
    done
    note "after killing the engine exec: $n call process(es) in the hub after $(since "$t") s (the client resubscribes with a new one)"
  fi
  kill -INT "$pid"
  wait "$pid"
  check "interrupted send exits 130" test $? = 130
  sleep 1
  check "the send's credential was issued and revoked (two revisions)" test "$(rec .committedRevision)" -ge $((rev0 + 2))
  task=$(latest_task "$W1")
  check "the task keeps running after Ctrl-C" test "$(jq -r .state "$task")" = TASK_STATE_WORKING
  local handle
  handle=$(grep -o 'task:[^ ]*' "$SMOKE_ROOT/ctrlc.log" | head -1)
  out=$(enclave a2a task get --backend "$ENGINE" --peer-group "$G" --json "$handle")
  check "task get reports the running task" test "$(echo "$out" | jq -r .state)" = TASK_STATE_WORKING || note "task get: $out"
  t=$(now)
  out=$(enclave a2a task cancel --backend "$ENGINE" --peer-group "$G" --json "$handle")
  local rc=$?
  note "cancel: $(since "$t") s"
  check "task cancel exits 0 with a canceled task" test "$rc/$(echo "$out" | jq -r .state)" = "0/TASK_STATE_CANCELED" || note "task cancel: $out"
  check "the worker canceled the task with confirmed termination" \
    test "$(jq -r '"\(.state) \(.termination)"' "$task")" = "TASK_STATE_CANCELED confirmed"
  check "no harness process is left on the worker" \
    test "$("$ENGINE" exec "$(container "$W1")" ps -eo args | grep -c '^claude -p')" = 0
}

# Degraded path: W2 confirms termination only after SLOW_TERMINATION, after the stop gave up on
# its acknowledgment; the next reconcile completes the departure.
step_orchestrator_stop() {
  echo "--- orchestrator stop: acknowledgments, degraded path, reconcile"
  local corch out rc t task2
  corch=$(container "$ORCH")
  out=$(mcp "$corch" 6 \
    "{\"name\":\"send_task\",\"arguments\":{\"peer\":\"$W1\",\"text\":\"$LONG_TASK\",\"wait_seconds\":4}}" \
    "{\"name\":\"send_task\",\"arguments\":{\"peer\":\"$W2\",\"text\":\"$STUBBORN_TASK\",\"wait_seconds\":4}}" \
    "{\"name\":\"send_task\",\"arguments\":{\"peer\":\"$W1\",\"text\":\"Reply queued.\",\"wait_seconds\":1}}")
  REAL_TASKS=$((REAL_TASKS + 2))
  check "MCP send_task returns handles: two working, one queued" \
    test "$(echo "$out" | jq -r .state | tr '\n' ' ')" = "TASK_STATE_WORKING TASK_STATE_WORKING TASK_STATE_SUBMITTED "
  t=$(now)
  XDG_CONFIG_HOME=$SMOKE_ROOT/xdg-fast enclave stop --backend "$ENGINE" --name "$ORCH" >"$SMOKE_ROOT/orch-stop.log" 2>&1
  rc=$?
  note "orchestrator stop: $(since "$t") s, exit $rc"
  check "orchestrator stop reports the degraded departure (exit 3)" test "$rc" = 3
  local dep
  dep=$(jq -c --arg m "$ORCH" '[.departures[] | select(.member == $m)] | last' "$REC")
  check "departure degraded with an open acknowledgment obligation" \
    test "$(echo "$dep" | jq -r .state)/$(jq -r --arg m "$ORCH" '[.obligations[] | select(.kind == "departure-ack" and .member == $m)] | length' "$REC")" = degraded/1
  check "$W1 acknowledged: running terminated and queued discarded, confirmed" \
    test "$(echo "$dep" | jq -r --arg w "$W1" '.acks[] | select(.worker == $w) | "\(.admissionStopPersisted) \(.runningTerminated | length) \(.queuedDiscarded | length) \(.termination)"')" = "true 1 1 confirmed"

  task2=$(latest_task "$W2")
  for _ in $(seq 1 120); do
    [ "$(jq -r .state "$task2")" = TASK_STATE_WORKING ] || break
    sleep 1
  done
  note "$W2's task ended $(since "$t") s after the stop began"
  check "$W2's task records the confirmed termination" \
    test "$(jq -r '"\(.state) \(.reason) \(.termination)"' "$task2")" = "TASK_STATE_FAILED orchestrator-lost confirmed"
  check "reconcile exits 0" bash -c "cd '$PROJECT' && '$CLI_BIN' a2a reconcile --backend '$ENGINE' --peer-group '$G' >'$SMOKE_ROOT/orch-reconcile.log' 2>&1"
  dep=$(jq -c --arg m "$ORCH" '[.departures[] | select(.member == $m)] | last' "$REC")
  check "departure complete after reconcile" test "$(echo "$dep" | jq -r .state)" = complete
  check "no open obligation is left" test "$(rec '.obligations | length')" = 0
  check "$W2 is not quarantined" \
    test "$(enclave ps --backend "$ENGINE" --json | jq -r --arg c "$(container "$W2")" '.[] | select(.name == $c) | .a2a.quarantine')" = no
  check "the workers keep running" test "$(rec '[.members[] | select(.role == "worker")] | length')" = 2
}

step_kill9_reconcile() {
  echo "--- kill -9 of a joining enclave process, then reconcile"
  local name=$PREFIX-k1 pid out
  enclave_bg "$SMOKE_ROOT/k1.log" run --backend "$ENGINE" --tool claude --peer-group "$G" --name "$name" --project-mount readonly
  pid=$BG_PID
  for _ in $(seq 1 600); do
    jq -e --arg m "$name" '.members[] | select(.name == $m)' "$REC" >/dev/null 2>&1 && break
    sleep 0.05
  done
  kill -9 "$pid"
  wait "$pid" 2>/dev/null
  note "killed with the member in state $(jq -r --arg m "$name" '.members[] | select(.name == $m) | .state' "$REC"), pending revision $(rec .pendingRevision.revision)"
  out=$(enclave a2a reconcile --backend "$ENGINE" --peer-group "$G" 2>&1)
  check "reconcile removes the unfinished join" bash -c "echo '$out' | grep -q 'removed member $name'"
  check "the member is gone from the record" test "$(jq --arg m "$name" '[.members[] | select(.name == $m)] | length' "$REC")" = 0
  check "the hub is not attached to its network" \
    bash -c "! '$ENGINE' inspect -f '{{json .NetworkSettings.Networks}}' 'enclave-a2a-hub-$G' | grep -q -- '-$name-net'"
  check "no pending revision is left" test "$(rec .pendingRevision)" = null
}

step_foreground_signal() {
  echo "--- foreground worker without a TTY: SIGTERM to the enclave process"
  local name=$PREFIX-f1 pid child t
  enclave_bg "$SMOKE_ROOT/f1.log" run --backend "$ENGINE" --tool claude --peer-group "$G" --name "$name" --project-mount readonly
  pid=$BG_PID
  for _ in $(seq 1 120); do grep -q 'hub epoch' "$SMOKE_ROOT/f1.log" && break; sleep 0.5; done
  t=$(now)
  kill -TERM "$pid"
  for _ in $(seq 1 15); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
  if kill -0 "$pid" 2>/dev/null; then
    fail "SIGTERM to the enclave process of a foreground worker did not end the session within 15 s"
    child=$(pgrep -P "$pid" -f "$ENGINE run")
    t=$(now)
    [ -n "$child" ] && kill -TERM "$child"
  fi
  wait "$pid"
  note "exit $? $(since "$t") s after the signal that ended it"
  check "the foreground worker left the group" test "$(jq --arg m "$name" '[.members[] | select(.name == $m)] | length' "$REC")" = 0
}

step_teardown() {
  echo "--- teardown"
  local stores t
  stores=$(jq -r '.entries[].storePath' "$STATE_ROOT/a2a/$G/inventory/$(rec .groupGeneration).json" | sort -u)
  t=$(now)
  check "stop --peer-group exits 0" bash -c "cd '$PROJECT' && '$CLI_BIN' stop --backend '$ENGINE' --peer-group '$G' >'$SMOKE_ROOT/teardown.log' 2>&1"
  note "teardown: $(since "$t") s"
  check "no member container or hub is left" test -z "$("$ENGINE" ps -a --format '{{.Names}}' | grep -E -- "-($W1|$W2|$ORCH)\$|^enclave-a2a-hub-$G\$")"
  check "the group directory is gone" test ! -e "$STATE_ROOT/a2a/$G"
  local left=0 s
  for s in $stores; do [ -e "$s" ] && left=$((left + 1)); done
  check "every inventory store was deleted" test "$left" = 0
}

REAL_STEPS=" send ctrlc_cancel orchestrator_stop "
STEPS=${*:-rejections plain send ctrlc_cancel orchestrator_stop kill9_reconcile foreground_signal}

for step in $STEPS; do
  declare -F "step_$step" >/dev/null || die "unknown step: $step"
done
preflight
for step in $STEPS; do
  if [ "$REAL" != 1 ] && [ "${REAL_STEPS#* "$step" }" != "$REAL_STEPS" ]; then
    echo "--- skipped $step (A2A_SMOKE_REAL=0)"
    continue
  fi
  [ "$step" = rejections ] || [ -e "$REC" ] || step_join
  "step_$step"
done
[ -e "$REC" ] && step_teardown
[ "$FAILS" = 0 ]
