#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Live smoke test for the image inbox (--image-inbox, `enclave img import`):
# clipboard, screenshot, file, and stdin import; the read-only mount; isolation
# between two projects; targeting; and inbox removal when a project's last
# inbox-enabled session ends. Linux only. Requires a Docker daemon (or podman,
# with ENGINE=podman); builds the claude tool image on first use.
#
# Fake wl-paste, wl-copy, slurp, grim, and notify-send on PATH stand in for
# the desktop, so it needs no display and leaves the real clipboard alone.
# It starts and stops only its own sessions, by container name. Each import
# also sweeps the inboxes of every project without a running inbox-enabled
# session, as any import does. It refuses to run while another
# inbox-enabled session is running, since imports could target it.
#
# Usage: scripts/smoke-image-inbox.sh
set -u

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
CLI_BIN=${CLI_BIN:-$REPO_ROOT/bin/enclave}
ENGINE=${ENGINE:-docker}
# Outside home: enclave refuses to mount hidden home directories.
SMOKE_ROOT=${SMOKE_ROOT:-${TMPDIR:-/tmp}/enclave-img-smoke-$(id -u)}
P1=$SMOKE_ROOT/project1
P2=$SMOKE_ROOT/project2
S1=imgsmoke1-$$
S2=imgsmoke2-$$
INBOX_ROOT=${XDG_CACHE_HOME:-$HOME/.cache}/enclave/inbox
CONTAINER_INBOX=/mnt/host-images

pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*"; exit 1; }

[ "$(uname -s)" = Linux ] || fail "Linux only"
[ -x "$CLI_BIN" ] || fail "enclave binary not found at $CLI_BIN (run make build)"
mkdir -p "$P1" "$P2"
echo smoke1 > "$P1/README.md"
echo smoke2 > "$P2/README.md"

# run_tty <dir> <enclave args...>: run enclave under a pty, capture output.
run_tty() {
  local dir=$1; shift
  local cmd
  cmd=$(printf '%q ' "$CLI_BIN" "$@")
  (cd "$dir" && script -qec "$cmd" /dev/null 2>&1)
}

hash_for() { (cd "$1" && "$CLI_BIN" info --tool claude 2>/dev/null | awk '/Project Hash:/ {print $3}'); }

H1=$(hash_for "$P1")
H2=$(hash_for "$P2")
[ -n "$H1" ] && [ -n "$H2" ] || fail "could not resolve project hashes"
C1=enclave-claude-$H1-$S1
C2=enclave-claude-$H2-$S2

FAKE=$(mktemp -d)
FAKEBIN=$FAKE/bin
FIXTURE=$FAKE/fixture.png
CLIPBOARD_OUT=$FAKE/clipboard-out
NOTIFY_LOG=$FAKE/notify.log
mkdir -p "$FAKEBIN"

cleanup() {
  local c
  for c in "$C1" "$C2"; do
    "$CLI_BIN" stop "$c" >/dev/null 2>&1
    "$ENGINE" rm -f "$c" >/dev/null 2>&1
  done
  rm -rf "$FAKE"
}
trap cleanup EXIT

# Minimal valid 1x1 PNG.
printf '\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\x0aIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB\x60\x82' > "$FIXTURE"

cat > "$FAKEBIN/wl-paste" <<EOF
#!/bin/bash
[ "\$1" = --list-types ] && { echo image/png; exit 0; }
cat "$FIXTURE"
EOF
cat > "$FAKEBIN/wl-copy" <<EOF
#!/bin/bash
cat > "$CLIPBOARD_OUT"
EOF
cat > "$FAKEBIN/slurp" <<'EOF'
#!/bin/bash
echo "0,0 1x1"
EOF
cat > "$FAKEBIN/grim" <<EOF
#!/bin/bash
cat "$FIXTURE"
EOF
cat > "$FAKEBIN/notify-send" <<EOF
#!/bin/bash
printf '%s\n' "\$*" >> "$NOTIFY_LOG"
EOF
chmod +x "$FAKEBIN"/*

export PATH="$FAKEBIN:$PATH"
export WAYLAND_DISPLAY=smoke
unset DISPLAY

# img_import <dir> <img import args...>: run `img import` in dir; prints
# stdout only, stderr goes to $FAKE/stderr.
img_import() {
  local dir=$1; shift
  (cd "$dir" && "$CLI_BIN" img import "$@" 2>"$FAKE/stderr")
}

# in_container <container> <command>
in_container() { "$ENGINE" exec "$1" sh -c "$2"; }

echo "=== precondition ==="
img_import "$SMOKE_ROOT" --file "$FIXTURE" >/dev/null && fail "an import succeeded before the smoke sessions started"
grep -q "no running session has an image inbox" "$FAKE/stderr" \
  || fail "stop other inbox-enabled sessions first: $(cat "$FAKE/stderr")"
pass "no other inbox-enabled session is running"

echo "=== start two inbox-enabled sessions ==="
run_tty "$P1" run --tool claude --background --name "$S1" --image-inbox --allow-all-network --disable-gateway >/dev/null
run_tty "$P2" run --tool claude --background --name "$S2" --image-inbox --allow-all-network --disable-gateway >/dev/null
for c in "$C1" "$C2"; do
  "$ENGINE" ps --format '{{.Names}}' | grep -qx "$c" || fail "container $c not running"
done
[ "$(stat -c %a "$INBOX_ROOT/$H1")" = 700 ] || fail "inbox of project1 is not mode 0700"
[ "$(in_container "$C1" 'printenv ENCLAVE_IMAGE_INBOX')" = "$CONTAINER_INBOX" ] || fail "ENCLAVE_IMAGE_INBOX not set"
pass "sessions started with their inboxes"

echo "=== clipboard import ==="
out=$(img_import "$P1") || fail "clipboard import failed: $(cat "$FAKE/stderr")"
case $out in
  "$CONTAINER_INBOX"/*.png) ;;
  *) fail "stdout is not a single container path: $out" ;;
esac
IMG1=$(basename "$out")
[ "$(cat "$CLIPBOARD_OUT")" = "$out" ] || fail "container path not copied to the clipboard"
grep -q "Imported host image for .*project1" "$NOTIFY_LOG" || fail "no success notification naming project1"
[ "$(stat -c %a "$INBOX_ROOT/$H1/$IMG1")" = 600 ] || fail "host image is not mode 0600"
"$ENGINE" exec "$C1" cat "$out" | cmp -s - "$FIXTURE" || fail "host image differs in the session"
in_container "$C1" "echo x > '$out'" 2>&1 | grep -q "Read-only file system" \
  || fail "session could write to its inbox"
in_container "$C1" "touch '$CONTAINER_INBOX/new'" 2>&1 | grep -q "Read-only file system" \
  || fail "session could create a file in its inbox"
pass "clipboard import landed read-only in project1, path on clipboard, notified"

echo "=== screenshot, file, and stdin import ==="
: > "$CLIPBOARD_OUT"
out=$(img_import "$P1" --screenshot --no-clipboard) || fail "screenshot import failed: $(cat "$FAKE/stderr")"
in_container "$C1" "test -f '$out'" || fail "screenshot not visible in project1"
[ -s "$CLIPBOARD_OUT" ] && fail "--no-clipboard still wrote the clipboard"
cp "$FIXTURE" "$FAKE/shot.png"
out=$(img_import "$P2" --file "$FAKE/shot.png") || fail "file import failed: $(cat "$FAKE/stderr")"
IMG2=$(basename "$out")
in_container "$C2" "test -f '$out'" || fail "file import not visible in project2"
[ -s "$CLIPBOARD_OUT" ] && fail "--file wrote the clipboard"
out=$(img_import "$P2" - < "$FIXTURE") || fail "stdin import failed: $(cat "$FAKE/stderr")"
in_container "$C2" "test -f '$out'" || fail "stdin import not visible in project2"
pass "screenshot, --file, and stdin imports"

echo "=== validation ==="
echo "not an image" > "$FAKE/bad.png"
img_import "$P1" --file "$FAKE/bad.png" >/dev/null && fail "a non-image was accepted"
grep -q "not a PNG or JPEG" "$FAKE/stderr" || fail "unexpected error for a non-image: $(cat "$FAKE/stderr")"
pass "non-image rejected"

echo "=== isolation between projects ==="
for c in "$C1" "$C2"; do
  "$ENGINE" ps --format '{{.Names}}' | grep -qx "$c" || fail "container $c stopped"
done
in_container "$C2" "test -e '$CONTAINER_INBOX/$IMG1'" && fail "project2 sees project1's host image"
in_container "$C1" "test -e '$CONTAINER_INBOX/$IMG2'" && fail "project1 sees project2's host image"
pass "each project sees only its own host images"

echo "=== targeting outside a project ==="
: > "$NOTIFY_LOG"
img_import "$SMOKE_ROOT" --file "$FAKE/shot.png" >/dev/null && fail "ambiguous import was accepted"
grep -q "$S1" "$FAKE/stderr" && grep -q "$S2" "$FAKE/stderr" \
  || fail "ambiguity error does not list both sessions: $(cat "$FAKE/stderr")"
grep -q "Host image import failed" "$NOTIFY_LOG" || fail "no failure notification"
out=$(img_import "$SMOKE_ROOT" --file "$FAKE/shot.png" --session "$S2") \
  || fail "--session import failed: $(cat "$FAKE/stderr")"
in_container "$C2" "test -f '$out'" || fail "--session import not visible in project2"
pass "ambiguous import refused; --session picks the project"

echo "=== cleanup ==="
"$CLI_BIN" stop "$C1" >/dev/null 2>&1 || fail "enclave stop $C1 failed"
[ -e "$INBOX_ROOT/$H1" ] && fail "project1's inbox survived its last session"
[ -d "$INBOX_ROOT/$H2" ] || fail "stopping project1 removed project2's inbox"
pass "enclave stop removed only the stopped project's inbox"
"$ENGINE" stop -t 2 "$C2" >/dev/null 2>&1 || fail "$ENGINE stop $C2 failed"
img_import "$P2" --file "$FAKE/shot.png" >/dev/null && fail "import without a running session was accepted"
[ -e "$INBOX_ROOT/$H2" ] && fail "the next import did not sweep project2's orphaned inbox"
pass "the next import swept the inbox of a session stopped outside enclave"

echo "ALL SMOKE CHECKS PASSED"
