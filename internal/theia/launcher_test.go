// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package theia

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildArgs_NoPreferences(t *testing.T) {
	got, err := BuildArgs("enclave-claude-e4ae0b18-main", nil)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := []string{"--attach-container", "enclave-claude-e4ae0b18-main"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildArgs: got %v, want %v", got, want)
	}
}

func TestBuildArgs_PreferencesEncodedAndSorted(t *testing.T) {
	prefs := map[string]any{
		"b.key": "two words",
		"a.key": true,
		"c.key": 5,
	}
	got, err := BuildArgs("enclave-claude-e4ae0b18-main", prefs)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := []string{
		"--attach-container", "enclave-claude-e4ae0b18-main",
		"--session-preference", `a.key=true`,
		"--session-preference", `b.key="two words"`,
		"--session-preference", `c.key=5`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildArgs:\n got:  %v\n want: %v", got, want)
	}
}

func TestBuildArgs_EncodesNestedObjectPreference(t *testing.T) {
	prefs := map[string]any{
		"ai-features.chat.toolConfirmation": map[string]any{
			"shellExecute": "always_allow",
		},
	}
	got, err := BuildArgs("enclave-claude-abc12345-main", prefs)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := []string{
		"--attach-container", "enclave-claude-abc12345-main",
		"--session-preference", `ai-features.chat.toolConfirmation={"shellExecute":"always_allow"}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildArgs:\n got:  %v\n want: %v", got, want)
	}
}

func TestBuildArgs_DefaultPreferences(t *testing.T) {
	got, err := BuildArgs("enclave-claude-abc12345-main", DefaultPreferences)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := []string{
		"--attach-container", "enclave-claude-abc12345-main",
		"--session-preference", `ai-features.AiEnable.enableAI=true`,
		"--session-preference", `ai-features.agentMode.enabled=true`,
		"--session-preference", `ai-features.agentSettings={"Coder":{"capabilityOverrides":{"shell-execution":true}}}`,
		"--session-preference", `ai-features.chat.defaultChatAgent="Coder"`,
		"--session-preference", `ai-features.chat.defaultToolConfirmation="always_allow"`,
		"--session-preference", `ai-features.chat.toolConfirmation={"shellExecute":"always_allow"}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildArgs(defaults):\n got:  %v\n want: %v", got, want)
	}
}

func TestBuildArgs_RejectsBadContainerName(t *testing.T) {
	bad := []string{"", "-flag", "name with space", "name;pwn", "name`x`", "../escape"}
	for _, name := range bad {
		if _, err := BuildArgs(name, nil); err == nil {
			t.Fatalf("BuildArgs(%q) expected error", name)
		}
	}
}

func TestVariant_Valid(t *testing.T) {
	if !VariantTheia.Valid() || !VariantTheiaNext.Valid() {
		t.Fatal("expected both variants valid")
	}
	if Variant("vscode").Valid() {
		t.Fatal("vscode should not be a valid theia variant")
	}
}

func TestLogPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	got := LogPath("/home/u", "enclave-claude-abc12345-main")
	want := "/home/u/.local/state/enclave/logs/theia/enclave-claude-abc12345-main.log"
	if got != want {
		t.Fatalf("LogPath: got %q, want %q", got, want)
	}
}

func TestLaunch_UnsupportedVariant(t *testing.T) {
	err := Launch(Variant("emacs"), "enclave-claude-aaaaaaaa-main", nil, "")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("Launch: got %v, want unsupported variant error", err)
	}
}

func TestRedactArgs_MasksTokenLeavesInputUntouched(t *testing.T) {
	in := []string{
		"--attach-container", "enclave-theia-abc",
		"--session-preference", "externalApi.port=3333",
		"--session-preference", `externalApi.token="s3cret"`,
	}
	got := redactArgs(in)
	want := []string{
		"--attach-container", "enclave-theia-abc",
		"--session-preference", "externalApi.port=3333",
		"--session-preference", "externalApi.token=<redacted>",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redactArgs: got %v, want %v", got, want)
	}
	if in[5] != `externalApi.token="s3cret"` {
		t.Fatalf("redactArgs mutated its input: %v", in)
	}
}
