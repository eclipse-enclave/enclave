#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Real, billed mixed-harness review. All config/state writes are isolated;
# only copies of the host's shared auth stores and secrets are used.
# ENGINE=docker|podman A2A_SMOKE_ORCHESTRATOR_TOOL=claude|codex scripts/a2a-mixed-smoke.sh
# CLI_BIN overrides the binary; A2A_SMOKE_ROOT must be a NEW directory.
# Logs remain in the printed scratch directory; copied credentials are removed.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
CLI_BIN=${CLI_BIN:-$REPO_ROOT/bin/enclave}
ENGINE=${ENGINE:-docker}
LEAD_TOOL=${A2A_SMOKE_ORCHESTRATOR_TOOL:-claude}
case "$ENGINE" in docker|podman) ;; *) echo "Unsupported engine: $ENGINE" >&2; exit 2 ;; esac
case "$LEAD_TOOL" in claude|codex) ;; *) echo "Orchestrator must be claude or codex" >&2; exit 2 ;; esac
for dep in "$ENGINE" jq timeout git script; do command -v "$dep" >/dev/null; done
test -x "$CLI_BIN"

HOST_STATE=${XDG_STATE_HOME:-$HOME/.local/state}/enclave
ROOT=${A2A_SMOKE_ROOT:-$(mktemp -d /tmp/enclave-a2a-mixed.XXXXXX)}
if [ -n "${A2A_SMOKE_ROOT:-}" ]; then mkdir -m 700 "$ROOT"; fi
ROOT=$(cd "$ROOT" && pwd)
PREFIX=a2a-mixed-$(basename "$ROOT" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9' | tail -c 12)
export XDG_CONFIG_HOME=$ROOT/config XDG_STATE_HOME=$ROOT/state XDG_CACHE_HOME=$ROOT/cache
export ANTHROPIC_MODEL=${ANTHROPIC_MODEL:-haiku}
mkdir -p "$XDG_CONFIG_HOME/enclave" "$XDG_STATE_HOME/enclave/tools" "$ROOT/project"
printf '%s\n' '{"tool":"claude"}' >"$XDG_CONFIG_HOME/enclave/config.json"
enclave() { (cd "$ROOT/project" && "$CLI_BIN" "$@"); }
cleanup() {
  local status=$? teardown_ok=1 path
  trap - EXIT
  if ! enclave stop --backend "$ENGINE" --peer-group "$PREFIX" >"$ROOT/cleanup.log" 2>&1; then
    echo "Teardown failed; see $ROOT/cleanup.log" >&2
    status=1
    teardown_ok=0
  fi
  if [ -n "$("$ENGINE" ps -aq --filter "label=enclave.a2a.group=$PREFIX")" ]; then
    echo "Containers remain for $PREFIX (see cleanup.log)" >&2
    status=1
    teardown_ok=0
  fi
  if [ "$teardown_ok" = 1 ]; then
    if [ -f "$ROOT/transcripts.txt" ]; then
      while IFS= read -r path; do
        if [ -e "$path" ] || [ -L "$path" ]; then
          echo "Transcript inventory not deleted: $path" >&2
          status=1
        fi
      done <"$ROOT/transcripts.txt"
    fi
    if [ -e "$XDG_STATE_HOME/enclave/a2a/$PREFIX" ]; then
      echo "Group state remains after teardown" >&2
      status=1
    fi
    rm -rf "$ROOT/state" "$ROOT/config" "$ROOT/cache"
  else
    # Preserve lifecycle records needed for a scoped stop/reconcile retry,
    # while removing copied upstream credentials even if teardown failed.
    rm -rf "$ROOT/state/enclave/tools" "$ROOT/state/enclave/secrets" "$ROOT/config" "$ROOT/cache"
    for path in "$ROOT/state/enclave/projects/"*/claude/config-store/*/.credentials.json \
      "$ROOT/state/enclave/projects/"*/claude/config-store/*/.claude.json \
      "$ROOT/state/enclave/projects/"*/codex/config-store/*/auth.json; do
      rm -f "$path"
    done
    echo "Lifecycle state retained at $ROOT/state for scoped cleanup of $PREFIX" >&2
  fi
  echo "Mixed smoke exit $status; logs: $ROOT"
  exit "$status"
}
trap cleanup EXIT
for tool in claude codex; do
  if [ -d "$HOST_STATE/tools/$tool/auth" ]; then
    mkdir -p "$XDG_STATE_HOME/enclave/tools/$tool"
    cp -rL "$HOST_STATE/tools/$tool/auth" "$XDG_STATE_HOME/enclave/tools/$tool/"
  fi
done
if [ -d "$HOST_STATE/secrets" ]; then cp -rL "$HOST_STATE/secrets" "$XDG_STATE_HOME/enclave/"; fi
echo "Mixed smoke: $ENGINE, $LEAD_TOOL orchestrator, group $PREFIX; logs: $ROOT"

# Deliberately buggy review fixture, never installed or executed.
cat >"$ROOT/project/review.py" <<'PY'
from pathlib import Path

def read_document(root, user_path):
    candidate = Path(root) / user_path
    if str(candidate).startswith(str(root)):
        return candidate.read_text()
    raise ValueError("outside root")
PY
git -C "$ROOT/project" init -q

for tool in claude codex; do
  enclave run --backend "$ENGINE" --tool "$tool" --slim --no-memory \
    --peer-group "$PREFIX" --name "$PREFIX-$tool" --project-mount readonly \
    --a2a-description "Adversarial $tool reviewer: bugs, edge cases, missing tests" \
    --pass-env ANTHROPIC_MODEL --background >"$ROOT/start-$tool.log" 2>&1
done

brief="Review review.py for concrete correctness and security bugs. Do not edit files. Keep the answer under 200 words with exact counterexamples and missing tests."
prompt="You are the lead of a two-reviewer A2A review. Use only the enclave-a2a MCP tools and local read-only tools. First list_peers. Send EXACTLY this brief to BOTH $PREFIX-claude and $PREFIX-codex: $brief
Wait using get_task until BOTH complete, retaining each returned context and handle. Send each reviewer's findings to the OTHER reviewer using send_task with that reviewer's ORIGINAL context, asking for a short rebuttal, then wait for both replies. Do not confuse harness task IDs with A2A task: handles. Independently inspect review.py and verify the surviving findings using counterexamples. Return ONE ranked list with concrete tests, followed by A2A_MIXED_REVIEW_OK. Never edit files, start new agents, or run long commands."

args=()
if [ "$LEAD_TOOL" = claude ]; then
  args=(-p "$prompt" --output-format stream-json --verbose)
else
  args=(exec --json --skip-git-repo-check "$prompt")
fi
# A bounded foreground lead exercises the real runtime's orchestratorArgs,
# MCP discovery, sends, continuation contexts and departure, not a host shim.
printf -v lead_command '%q ' "$CLI_BIN" run \
  --backend "$ENGINE" --tool "$LEAD_TOOL" --slim --no-memory \
  --peer-group "$PREFIX" --name "$PREFIX-lead" --role orchestrator \
  --project-mount readonly --pass-env ANTHROPIC_MODEL -- "${args[@]}"
# Orchestrator sessions deliberately require a terminal. A PTY also makes
# this test runnable from CI or a redirected shell, without changing runtime.
(cd "$ROOT/project" && SHELL=/bin/bash timeout --signal=INT --kill-after=30s 600s \
  script -qefc "$lead_command" /dev/null) \
  >"$ROOT/lead.jsonl" 2>"$ROOT/lead.log"

# Inspect authoritative adapter records, not the lead's assertions. Each
# reviewer must have at least two completed tasks from the same lead/context.
for tool in claude codex; do
  record=$XDG_STATE_HOME/enclave/a2a/$PREFIX/group.json
  generation=$(jq -r .groupGeneration "$record")
  store=$(jq -r --arg m "$PREFIX-$tool" '.entries[] | select(.member == $m) | .storePath' \
    "$XDG_STATE_HOME/enclave/a2a/$PREFIX/inventory/$generation.json")
  jq -es --arg lead "$PREFIX-lead" '
    map(select(.principal.member == $lead and .state == "TASK_STATE_COMPLETED" and .termination == "confirmed"))
    | group_by(.contextId) | any(length >= 2)
  ' "$store"/tasks/*.json >/dev/null
  cp "$store/contexts.json" "$ROOT/$tool-contexts.json"
  config_store=$(dirname "$(dirname "$store")")
  jq -r --arg root "$config_store" '.contexts[].transcripts[] | $root + "/" + .' \
    "$store/contexts.json" >>"$ROOT/transcripts.txt"
  jq -s 'sort_by(.seq) | map({taskId, contextId, contentHash, state, termination})' \
    "$store"/tasks/*.json >"$ROOT/$tool-tasks.json"
done
test "$(jq -r '.[0].contentHash' "$ROOT/claude-tasks.json")" = \
  "$(jq -r '.[0].contentHash' "$ROOT/codex-tasks.json")"
jq -Rrs --arg tool "$LEAD_TOOL" '
  [split("\n")[] | fromjson? |
    if $tool == "claude" then select(.type == "result" and .subtype == "success") | .result
    else select(.type == "item.completed" and .item.type == "agent_message") | .item.text end]
  | last // ""
' "$ROOT/lead.jsonl" >"$ROOT/final.txt"
grep -q A2A_MIXED_REVIEW_OK "$ROOT/final.txt"
echo "PASS: Claude + Codex reviews and rebuttals in retained contexts, $LEAD_TOOL synthesis"
