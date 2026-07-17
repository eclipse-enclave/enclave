#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Smoke test for the host-import image inbox (--image-inbox / `enclave img import`).
#
# It puts a fake `wl-paste` on PATH that emits a fixture PNG, starts a background
# session with --image-inbox, imports the "clipboard" image, and asserts the
# file lands in the container read-only. Requires Docker and a built image; run
# it directly (there is no make target):
#
#   scripts/smoke-image-inbox.sh
#
# Honors ENCLAVE_BIN (defaults to ./bin/enclave) and SMOKE_TOOL (claude).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${ENCLAVE_BIN:-$ROOT_DIR/bin/enclave}"
TOOL="${SMOKE_TOOL:-claude}"
SESSION="imgsmoke-$$"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}
pass() {
	echo "PASS: $*"
}

[ -x "$BIN" ] || fail "enclave binary not found at $BIN (run: make build)"

WORK="$(mktemp -d)"
FAKEBIN="$WORK/bin"
FIXTURE="$WORK/fixture.png"
mkdir -p "$FAKEBIN"

cleanup() {
	"$BIN" stop "$SESSION" >/dev/null 2>&1 || true
	rm -rf "$WORK"
}
trap cleanup EXIT

# Minimal valid 1x1 PNG fixture.
printf '\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\x0aIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB\x60\x82' >"$FIXTURE"

# Fake wl-paste: `--list-types` advertises PNG; `--type image/png` emits the fixture.
cat >"$FAKEBIN/wl-paste" <<EOF
#!/usr/bin/env bash
for arg in "\$@"; do
	if [ "\$arg" = "--list-types" ]; then echo "image/png"; exit 0; fi
done
cat "$FIXTURE"
EOF
chmod +x "$FAKEBIN/wl-paste"

# Fake wl-copy so --no-copy is not required for the clipboard-copy path.
cat >"$FAKEBIN/wl-copy" <<'EOF'
#!/usr/bin/env bash
cat >/dev/null
EOF
chmod +x "$FAKEBIN/wl-copy"

export PATH="$FAKEBIN:$PATH"
export WAYLAND_DISPLAY="smoke"
unset DISPLAY || true

echo "Starting background session $SESSION with --image-inbox..."
CONTAINER="$("$BIN" run --tool "$TOOL" --background --name "$SESSION" --image-inbox 2>/dev/null | tail -n1)"
[ -n "$CONTAINER" ] || fail "session did not start"
pass "session started: $CONTAINER"

echo "Importing clipboard image..."
IMPORTED="$("$BIN" img import --no-copy 2>/dev/null | grep '^/mnt/host-images/' | tail -n1)"
[ -n "$IMPORTED" ] || fail "img import did not print a container path"
pass "imported path: $IMPORTED"

echo "Verifying the file is readable in the container..."
docker exec "$CONTAINER" test -f "$IMPORTED" || fail "imported file not visible in container"
pass "file visible in container"

echo "Verifying the mount is read-only..."
if docker exec "$CONTAINER" sh -c "echo x > '$IMPORTED'" >/dev/null 2>&1; then
	fail "container was able to write to the inbox (should be read-only)"
fi
pass "inbox mount is read-only"

echo "ALL SMOKE CHECKS PASSED"
