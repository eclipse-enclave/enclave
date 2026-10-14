// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"enclave/internal/util"
)

// The engine check seeds this grant into an enrolled identity; a drifted
// schema would make every later check fail for the wrong reason.
func TestEngineCheckSyntheticGrantIsAccepted(t *testing.T) {
	b := fixtureBroker(t)
	config := b.config
	config.Identity = util.HashString(config.StoreDir)
	config.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(config.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := b.now().Unix()
	script := filepath.Join("..", "..", "..", "scripts", "protected-auth-engine-check.sh")
	cmd := exec.Command("bash", "-c", `. "$1" && synthetic_grant "$2" "$3"`, "engine-check", script, config.StoreDir, strconv.FormatInt(now, 10))
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("synthetic_grant: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.StoreDir, grantFile), output, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = b.now
	view, err := reopened.NativeView()
	if err != nil {
		t.Fatalf("gateway rejected the engine check's grant: %v", err)
	}
	if !strings.Contains(string(view), accessHandlePrefix) || strings.Contains(string(view), "synthetic-engine-check") {
		t.Fatalf("native view does not project handles: %s", view)
	}
}
