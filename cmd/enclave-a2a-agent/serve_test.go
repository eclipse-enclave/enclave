// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

func serveEnv(role string) []string {
	return []string{
		a2a.EnvRole + "=" + role,
		a2a.EnvGroup + "=review",
		a2a.EnvGroupGeneration + "=00000000000000ff",
		a2a.EnvMember + "=worker-1",
		a2a.EnvMemberGeneration + "=0000000000000abc",
		a2a.EnvHubHost + "=a2a.enclave",
		a2a.EnvHubPort + "=41820",
		a2a.EnvInboundToken + "=in",
		a2a.EnvOutboundToken + "=out",
		a2a.EnvAdapterPort + "=41821",
		a2a.EnvTableDir + "=/tmp/a2a/0000000000000abc",
	}
}

func TestServeRefusesBeforeStarting(t *testing.T) {
	if err := serve(context.Background(), nil); err == nil || !strings.Contains(err.Error(), a2a.EnvRole) {
		t.Fatalf("empty environment: %v", err)
	}
	if err := serve(context.Background(), serveEnv("orchestrator")); err == nil || !strings.Contains(err.Error(), "only workers") {
		t.Fatalf("orchestrator: %v", err)
	}
	if err := serve(context.Background(), serveEnv("worker")); !errors.Is(err, errNoDriver) {
		t.Fatalf("worker without a driver: %v", err)
	}
	if code := runServe([]string{"extra"}); code != 2 {
		t.Fatalf("runServe with arguments: exit %d, want 2", code)
	}
}

const claudeBlock = `{"command":["claude","-p","--output-format","stream-json","--verbose"],` +
	`"newSessionArgs":["--session-id","{sessionId}"],"resumeArgs":["--resume","{sessionId}"],` +
	`"eventFormat":"claude-stream-json","skills":[{"id":"coding","name":"Coding","description":"Writes code"}]}`

const claudeYolo = `,"yoloFlag":"--dangerously-skip-permissions"}`

func TestNewDriverFromHarnessEnv(t *testing.T) {
	block := strings.TrimSuffix(claudeBlock, "}") + claudeYolo
	env := []string{a2a.EnvHarness + "=" + block, a2a.EnvYolo + "=1"}
	driver, skills, err := newDriver(env, io.Discard)
	if err != nil {
		t.Fatalf("newDriver: %v", err)
	}
	if driver.Name() != "claude-stream-json" {
		t.Fatalf("driver %q", driver.Name())
	}
	if len(skills) != 1 || skills[0].ID != "coding" || skills[0].Tags == nil {
		t.Fatalf("skills: %+v", skills)
	}
	argv := func(d a2aagent.Driver) string {
		t.Helper()
		cmd, err := d.Command(context.Background(), a2aagent.Invocation{HarnessSessionID: "123e4567-e89b-42d3-a456-426614174000"})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(cmd.Args, " ")
	}
	want := "claude -p --output-format stream-json --verbose --dangerously-skip-permissions --resume 123e4567-e89b-42d3-a456-426614174000"
	if got := argv(driver); got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}

	plain := "claude -p --output-format stream-json --verbose --resume 123e4567-e89b-42d3-a456-426614174000"
	for name, env := range map[string][]string{
		"no yolo":      {a2a.EnvHarness + "=" + block},
		"yolo off":     {a2a.EnvHarness + "=" + block, a2a.EnvYolo + "=0"},
		"no yolo flag": {a2a.EnvHarness + "=" + claudeBlock, a2a.EnvYolo + "=1"},
	} {
		var log strings.Builder
		d, _, err := newDriver(env, &log)
		if err != nil || !strings.Contains(log.String(), "without bypassed permissions") {
			t.Fatalf("%s: err %v, log %q", name, err, log.String())
		}
		if got := argv(d); got != plain {
			t.Fatalf("%s: argv = %q, want %q", name, got, plain)
		}
	}
	refusals := map[string][]string{
		"no block":       nil,
		"not json":       {a2a.EnvHarness + "=command: claude"},
		"unknown field":  {a2a.EnvHarness + `={"command":["c"],"resumeArgs":["{sessionId}"],"eventFormat":"claude-stream-json","extra":1}`},
		"unknown format": {a2a.EnvHarness + `={"command":["c"],"resumeArgs":["{sessionId}"],"eventFormat":"jsonl"}`},
	}
	for name, env := range refusals {
		if _, _, err := newDriver(env, io.Discard); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestTranscriptRoot(t *testing.T) {
	store := filepath.Join(string(filepath.Separator), "home", "u", ".claude")
	cases := map[string]string{
		filepath.Join(store, "a2a", "0000000000000abc"):       store,
		filepath.Join(store, "a2a", "0000000000000abc") + "/": store,
		filepath.Join(store, "other", "0000000000000abc"):     "",
		filepath.Join(store, "a2a", "ffffffffffffffff"):       "",
	}
	for dir, want := range cases {
		cfg := a2aagent.Config{TableDir: dir, MemberGeneration: "0000000000000abc"}
		if got := transcriptRoot(cfg); got != want {
			t.Errorf("transcriptRoot(%q) = %q, want %q", dir, got, want)
		}
	}
}
