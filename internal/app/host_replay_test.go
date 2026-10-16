// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/hosthooks"
	"enclave/internal/model"
)

func TestHostReplayExactArgsAndFailureRollback(t *testing.T) {
	old := hostReexec
	defer func() { hostReexec = old }()
	args := []string{"--tool", "claude", "exec", "--", "codex", "space value", "$(literal)", "--tool=not-enclave"}
	record := replayRecord{Version: 1, PID: os.Getpid(), InvocationID: "id", Tool: "claude", Backend: "docker", ToolSource: model.SourceGlobal}
	t.Setenv("REPLAY_TEST", "original")
	hostReexec = func(_ string, argv, env []string) error {
		if !reflect.DeepEqual(argv[1:], args) {
			t.Fatalf("argv=%q", argv)
		}
		values := hosthooks.EnvironmentMap(env)
		t.Setenv(hosthooks.ReplayEnv, values[hosthooks.ReplayEnv])
		replay := consumeHostReplay()
		if replay == nil || replay.ToolSource != model.SourceGlobal || replay.PID != os.Getpid() {
			t.Fatal(replay)
		}
		if _, present := os.LookupEnv(hosthooks.ReplayEnv); present {
			t.Fatal("replay record leaked")
		}
		return fmt.Errorf("simulated exec failure")
	}
	if err := replayHost(args, []string{"REPLAY_TEST=pending"}, record); err == nil {
		t.Fatal("expected failure")
	}
	if os.Getenv("REPLAY_TEST") != "original" {
		t.Fatal("pending env leaked after failure")
	}
}
func TestReplayRecordRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"bad", strings.Repeat("x", 9000), base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"pid":0}`))} {
		t.Setenv(hosthooks.ReplayEnv, raw)
		if got := consumeHostReplay(); got != nil {
			t.Fatal(got)
		}
	}
}

// This helper reexecutes the test binary itself, proving PID, argument, initial
// environment and unread stdin preservation through the actual Unix exec seam.
func TestHostReplayProcess(t *testing.T) {
	if os.Getenv("HOOK_REPLAY_TEST_HELPER") == "1" {
		record := consumeHostReplay()
		if record == nil {
			record = &replayRecord{Version: 1, PID: os.Getpid(), InvocationID: "process", Tool: "claude", Backend: "docker"}
			env := append(os.Environ(), "REPLAY_AT_START=yes")
			if err := replayHost(os.Args[1:], env, *record); err != nil {
				t.Fatal(err)
			}
			t.Fatal("exec returned")
		}
		data := make([]byte, 64)
		n, err := os.Stdin.Read(data)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]any{"pid": os.Getpid(), "recordPID": record.PID, "input": string(data[:n]), "initialEnv": os.Getenv("REPLAY_AT_START"), "args": os.Args[len(os.Args)-3:]}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostReplayProcess$", "--", "space value", "$(literal)", "--tool=not-enclave")
	cmd.Env = append(os.Environ(), "HOOK_REPLAY_TEST_HELPER=1")
	cmd.Stdin = strings.NewReader("unread agent input")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		PID        int      `json:"pid"`
		RecordPID  int      `json:"recordPID"`
		Input      string   `json:"input"`
		InitialEnv string   `json:"initialEnv"`
		Args       []string `json:"args"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("%s: %v", output, err)
	}
	if result.PID != cmd.Process.Pid || result.RecordPID != result.PID || result.Input != "unread agent input" || result.InitialEnv != "yes" || !reflect.DeepEqual(result.Args, []string{"space value", "$(literal)", "--tool=not-enclave"}) {
		t.Fatalf("replay=%+v", result)
	}
}
