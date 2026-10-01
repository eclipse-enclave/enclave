// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The vnc feature is the only in-tree extension that uses commands.startup, and
// model.Extension carries no Commands field, so the golden surface snapshot in
// internal/config cannot pin that part of its spec. These tests cover the seam
// the snapshot misses: that the shipped spec is shaped the way the runtime yq
// glue expects, and that the argv it names is the path install.sh writes.

const vncSupervisorPath = "/usr/local/bin/vnc-supervisor"

func vncFeatureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "extensions", "features", "vnc"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return dir
}

// TestVNCStartupCommandRunsDetached feeds the real spec through
// enclave_apply_startup_commands with the supervisor path redirected at a stub,
// proving the seq-form command parses and that background: true detaches it.
func TestVNCStartupCommandRunsDetached(t *testing.T) {
	if !hasMikefarahYq(t) {
		t.Skip("requires mikefarah/yq v4; host has kislyuk yq")
	}

	spec, err := os.ReadFile(filepath.Join(vncFeatureDir(t), "spec.yaml"))
	if err != nil {
		t.Fatalf("read vnc spec: %v", err)
	}
	if !strings.Contains(string(spec), vncSupervisorPath) {
		t.Fatalf("vnc spec no longer names %s; update this test and install.sh together", vncSupervisorPath)
	}

	work := t.TempDir()
	extDir := t.TempDir()
	gate := filepath.Join(work, "gate")
	marker := filepath.Join(work, "started")

	// The stub blocks on a FIFO before touching the marker, so the marker can
	// only appear after enclave_apply_startup_commands has already returned.
	// A foreground regression would deadlock on the write to the gate instead.
	stub := filepath.Join(work, "vnc-supervisor")
	stubBody := "#!/bin/bash\nread -r _ < " + gate + "\ntouch " + marker + "\n"
	if err := os.WriteFile(stub, []byte(stubBody), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	redirected := strings.ReplaceAll(string(spec), vncSupervisorPath, stub)
	if err := os.WriteFile(filepath.Join(extDir, "spec.yaml"), []byte(redirected), 0o644); err != nil {
		t.Fatalf("write redirected spec: %v", err)
	}

	script := `set -e
. "$KIT"
mkfifo "$GATE"
enclave_apply_startup_commands "$EXT"
if [ -e "$MARKER" ]; then echo "MARKER_TOO_EARLY"; exit 1; fi
echo go > "$GATE"
wait
if [ ! -e "$MARKER" ]; then echo "MARKER_MISSING"; exit 1; fi
echo PASS`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = startupEnv(t, map[string]string{"EXT": extDir, "GATE": gate, "MARKER": marker})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apply vnc startup: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("expected PASS, got:\n%s", out)
	}
}

// TestVNCInstallScriptMatchesStartupCommand keeps spec.yaml's argv and the
// install destinations from drifting apart: a rename on one side would
// otherwise only surface as a session that starts without a display.
func TestVNCInstallScriptMatchesStartupCommand(t *testing.T) {
	dir := vncFeatureDir(t)
	install, err := os.ReadFile(filepath.Join(dir, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	body := string(install)

	for _, name := range []string{"vnc-supervisor", "vnc-open", "vnc-chromium"} {
		source := filepath.Join(dir, "bin", name)
		if _, err := os.Stat(source); err != nil {
			t.Fatalf("missing shipped script %s: %v", source, err)
		}
		// install.sh must set the mode explicitly: source modes are lost when
		// the asset tree comes from the embedded FS or a distro package, which
		// normalize only install.sh itself.
		want := `install -D -m 755 "$dir/bin/` + name + `" /usr/local/bin/` + name
		if !strings.Contains(body, want) {
			t.Fatalf("install.sh must contain %q, got:\n%s", want, body)
		}
	}

	if !strings.Contains(body, "/usr/local/share/enclave/vnc/waiting.html") {
		t.Fatalf("install.sh must place the waiting page under the shared enclave asset root, got:\n%s", body)
	}
}
