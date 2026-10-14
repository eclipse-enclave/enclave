#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Engine checks for --protect-auth that need no provider account: admission,
# mounts, handle-only views, release refusals, reload, exec, scopes, lifecycle
# cleanup, ownership and continuity, against a real Docker or Podman engine,
# for every built-in tool. Synthetic grants stand in for captured logins; they
# never authenticate at a provider. Real logins remain a human check.
#
# Runs against disposable XDG roots and a disposable project, so existing
# Enclave data is untouched. Builds each tool's image on first use.
#
# Usage: scripts/protected-auth-engine-check.sh [--engine docker|podman]
#          [--tool <name>|all] [--report FILE] [check...]
set -u

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLI_BIN=${CLI_BIN:-$REPO_ROOT/bin/enclave}
# Every synthetic secret contains one of these, so a scan finds any leak.
SYNTHETIC_MARKERS=(synthetic-engine-check syntheticenginecheck)
GH_PIN=/opt/enclave/gh-2.87.3/bin/gh
ALL_TOOLS="claude codex opencode pi mistral-vibe theia theia-next"
ALL_CHECKS="enroll refuse-unprotected handles-only release-refusals mounts exec-pin reload concurrency stop-cleanup continuity project-scope ephemeral reset-downgrade"
# Checks that need a named identity with grants; adapterless tools skip them.
IDENTITY_CHECKS=" refuse-unprotected handles-only concurrency project-scope reset-downgrade "

sha256_hex() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum | cut -d' ' -f1
    else
        shasum -a 256 | cut -d' ' -f1
    fi
}

b64url() { base64 | tr -d '\n=' | tr '+/' '-_'; }

iso_time() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ; }

# Per-tool layout, mirroring internal/protectedauth.
tool_config_dir() {
    case $1 in
        claude) echo .claude ;;
        codex) echo .codex ;;
        opencode) echo .config/opencode ;;
        pi) echo .pi ;;
        mistral-vibe) echo .vibe ;;
        theia) echo .theia-ide ;;
        theia-next) echo .theia-ide-next ;;
    esac
}

tool_adapters() {
    case $1 in
        claude) echo anthropic ;;
        codex) echo openai ;;
        opencode) echo openai copilot ;;
        pi) echo anthropic openai ;;
        mistral-vibe) echo mistral ;;
    esac
}

# The credential file the tool reads, inside the session.
tool_native_file() {
    case $1 in
        claude) echo "/home/agent/.enclave-auth/.credentials.json" ;;
        codex) echo "/home/agent/.codex/auth.json" ;;
        opencode) echo "/home/agent/.config/opencode/auth.json" ;;
        pi) echo "/home/agent/.pi/agent/auth.json" ;;
        mistral-vibe) echo "/home/agent/.vibe/.env" ;;
    esac
}

# The handle marker each adapter puts into native files.
adapter_marker() {
    case $1 in
        anthropic) echo "sk-ant-oat01-" ;;
        openai) echo "ENCLAVEoai" ;;
        mistral) echo "ENCLAVEmis" ;;
        copilot) echo "gho_enclave_copilot_" ;;
    esac
}

# synthetic_grants <tool> <identity-dir> <unix-now> writes a ready grant for
# each of the tool's adapters that the gateway accepts for that identity. None
# is a valid provider credential. The gateway reads only private files.
synthetic_grants() (
    umask 077
    local tool=$1 dir=$2 now=$3 identity committed expires header access id adapter
    identity=$(printf '%s' "$dir" | sha256_hex)
    committed=$(iso_time "$now")
    expires=$((now + 3600))
    for adapter in $(tool_adapters "$tool"); do
        case $adapter in
            anthropic)
                printf '{"schema":1,"identity":"%s","issuer":"https://platform.claude.com","generation":1,"status":"ready","committedAt":"%s","expiresAt":%d,"token":{"access_token":"synthetic-engine-check-access-0123456789","refresh_token":"synthetic-engine-check-refresh-0123456789","token_type":"Bearer","expires_in":3600,"scope":"user:inference user:profile","account":{"uuid":"11111111-1111-4111-8111-111111111111","email_address":"engine-check@example.test"},"organization":{"uuid":"22222222-2222-4222-8222-222222222222"}}}\n' \
                    "$identity" "$committed" "$expires" > "$dir/oauth-grant.json"
                ;;
            openai)
                header=$(printf '%s' '{"alg":"none"}' | b64url)
                access="$header.$(printf '{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"acct-engine-check"}}' "$expires" | b64url).synthetic-engine-check-access-signature"
                id="$header.$(printf '%s' '{"email":"engine-check@example.test","https://api.openai.com/auth":{"chatgpt_account_id":"acct-engine-check"}}' | b64url).synthetic-engine-check-id-signature"
                printf '{"schema":1,"identity":"%s","generation":1,"status":"ready","committedAt":"%s","expiresAt":%d,"accountId":"acct-engine-check","token":{"access":"%s","refresh":"synthetic-engine-check-refresh-0123456789","id":"%s","life":3600}}\n' \
                    "$identity" "$committed" "$expires" "$access" "$id" > "$dir/openai-grant.json"
                ;;
            mistral)
                printf '{"schema":1,"identity":"%s","status":"ready","committedAt":"%s","key":"synthetic-engine-check-mistral-key-0123456789"}\n' \
                    "$identity" "$committed" > "$dir/mistral-grant.json"
                ;;
            copilot)
                printf '{"schema":1,"identity":"%s","status":"ready","committedAt":"%s","token":"gho_syntheticenginecheck0123456789","scope":"read:user"}\n' \
                    "$identity" "$committed" > "$dir/copilot-grant.json"
                ;;
        esac
    done
)

RESULTS=()
FAILED=0

record() {
    local status=$1 name="$TOOL/$2" detail=$3
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
    cmd=$(printf '%q ' "$CLI_BIN" --tool "$TOOL" --backend "$ENGINE" "$@")
    (cd "$dir" && env -u ANTHROPIC_API_KEY -u ANTHROPIC_AUTH_TOKEN -u CLAUDE_CODE_OAUTH_TOKEN -u OPENAI_API_KEY \
        -u GEMINI_API_KEY -u MISTRAL_API_KEY -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN -u GITHUB_ENTERPRISE_TOKEN \
        -u GH_HOST -u GH_CONFIG_DIR -u GITLAB_TOKEN \
        script -qec "$cmd" /dev/null 2>&1)
}

protected_shell() {
    local dir=$1 script=$2
    shift 2
    enclave_in "$dir" shell --protect-auth --allow-all-network "$@" -- -c "$script"
}

identity_dir() { echo "$STATE/tools/$TOOL/auth/$1"; }
identity_meta() {
    local hash
    hash=$(printf '%s' "$1" | sha256_hex)
    echo "$STATE/locks/store-$hash.identity.json"
}
container_name() { echo "enclave-$TOOL-$PROJECT_HASH-$1"; }
has_adapters() { [ -n "$(tool_adapters "$TOOL")" ]; }

seed_grants() {
    local dir
    dir=$(identity_dir "$1")
    [ -d "$dir" ] || return 1
    synthetic_grants "$TOOL" "$dir" "$(date +%s)"
}

# Checks run in the listed order; these let a single check run on its own.
ensure_grant() {
    has_adapters || return 0
    if [ ! -f "$(identity_meta "$(identity_dir check-a)")" ]; then
        protected_shell "$PROJECT" true --auth-scope shared --auth-name check-a >/dev/null
    fi
    [ -n "$(find "$(identity_dir check-a)" -name '*grant.json' 2>/dev/null)" ] || seed_grants check-a
}

start_background() {
    enclave_in "$PROJECT" run --background --name "$1" --protect-auth --allow-all-network --auth-scope shared --auth-name check-a >/dev/null
    "$ENGINE" ps --format '{{.Names}}' | grep -qx "$(container_name "$1")"
}

ensure_background() {
    ensure_grant
    "$ENGINE" ps --format '{{.Names}}' | grep -qx "$(container_name "$1")" || start_background "$1"
}

check_enroll() {
    local out native view_link=""
    native=$(tool_native_file "$TOOL")
    if [ "$TOOL" = claude ]; then
        # shellcheck disable=SC2016 # Expanded inside the session.
        view_link='echo "view=$CLAUDE_SECURESTORAGE_CONFIG_DIR";'
    elif [ -n "$native" ]; then
        view_link="echo \"view=\$(dirname \"\$(readlink $native)\")\";"
    fi
    # shellcheck disable=SC2016 # Expanded inside the session.
    out=$(protected_shell "$PROJECT" "echo \"protected=\$ENCLAVE_PROTECTED_AUTH\"; $view_link echo \"auth-mounts=\$(grep -c \"tools/$TOOL/auth\" /proc/self/mountinfo)\"" --auth-scope shared --auth-name check-a)
    if ! grep -q "protected=1" <<< "$out"; then
        record FAIL enroll "session did not start protected: $(tail -n 3 <<< "$out")"
        return
    fi
    if [ -n "$native" ] && ! grep -q "view=/home/agent/.enclave-auth" <<< "$out"; then
        record FAIL enroll "native credentials do not resolve to the handle view: $(tail -n 3 <<< "$out")"
        return
    fi
    if has_adapters && ! grep -q '"protected":true' "$(identity_meta "$(identity_dir check-a)")" 2>/dev/null; then
        record FAIL enroll "identity metadata does not record protection"
        return
    fi
    if ! grep -q '^auth-mounts=0' <<< "$(tr -d '\r' <<< "$out")"; then
        record FAIL enroll "workload mount table names the real auth store, or was not read: $(grep auth-mounts <<< "$out")"
        return
    fi
    record PASS enroll "protected session started; credentials resolve to the view only"
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
    local out native adapter missing=() leaks
    native=$(tool_native_file "$TOOL")
    # Matching files are reported on LEAK lines; scan-done proves the scan ran.
    out=$(protected_shell "$PROJECT" "cat $native; echo; grep -rIl -e '${SYNTHETIC_MARKERS[0]}' -e '${SYNTHETIC_MARKERS[1]}' /home /tmp /run /etc /opt /var 2>/dev/null | sed 's/^/LEAK: /'; echo scan-done" --auth-scope shared --auth-name check-a)
    out=$(tr -d '\r' <<< "$out")
    leaks=$(grep '^LEAK: ' <<< "$out")
    for adapter in $(tool_adapters "$TOOL"); do
        grep -q "$(adapter_marker "$adapter")" <<< "$out" || missing+=("$adapter")
    done
    if ! grep -qx scan-done <<< "$out"; then
        record FAIL handles-only "the workload scan did not complete: $(tail -n 3 <<< "$out")"
    elif [ ${#missing[@]} -ne 0 ]; then
        record FAIL handles-only "native file lacks handles for: ${missing[*]} ($(tail -n 3 <<< "$out"))"
    elif [ -n "$leaks" ] || grep -q -e "${SYNTHETIC_MARKERS[0]}" -e "${SYNTHETIC_MARKERS[1]}" <<< "$(sed '/^scan-done$/q' <<< "$out")"; then
        record FAIL handles-only "a grant's secret is readable in the workload: $(tr '\n' ' ' <<< "$leaks")"
    else
        record PASS handles-only "the native file holds handles; no workload file contains a grant's secret"
    fi
}

check_release_refusals() {
    ensure_grant
    local out native marker=""
    native=$(tool_native_file "$TOOL")
    has_adapters && marker=$(adapter_marker "$(tool_adapters "$TOOL" | cut -d' ' -f1)")
    # shellcheck disable=SC2016 # Expanded inside the session.
    out=$(protected_shell "$PROJECT" '
        handle=$(grep -o "[A-Za-z0-9._-]*'"${marker:-no-handle}"'[A-Za-z0-9._-]*" '"${native:-/dev/null}"' 2>/dev/null | head -n 1)
        code() { curl -s -o /dev/null -w "%{http_code}" --max-time 20 "$@" || echo curl-fail; }
        echo "unrelated=$(code https://example.com/)"
        if [ -n "$handle" ]; then echo "unrelated-handle=$(code -H "Authorization: Bearer $handle" https://example.com/)"; fi
        echo "pasted-key=$(code -X POST -H "x-api-key: sk-ant-api03-pasted" https://api.anthropic.com/v1/messages)"
        echo "pasted-token=$(code -H "Authorization: Bearer pasted" https://chatgpt.com/backend-api/codex/models)"
        echo "token-lookalike=$(code -X POST https://api.anthropic.com/v1/oauth/token)"
        echo "revoke=$(code -X POST https://platform.claude.com/v1/oauth/token/revoke)"
        echo "api-key-exchange=$(code -X POST https://api.anthropic.com/api/oauth/claude_cli/create_api_key)"
    ' --auth-scope shared --auth-name check-a)
    local failures=() name
    grep -q 'unrelated=[23]' <<< "$out" || failures+=("unrelated destination unreachable in unrestricted mode")
    for name in pasted-key pasted-token token-lookalike revoke api-key-exchange; do
        grep -q "$name=403" <<< "$out" || failures+=("$name not refused")
    done
    if has_adapters && ! grep -q 'unrelated-handle=403' <<< "$out"; then
        failures+=("handle released to an unrelated host")
    fi
    if [ ${#failures[@]} -eq 0 ]; then
        record PASS release-refusals "handles, pasted credentials, token and API-key endpoints refused; unrelated egress works"
    else
        record FAIL release-refusals "$(IFS=';'; echo "${failures[*]}")"
    fi
}

check_mounts() {
    if ! ensure_background check-bg; then
        record FAIL mounts "background protected session did not start"
        return
    fi
    local workload gateway
    workload=$("$ENGINE" inspect --format '{{range .Mounts}}{{.Source}} {{.Destination}}{{"\n"}}{{end}}' "$(container_name check-bg)")
    gateway=$("$ENGINE" inspect --format '{{range .Mounts}}{{.Source}} {{.Destination}}{{"\n"}}{{end}}' "$(container_name check-bg)-gateway" 2>/dev/null)
    if grep -q -e "$STATE/tools/$TOOL/auth" -e "$STATE/locks" <<< "$workload"; then
        record FAIL mounts "workload mounts a real grant or host locks"
    elif ! grep -q " /oauth/grant$" <<< "$gateway"; then
        record FAIL mounts "gateway lacks the grant mount"
    elif ! grep -q "/home/agent/.enclave-auth$" <<< "$workload" || ! grep -q ".protected /home/agent/$(tool_config_dir "$TOOL")$" <<< "$workload"; then
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
    enclave_in "$PROJECT" exec --name check-bg -- bash -c "find /home/agent/.enclave-auth -type f -exec sha256sum {} + | sort | sha256sum" | grep -o '^[0-9a-f]\{64\}'
}

check_reload() {
    ensure_background check-bg
    local before after status
    before=$(view_hash)
    (cd "$PROJECT" && "$CLI_BIN" --tool "$TOOL" --backend "$ENGINE" network apply >/dev/null 2>&1)
    after=$(view_hash)
    status=$(cd "$PROJECT" && "$CLI_BIN" --tool "$TOOL" --backend "$ENGINE" network status 2>&1)
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
    out=$(cd "$PROJECT" && "$CLI_BIN" auth reset --tool "$TOOL" --backend "$ENGINE" --auth-scope shared --auth-name check-a 2>&1)
    if [ -z "$(find "$(identity_dir check-a)" -name '*grant.json' 2>/dev/null)" ]; then
        record FAIL concurrency "reset removed the grants of running sessions: $out"
    elif ! grep -q "auth identity is in use" <<< "$out"; then
        record FAIL concurrency "reset kept the grants for another reason: $out"
    else
        record PASS concurrency "two sessions share the identity; reset refused while they run"
    fi
}

check_stop_cleanup() {
    "$CLI_BIN" stop --backend "$ENGINE" "$(container_name check-bg)" >/dev/null 2>&1
    "$CLI_BIN" stop --backend "$ENGINE" "$(container_name check-bg2)" >/dev/null 2>&1
    sleep 3
    # The next admission sweeps state a stopped gateway still held.
    protected_shell "$PROJECT" true --auth-scope shared --auth-name check-a >/dev/null
    local leftovers meta
    meta=$(identity_meta "$(identity_dir check-a)")
    leftovers=$(find "$STATE/oauth-sessions" -mindepth 1 -maxdepth 1 2>/dev/null; find "$STATE/locks" -name 'auth-start-*.lock' 2>/dev/null)
    if has_adapters && [ ! -f "$meta" ]; then
        record FAIL stop-cleanup "the identity was never enrolled"
    elif has_adapters && grep -q '"reservations":\[{' "$meta"; then
        record FAIL stop-cleanup "reservations survived stop"
    elif [ -n "$leftovers" ]; then
        record FAIL stop-cleanup "session state survived stop: $(tr '\n' ' ' <<< "$leftovers")"
    else
        record PASS stop-cleanup "stop released reservations, session trees and start locks"
    fi
}

check_continuity() {
    local dir protected ordinary
    dir="/home/agent/$(tool_config_dir "$TOOL")"
    protected_shell "$PROJECT" "echo kept > $dir/engine-check-marker" --auth-scope shared --auth-name check-a >/dev/null
    protected=$(protected_shell "$PROJECT" "cat $dir/engine-check-marker 2>/dev/null || echo missing" --auth-scope shared --auth-name check-a)
    ordinary=$(enclave_in "$PROJECT" shell --auth-scope shared --auth-name check-ordinary -- -c "cat $dir/engine-check-marker 2>/dev/null || echo missing")
    if ! grep -q kept <<< "$protected"; then
        record FAIL continuity "protected config did not persist"
    elif grep -q kept <<< "$ordinary"; then
        record FAIL continuity "ordinary session sees protected config"
    else
        record PASS continuity "protected config persists and stays out of ordinary sessions"
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
    leftovers=$(find "$STATE/tools/$TOOL/auth" -maxdepth 1 -name 'ephemeral.*' 2>/dev/null)
    if [ -z "$leftovers" ]; then
        record PASS ephemeral "per-session identities removed after their sessions"
    else
        record FAIL ephemeral "ephemeral identities remain: $leftovers"
    fi
}

check_reset_downgrade() {
    ensure_grant
    local out
    out=$(cd "$PROJECT" && "$CLI_BIN" auth reset --tool "$TOOL" --backend "$ENGINE" --auth-scope shared --auth-name check-a 2>&1)
    if [ -n "$(find "$(identity_dir check-a)" -name '*grant.json' 2>/dev/null)" ]; then
        record FAIL reset-downgrade "reset kept a grant: $out"
        return
    fi
    grep -q '"protected":true' "$(identity_meta "$(identity_dir check-a)")" || { record FAIL reset-downgrade "reset removed protection"; return; }
    out=$(cd "$PROJECT" && "$CLI_BIN" auth downgrade --tool "$TOOL" --backend "$ENGINE" --auth-scope shared --auth-name check-a 2>&1)
    if grep -q '"protected":false' "$(identity_meta "$(identity_dir check-a)")"; then
        record PASS reset-downgrade "reset kept protection; downgrade cleared it"
    else
        record FAIL reset-downgrade "downgrade failed: $out"
    fi
}

cleanup() {
    local tool session
    for tool in $TOOLS; do
        for session in check-bg check-bg2; do
            "$CLI_BIN" stop --backend "$ENGINE" "enclave-$tool-$PROJECT_HASH-$session" >/dev/null 2>&1 || true
        done
    done
    rm -rf "$ROOT" 2>/dev/null || echo "Left $ROOT behind; remove it after checking its ownership" >&2
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
        echo "| Tools | $TOOLS |"
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
    TOOLS=claude
    local checks=()
    while [ $# -gt 0 ]; do
        case $1 in
            --engine) ENGINE=$2; shift 2 ;;
            --report) REPORT=$2; shift 2 ;;
            --tool) TOOLS=$2; shift 2 ;;
            *) checks+=("$1"); shift ;;
        esac
    done
    # Elsewhere Enclave ignores XDG_*, so the checks would touch real state.
    [ "$(uname -s)" = Linux ] || { echo "the engine check runs on Linux hosts only" >&2; exit 2; }
    [ "$TOOLS" = all ] && TOOLS=$ALL_TOOLS
    for TOOL in $TOOLS; do
        [ -n "$(tool_config_dir "$TOOL")" ] || { echo "unknown tool $TOOL" >&2; exit 2; }
    done
    if [ -z "$ENGINE" ]; then
        ENGINE=$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)
    fi
    [ -x "$CLI_BIN" ] || { echo "enclave binary not found at $CLI_BIN (run make build)" >&2; exit 2; }
    command -v "$ENGINE" >/dev/null 2>&1 || { echo "$ENGINE not found" >&2; exit 2; }
    command -v script >/dev/null 2>&1 || { echo "script (util-linux) is required" >&2; exit 2; }
    [ ${#checks[@]} -gt 0 ] || read -r -a checks <<< "$ALL_CHECKS"
    for check in "${checks[@]}"; do
        [[ " $ALL_CHECKS " == *" $check "* ]] || { echo "unknown check $check" >&2; exit 2; }
    done

    # Mount sources are canonical paths; a symlinked TMPDIR would hide leaks.
    ROOT=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/enclave-protected-check.XXXXXX")" && pwd -P)
    local root=$ROOT
    export XDG_CONFIG_HOME=$root/config XDG_STATE_HOME=$root/state XDG_CACHE_HOME=$root/cache
    STATE=$XDG_STATE_HOME/enclave
    PROJECT=$root/project
    REPORT=${REPORT:-$PWD/protected-auth-engine-check.md}
    mkdir -p "$PROJECT"
    git -C "$PROJECT" init -q
    git -C "$PROJECT" config user.name "Engine Check"
    git -C "$PROJECT" config user.email engine-check@example.test
    echo check > "$PROJECT/README.md"
    PROJECT_HASH=$(cd "$PROJECT" && "$CLI_BIN" --tool claude info 2>/dev/null | awk '/Project Hash:/ {print $3}')
    [ -n "$PROJECT_HASH" ] || { echo "could not resolve the project hash" >&2; exit 2; }
    trap cleanup EXIT

    for TOOL in $TOOLS; do
        for check in "${checks[@]}"; do
            if ! has_adapters && [[ $IDENTITY_CHECKS == *" $check "* ]]; then
                record SKIP "$check" "no login adapters, so no named identity"
                continue
            fi
            "check_${check//-/_}"
        done
    done
    write_report
    exit "$FAILED"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    main "$@"
fi
