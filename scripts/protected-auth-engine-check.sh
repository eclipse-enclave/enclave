#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Engine checks for --protect-auth that need no provider account: admission,
# mounts, handle-only views, release refusals, reload, exec, scopes, lifecycle
# cleanup, ownership and continuity, against a real Docker or Podman engine.
# A synthetic grant stands in for a captured login; it never reaches a
# provider successfully. Real login and refresh remain a human check.
#
# Runs against disposable XDG roots and a disposable project, so existing
# Enclave data is untouched. Builds the Claude image on first use.
#
# Usage: scripts/protected-auth-engine-check.sh [--engine docker|podman] [--report FILE] [check...]
set -u

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLI_BIN=${CLI_BIN:-$REPO_ROOT/bin/enclave}
SYNTHETIC_ACCESS="synthetic-engine-check-access-0123456789"
SYNTHETIC_REFRESH="synthetic-engine-check-refresh-0123456789"
GH_PIN=/opt/enclave/gh-2.87.3/bin/gh
ALL_CHECKS="enroll refuse-unprotected handles-only release-refusals mounts exec-pin reload concurrency stop-cleanup continuity project-scope ephemeral reset-downgrade"

sha256_hex() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum | cut -d' ' -f1
    else
        shasum -a 256 | cut -d' ' -f1
    fi
}

# synthetic_grant <identity-dir> <unix-now> prints a ready grant that the
# gateway accepts for that identity. It is not a valid provider credential.
synthetic_grant() {
    local dir=$1 now=$2 expires=3600 identity committed
    identity=$(printf '%s' "$dir" | sha256_hex)
    committed=$(date -u -d "@$now" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$now" +%Y-%m-%dT%H:%M:%SZ)
    printf '{"schema":1,"identity":"%s","issuer":"https://platform.claude.com","generation":1,"status":"ready","committedAt":"%s","expiresAt":%d,"token":{"access_token":"%s","refresh_token":"%s","token_type":"Bearer","expires_in":%d,"scope":"user:inference user:profile","account":{"uuid":"11111111-1111-4111-8111-111111111111","email_address":"engine-check@example.test"},"organization":{"uuid":"22222222-2222-4222-8222-222222222222"}}}\n' \
        "$identity" "$committed" "$((now + expires))" "$SYNTHETIC_ACCESS" "$SYNTHETIC_REFRESH" "$expires"
}

RESULTS=()
FAILED=0

record() {
    local status=$1 name=$2 detail=$3
    RESULTS+=("| $name | $status | $detail |")
    echo "$status: $name — $detail"
    if [ "$status" = FAIL ]; then
        FAILED=1
    fi
}

# enclave_in <dir> <args...> runs the CLI under a pty without inherited
# provider credentials, which protected sessions refuse.
enclave_in() {
    local dir=$1 cmd
    shift
    cmd=$(printf '%q ' "$CLI_BIN" --tool claude --backend "$ENGINE" "$@")
    (cd "$dir" && env -u ANTHROPIC_API_KEY -u ANTHROPIC_AUTH_TOKEN -u CLAUDE_CODE_OAUTH_TOKEN \
        -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN -u GITHUB_ENTERPRISE_TOKEN -u GH_HOST -u GH_CONFIG_DIR \
        script -qec "$cmd" /dev/null 2>&1)
}

protected_shell() {
    local dir=$1 script=$2
    shift 2
    enclave_in "$dir" shell --protect-auth --allow-all-network "$@" -- -c "$script"
}

identity_dir() { echo "$STATE/tools/claude/auth/$1"; }
identity_meta() {
    local hash
    hash=$(printf '%s' "$1" | sha256_hex)
    echo "$STATE/locks/store-$hash.identity.json"
}
container_name() { echo "enclave-claude-$PROJECT_HASH-$1"; }

seed_grant() {
    local dir
    dir=$(identity_dir "$1")
    [ -d "$dir" ] || return 1
    (umask 077 && synthetic_grant "$dir" "$(date +%s)" > "$dir/oauth-grant.json")
}

# Checks run in the listed order; these let a single check run on its own.
ensure_grant() {
    if [ ! -f "$(identity_meta "$(identity_dir check-a)")" ]; then
        protected_shell "$PROJECT" true --auth-scope shared --auth-name check-a >/dev/null
    fi
    [ -f "$(identity_dir check-a)/oauth-grant.json" ] || seed_grant check-a
}

ensure_background() {
    ensure_grant
    "$ENGINE" ps --format '{{.Names}}' | grep -qx "$(container_name "$1")" || start_background "$1"
}

check_enroll() {
    local out
    # shellcheck disable=SC2016 # Expanded inside the session.
    out=$(protected_shell "$PROJECT" 'echo "securestorage=$CLAUDE_SECURESTORAGE_CONFIG_DIR"; grep -c "tools/claude/auth" /proc/self/mountinfo || true' --auth-scope shared --auth-name check-a)
    if ! grep -q "securestorage=/home/agent/.enclave-auth" <<< "$out"; then
        record FAIL enroll "session did not start with the handle view: $(tail -n 3 <<< "$out")"
        return
    fi
    if ! grep -q '"protected":true' "$(identity_meta "$(identity_dir check-a)")" 2>/dev/null; then
        record FAIL enroll "identity metadata does not record protection"
        return
    fi
    if grep -qx '[1-9][0-9]*' <<< "$(tail -n 1 <<< "$out" | tr -d '\r')"; then
        record FAIL enroll "workload mount table names the real auth store"
        return
    fi
    record PASS enroll "fresh identity enrolled; workload sees only the view"
}

check_refuse_unprotected() {
    local out
    # shellcheck disable=SC2016 # Evaluated inside the session, so only a run prints it.
    out=$(enclave_in "$PROJECT" shell --auth-scope shared --auth-name check-a -- -c 'echo "raw-session-$((6 * 7))"')
    if grep -q raw-session-42 <<< "$out"; then
        record FAIL refuse-unprotected "an ordinary session opened the protected identity"
    elif grep -q "rerun with --protect-auth" <<< "$out"; then
        record PASS refuse-unprotected "omitting --protect-auth was refused"
    else
        record FAIL refuse-unprotected "no refusal reported: $(tail -n 2 <<< "$out")"
    fi
}

check_handles_only() {
    ensure_grant
    local out
    out=$(protected_shell "$PROJECT" "cat ~/.enclave-auth/.credentials.json; echo; grep -rIl -e '$SYNTHETIC_ACCESS' -e '$SYNTHETIC_REFRESH' /home /tmp /run /etc /opt /var 2>/dev/null | head -n 3; echo scan-done" --auth-scope shared --auth-name check-a)
    if ! grep -q 'sk-ant-oat01-' <<< "$out"; then
        record FAIL handles-only "view lacks an access handle: $(tail -n 3 <<< "$out")"
    elif grep -q -e "$SYNTHETIC_ACCESS" -e "$SYNTHETIC_REFRESH" <<< "$(sed '/scan-done/q' <<< "$out")"; then
        record FAIL handles-only "the grant's token is readable in the workload"
    else
        record PASS handles-only "the view holds handles; no workload file contains the grant's tokens"
    fi
}

check_release_refusals() {
    ensure_grant
    local out
    # shellcheck disable=SC2016 # Expanded inside the session.
    out=$(protected_shell "$PROJECT" '
        handle=$(sed -n "s/.*\"accessToken\":\"\([^\"]*\)\".*/\1/p" ~/.enclave-auth/.credentials.json)
        code() { curl -s -o /dev/null -w "%{http_code}" --max-time 20 "$@" || echo curl-fail; }
        echo "unrelated=$(code https://example.com/)"
        echo "unrelated-handle=$(code -H "Authorization: Bearer $handle" https://example.com/)"
        echo "token-lookalike=$(code -X POST https://api.anthropic.com/v1/oauth/token)"
        echo "revoke=$(code -X POST https://platform.claude.com/v1/oauth/token/revoke)"
        echo "api-key=$(code -X POST -H "Authorization: Bearer $handle" https://api.anthropic.com/api/oauth/claude_cli/create_api_key)"
    ' --auth-scope shared --auth-name check-a)
    local failures=()
    grep -q 'unrelated=[23]' <<< "$out" || failures+=("unrelated destination unreachable in unrestricted mode")
    for name in unrelated-handle token-lookalike revoke api-key; do
        grep -q "$name=403" <<< "$out" || failures+=("$name not refused")
    done
    if [ ${#failures[@]} -eq 0 ]; then
        record PASS release-refusals "handles, token endpoints and API-key minting refused; unrelated egress works"
    else
        record FAIL release-refusals "$(IFS=';'; echo "${failures[*]}")"
    fi
}

start_background() {
    enclave_in "$PROJECT" run --background --name "$1" --protect-auth --allow-all-network --auth-scope shared --auth-name check-a >/dev/null
    "$ENGINE" ps --format '{{.Names}}' | grep -qx "$(container_name "$1")"
}

check_mounts() {
    if ! ensure_background check-bg; then
        record FAIL mounts "background protected session did not start"
        return
    fi
    local workload gateway
    workload=$("$ENGINE" inspect --format '{{range .Mounts}}{{.Source}} {{.Destination}}{{"\n"}}{{end}}' "$(container_name check-bg)")
    gateway=$("$ENGINE" inspect --format '{{range .Mounts}}{{.Source}} {{.Destination}}{{"\n"}}{{end}}' "$(container_name check-bg)-gateway" 2>/dev/null)
    if grep -q -e "$STATE/tools/claude/auth" -e "$STATE/locks" <<< "$workload"; then
        record FAIL mounts "workload mounts the real grant or host locks"
    elif ! grep -q "$(identity_dir check-a) " <<< "$gateway"; then
        record FAIL mounts "gateway lacks the grant mount"
    elif ! grep -q "/home/agent/.enclave-auth$" <<< "$workload" || ! grep -q ".protected /home/agent/.claude$" <<< "$workload"; then
        record FAIL mounts "unexpected workload layout: $(tr '\n' ';' <<< "$workload")"
    else
        record PASS mounts "grant and locks only in the gateway; view and protected config store in the workload"
    fi
    local runtime_file owner
    runtime_file=$(find "$STATE/oauth-sessions" -name oauth-session.json -print -quit 2>/dev/null)
    owner=$(stat -c '%u %a' "$runtime_file" 2>/dev/null)
    if [ "$owner" = "$(id -u) 600" ]; then
        record PASS ownership "gateway-written state is owned by the host user with mode 600"
    else
        record FAIL ownership "gateway-written state: ${owner:-missing}"
    fi
    if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce)" = Enforcing ]; then
        record INFO selinux "$(ls -Z "$runtime_file" 2>/dev/null)"
    fi
}

check_exec_pin() {
    ensure_background check-bg
    local out
    out=$(enclave_in "$PROJECT" exec --name check-bg -- bash -c 'command -v gh')
    if grep -q "$GH_PIN" <<< "$out"; then
        record PASS exec-pin "exec selects the pinned GitHub CLI"
    else
        record FAIL exec-pin "exec resolved: $(tail -n 1 <<< "$out")"
    fi
}

view_hash() {
    enclave_in "$PROJECT" exec --name check-bg -- bash -c 'sha256sum < ~/.enclave-auth/.credentials.json' | grep -o '^[0-9a-f]\{64\}'
}

check_reload() {
    ensure_background check-bg
    local before after status
    before=$(view_hash)
    (cd "$PROJECT" && "$CLI_BIN" --tool claude --backend "$ENGINE" network apply >/dev/null 2>&1)
    after=$(view_hash)
    status=$(cd "$PROJECT" && "$CLI_BIN" --tool claude --backend "$ENGINE" network status 2>&1)
    if [ -z "$before" ] || [ "$before" != "$after" ]; then
        record FAIL reload "handles changed across proxy reload"
    elif ! grep -q "IN-SYNC .*check-bg" <<< "$status"; then
        record FAIL reload "network status after reload: $(grep check-bg <<< "$status")"
    else
        record PASS reload "proxy reload kept handles; status IN-SYNC"
    fi
}

check_concurrency() {
    ensure_background check-bg
    local out
    if ! ensure_background check-bg2; then
        record FAIL concurrency "second session for the same identity was refused"
        return
    fi
    out=$(cd "$PROJECT" && "$CLI_BIN" auth reset --tool claude --backend "$ENGINE" --auth-scope shared --auth-name check-a 2>&1)
    if [ -f "$(identity_dir check-a)/oauth-grant.json" ]; then
        record PASS concurrency "two sessions share the identity; reset refused while they run"
    else
        record FAIL concurrency "reset removed the grant of running sessions: $out"
    fi
}

check_stop_cleanup() {
    "$CLI_BIN" stop --backend "$ENGINE" "$(container_name check-bg)" >/dev/null 2>&1
    "$CLI_BIN" stop --backend "$ENGINE" "$(container_name check-bg2)" >/dev/null 2>&1
    local leftovers meta
    meta=$(identity_meta "$(identity_dir check-a)")
    leftovers=$(find "$STATE/oauth-sessions" -mindepth 1 -maxdepth 1 2>/dev/null; find "$STATE/locks" -name 'auth-start-*.lock' 2>/dev/null)
    if [ ! -f "$meta" ]; then
        record FAIL stop-cleanup "the identity was never enrolled"
    elif grep -q '"reservations":\[{' "$meta"; then
        record FAIL stop-cleanup "reservations survived stop"
    elif [ -n "$leftovers" ]; then
        record FAIL stop-cleanup "session state survived stop: $(tr '\n' ' ' <<< "$leftovers")"
    else
        record PASS stop-cleanup "stop released reservations, session trees and start locks"
    fi
}

check_continuity() {
    protected_shell "$PROJECT" 'mkdir -p ~/.claude/projects && echo kept > ~/.claude/projects/engine-check-marker' --auth-scope shared --auth-name check-a >/dev/null
    local protected ordinary
    protected=$(protected_shell "$PROJECT" 'cat ~/.claude/projects/engine-check-marker 2>/dev/null || echo missing' --auth-scope shared --auth-name check-a)
    ordinary=$(enclave_in "$PROJECT" shell --auth-scope shared --auth-name check-ordinary -- -c 'cat ~/.claude/projects/engine-check-marker 2>/dev/null || echo missing')
    if ! grep -q kept <<< "$protected"; then
        record FAIL continuity "protected history did not persist"
    elif grep -q kept <<< "$ordinary"; then
        record FAIL continuity "ordinary session sees protected history"
    else
        record PASS continuity "protected history persists and stays out of ordinary sessions"
    fi
}

check_project_scope() {
    protected_shell "$PROJECT" true --auth-scope project >/dev/null
    local dir
    dir=$(identity_dir "project.$PROJECT_HASH")
    if grep -q '"protected":true' "$(identity_meta "$dir")" 2>/dev/null; then
        record PASS project-scope "project identity enrolled at project.<hash>"
    else
        record FAIL project-scope "no protected project identity at $dir"
    fi
}

check_ephemeral() {
    # shellcheck disable=SC2016 # Evaluated inside the session, so only a run prints it.
    if ! grep -q ephemeral-42 <<< "$(protected_shell "$PROJECT" 'echo "ephemeral-$((6 * 7))"' --ephemeral)"; then
        record FAIL ephemeral "ephemeral protected session did not start"
        return
    fi
    sleep 3
    # The next admission sweeps an identity whose gateway was still being removed.
    protected_shell "$PROJECT" true --auth-scope shared --auth-name check-a >/dev/null
    local leftovers
    leftovers=$(find "$STATE/tools/claude/auth" -maxdepth 1 -name 'ephemeral.*' 2>/dev/null)
    if [ -z "$leftovers" ]; then
        record PASS ephemeral "per-session identity removed after the session"
    else
        record FAIL ephemeral "ephemeral identities remain: $leftovers"
    fi
}

check_reset_downgrade() {
    ensure_grant
    local out
    out=$(cd "$PROJECT" && "$CLI_BIN" auth reset --tool claude --backend "$ENGINE" --auth-scope shared --auth-name check-a 2>&1)
    if [ -f "$(identity_dir check-a)/oauth-grant.json" ]; then
        record FAIL reset-downgrade "reset kept the grant: $out"
        return
    fi
    grep -q '"protected":true' "$(identity_meta "$(identity_dir check-a)")" || { record FAIL reset-downgrade "reset removed protection"; return; }
    out=$(cd "$PROJECT" && "$CLI_BIN" auth downgrade --tool claude --backend "$ENGINE" --auth-scope shared --auth-name check-a 2>&1)
    if grep -q '"protected":false' "$(identity_meta "$(identity_dir check-a)")"; then
        record PASS reset-downgrade "reset kept protection; downgrade cleared it"
    else
        record FAIL reset-downgrade "downgrade failed: $out"
    fi
}

cleanup() {
    for session in check-bg check-bg2; do
        "$CLI_BIN" stop --backend "$ENGINE" "$(container_name "$session")" >/dev/null 2>&1 || true
    done
}

write_report() {
    {
        echo "# Protected auth engine check"
        echo
        echo "| Item | Value |"
        echo "|------|-------|"
        echo "| Checkout | $(git -C "$REPO_ROOT" rev-parse --short HEAD)$(git -C "$REPO_ROOT" diff --quiet || echo ' (dirty)') |"
        echo "| Engine | $ENGINE $("$ENGINE" version --format '{{.Server.Version}}' 2>/dev/null || "$ENGINE" --version) |"
        echo "| Rootless | $("$ENGINE" info --format '{{.Host.Security.Rootless}}' 2>/dev/null || echo unknown) |"
        echo "| SELinux | $(getenforce 2>/dev/null || echo unavailable) |"
        echo "| OS | $(uname -srm) |"
        echo
        echo "| Check | Result | Detail |"
        echo "|-------|--------|--------|"
        printf '%s\n' "${RESULTS[@]}"
    } > "$REPORT"
    echo "Report: $REPORT"
}

main() {
    ENGINE=""
    REPORT=""
    local checks=()
    while [ $# -gt 0 ]; do
        case $1 in
            --engine) ENGINE=$2; shift 2 ;;
            --report) REPORT=$2; shift 2 ;;
            *) checks+=("$1"); shift ;;
        esac
    done
    if [ -z "$ENGINE" ]; then
        ENGINE=$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)
    fi
    [ -x "$CLI_BIN" ] || { echo "enclave binary not found at $CLI_BIN (run make build)" >&2; exit 2; }
    command -v "$ENGINE" >/dev/null 2>&1 || { echo "$ENGINE not found" >&2; exit 2; }
    command -v script >/dev/null 2>&1 || { echo "script (util-linux) is required" >&2; exit 2; }
    [ ${#checks[@]} -gt 0 ] || read -r -a checks <<< "$ALL_CHECKS"

    local root
    root=$(mktemp -d "${TMPDIR:-/tmp}/enclave-protected-check.XXXXXX")
    export XDG_CONFIG_HOME=$root/config XDG_STATE_HOME=$root/state XDG_CACHE_HOME=$root/cache
    STATE=$XDG_STATE_HOME/enclave
    PROJECT=$root/project
    REPORT=${REPORT:-$root/report.md}
    mkdir -p "$PROJECT"
    git -C "$PROJECT" init -q
    git -C "$PROJECT" config user.name "Engine Check"
    git -C "$PROJECT" config user.email engine-check@example.test
    echo check > "$PROJECT/README.md"
    PROJECT_HASH=$(cd "$PROJECT" && "$CLI_BIN" --tool claude info 2>/dev/null | awk '/Project Hash:/ {print $3}')
    [ -n "$PROJECT_HASH" ] || { echo "could not resolve the project hash" >&2; exit 2; }
    trap cleanup EXIT

    for check in "${checks[@]}"; do
        "check_${check//-/_}"
    done
    write_report
    exit "$FAILED"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    main "$@"
fi
