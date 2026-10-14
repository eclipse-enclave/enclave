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

	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

var adapterMarkers = map[string]string{
	protectedauth.AdapterAnthropic: accessHandlePrefix,
	protectedauth.AdapterOpenAI:    openaiHandleMarker,
	protectedauth.AdapterMistral:   mistralHandleMarker,
	protectedauth.AdapterCopilot:   copilotHandlePrefix,
}

// engineCheck calls a function of the engine check script.
func engineCheck(t *testing.T, args ...string) string {
	t.Helper()
	script := filepath.Join("..", "..", "..", "scripts", "protected-auth-engine-check.sh")
	cmd := exec.Command("bash", append([]string{"-c", `. "$1" && shift && "$@"`, "engine-check", script}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

// The script mirrors the protected tool table; a drifted copy would check the
// wrong files.
func TestEngineCheckToolTableMatchesProtectedAuth(t *testing.T) {
	var names []string
	for _, tool := range protectedauth.Tools() {
		names = append(names, tool.Name)
		if got := engineCheck(t, "tool_config_dir", tool.Name); got != tool.ConfigDir {
			t.Errorf("%s config dir = %q, want %q", tool.Name, got, tool.ConfigDir)
		}
		if got := engineCheck(t, "tool_adapters", tool.Name); got != strings.Join(tool.Adapters, " ") {
			t.Errorf("%s adapters = %q, want %v", tool.Name, got, tool.Adapters)
		}
		want := ""
		switch {
		case tool.StorageDirEnv != "":
			want = "/home/agent/.enclave-auth/.credentials.json"
		case len(tool.Files) > 0:
			want = "/home/agent/" + tool.ConfigDir + "/" + tool.Files[0].ConfigPath
		}
		if got := engineCheck(t, "tool_native_file", tool.Name); got != want {
			t.Errorf("%s native file = %q, want %q", tool.Name, got, want)
		}
		for _, adapter := range tool.Adapters {
			if got := engineCheck(t, "adapter_marker", adapter); got != adapterMarkers[adapter] {
				t.Errorf("%s marker = %q, want %q", adapter, got, adapterMarkers[adapter])
			}
		}
	}
	if got := engineCheck(t, "eval", `echo "$ALL_TOOLS"`); got != strings.Join(names, " ") {
		t.Errorf("ALL_TOOLS = %q, want %q", got, strings.Join(names, " "))
	}
}

// The engine check seeds these grants into an enrolled identity; a drifted
// schema would make every later check fail for the wrong reason.
func TestEngineCheckSyntheticGrantsAreAccepted(t *testing.T) {
	for _, tool := range protectedauth.Tools() {
		if len(tool.Adapters) == 0 {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			b := fixtureBroker(t)
			config := b.config
			config.Tool = tool.Name
			config.Identity = util.HashString(config.StoreDir)
			config.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
			if err := os.Mkdir(config.RuntimeDir, 0o700); err != nil {
				t.Fatal(err)
			}
			engineCheck(t, "synthetic_grants", tool.Name, config.StoreDir, strconv.FormatInt(b.now().Unix(), 10))
			reopened, err := Open(config)
			if err != nil {
				t.Fatalf("gateway rejected the engine check's grants: %v", err)
			}
			reopened.now = b.now
			view := filepath.Join(t.TempDir(), "view")
			if err := os.Mkdir(view, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := reopened.InitializeView(view); err != nil {
				t.Fatalf("gateway rejected the engine check's grants: %v", err)
			}
			var rendered strings.Builder
			entries, err := os.ReadDir(view)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(view, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				rendered.Write(data)
			}
			for _, adapter := range tool.Adapters {
				if !strings.Contains(rendered.String(), adapterMarkers[adapter]) {
					t.Errorf("view lacks the %s handle: %s", adapter, rendered.String())
				}
			}
			for _, marker := range []string{"synthetic-engine-check", "syntheticenginecheck"} {
				if strings.Contains(rendered.String(), marker) {
					t.Fatalf("view carries a synthetic secret: %s", rendered.String())
				}
			}
		})
	}
}
