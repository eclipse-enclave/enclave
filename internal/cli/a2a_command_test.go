// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestParseA2AVerbs(t *testing.T) {
	cases := []struct {
		args  []string
		check func(t *testing.T, res Result)
	}{
		{[]string{"a2a", "peers", "--json", "--peer-group", "review"}, func(t *testing.T, res Result) {
			if res.A2A.Verb != A2AVerbPeers || !res.A2A.JSON || res.Options.PeerGroup != "review" || res.Sources.PeerGroup != model.SourceCLI {
				t.Fatalf("%+v %q", res.A2A, res.Options.PeerGroup)
			}
		}},
		{[]string{"a2a", "log", "--tail", "20"}, func(t *testing.T, res Result) {
			if res.A2A.Verb != A2AVerbLog || res.A2A.Tail != 20 {
				t.Fatalf("%+v", res.A2A)
			}
		}},
		{[]string{"a2a", "send", "worker-1", "review the diff", "--detach", "--context", "ctx:worker-1:a1b2c3d4e5f60718:c1"}, func(t *testing.T, res Result) {
			r := res.A2A
			if r.Verb != A2AVerbSend || r.Peer != "worker-1" || r.Text != "review the diff" || !r.Detach || r.Context == "" || r.Wait != nil {
				t.Fatalf("%+v", r)
			}
		}},
		{[]string{"a2a", "send", "worker-1", "-", "--wait", "90s", "--backend", "podman"}, func(t *testing.T, res Result) {
			if res.A2A.Text != "-" || res.A2A.Wait == nil || *res.A2A.Wait != 90*time.Second || res.Options.Backend != "podman" {
				t.Fatalf("%+v", res.A2A)
			}
		}},
		{[]string{"a2a", "task", "get", "task:w:a1b2c3d4e5f60718:t1", "--wait", "0s"}, func(t *testing.T, res Result) {
			if res.A2A.Verb != A2AVerbTaskGet || res.A2A.Handle != "task:w:a1b2c3d4e5f60718:t1" || res.A2A.Wait == nil {
				t.Fatalf("%+v", res.A2A)
			}
		}},
		{[]string{"a2a", "task", "cancel", "task:w:a1b2c3d4e5f60718:t1", "--json"}, func(t *testing.T, res Result) {
			if res.A2A.Verb != A2AVerbTaskCancel || !res.A2A.JSON {
				t.Fatalf("%+v", res.A2A)
			}
		}},
		{[]string{"a2a", "task", "retry", "task:w:a1b2c3d4e5f60718:t1"}, func(t *testing.T, res Result) {
			if res.A2A.Verb != A2AVerbTaskRetry {
				t.Fatalf("%+v", res.A2A)
			}
		}},
		{[]string{"a2a", "reconcile"}, func(t *testing.T, res Result) {
			if res.A2A.Verb != A2AVerbReconcile || res.Sources.PeerGroup == model.SourceCLI {
				t.Fatalf("%+v", res.A2A)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			res, err := Parse(tc.args, config.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != ActionA2A || res.A2A == nil {
				t.Fatalf("action %q request %+v", res.Action, res.A2A)
			}
			tc.check(t, res)
		})
	}
}

func TestParseA2AVerbErrors(t *testing.T) {
	for _, args := range [][]string{
		{"a2a", "send", "worker-1"},
		{"a2a", "send", "worker-1", "x", "--detach", "--wait", "1m"},
		{"a2a", "send", "worker-1", "x", "--wait", "-1s"},
		{"a2a", "log", "--tail", "-1"},
		{"a2a", "peers", "--peer-group", "Bad_Name"},
		{"a2a", "task", "get"},
		{"a2a", "nope"},
		{"a2a", "peers", "--backend", "docker"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := Parse(args, config.DefaultOptions()); err == nil {
				t.Fatal("expected a parse error")
			}
		})
	}
}

func TestA2AFlagCompletions(t *testing.T) {
	var res Result
	rootCmd := newRootCommand(&res)
	runCmd := findSubCommand(rootCmd, "run")
	if runCmd == nil {
		t.Fatal("run command not found")
	}
	for flag, want := range map[string][]string{
		"role":    {config.A2ARoleOrchestrator, config.A2ARoleWorker},
		"a2a-log": {config.A2ALogCoarse, config.A2ALogFull},
	} {
		got, _ := completeFlag(t, runCmd, flag)
		if !slices.Equal(got, want) {
			t.Fatalf("--%s completions = %v, want %v", flag, got, want)
		}
	}
}
