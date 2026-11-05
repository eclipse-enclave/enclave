// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2aagent

import (
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
)

func workerEnv() []string {
	return []string{
		"PATH=/usr/bin",
		a2a.EnvRole + "=worker",
		a2a.EnvGroup + "=review",
		a2a.EnvGroupGeneration + "=00000000000000ff",
		a2a.EnvMember + "=worker-1",
		a2a.EnvMemberGeneration + "=0000000000000abc",
		a2a.EnvHubHost + "=a2a.enclave",
		a2a.EnvHubPort + "=41820",
		a2a.EnvInboundToken + "=in",
		a2a.EnvOutboundToken + "=out",
		a2a.EnvAdapterPort + "=41821",
		a2a.EnvTableDir + "=/home/u/.claude/a2a/0000000000000abc",
	}
}

func TestLoadConfigWorker(t *testing.T) {
	env := append(workerEnv(),
		a2a.EnvQueueDepth+"=4",
		a2a.EnvTaskDeadline+"=10m",
		a2a.EnvReconnectWindow+"=45s",
		a2a.EnvMaxRequestBytes+"=2048",
		a2a.EnvDescription+"=reviews diffs",
	)
	cfg, err := LoadConfig(env)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Role != a2a.RoleWorker || cfg.Member != "worker-1" || cfg.HubPort != 41820 || cfg.AdapterPort != 41821 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.QueueDepth != 4 || cfg.TaskDeadline != 10*time.Minute || cfg.ReconnectWindow != 45*time.Second ||
		cfg.MaxRequestBytes != 2048 || cfg.Description != "reviews diffs" {
		t.Fatalf("bounds not parsed: %+v", cfg)
	}
	if cfg.ContextTTL != 0 || cfg.RecordLimit != 0 {
		t.Fatalf("absent bounds must stay zero: %+v", cfg)
	}
}

func TestLoadConfigOrchestratorNeedsNoWorkerFields(t *testing.T) {
	var env []string
	for _, kv := range workerEnv() {
		if strings.HasPrefix(kv, a2a.EnvOutboundToken) || strings.HasPrefix(kv, a2a.EnvAdapterPort) || strings.HasPrefix(kv, a2a.EnvTableDir) {
			continue
		}
		env = append(env, strings.Replace(kv, "=worker", "=orchestrator", 1))
	}
	env = append(env, a2a.EnvSendWait+"=90s")
	cfg, err := LoadConfig(env)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Role != a2a.RoleOrchestrator || cfg.SendWait != 90*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	// CR-A11-1: the orchestrator's send journal lives in its table directory.
	cfg, err = LoadConfig(append(env, a2a.EnvTableDir+"=/home/u/.claude/a2a/0000000000000abc"))
	if err != nil || cfg.TableDir != "/home/u/.claude/a2a/0000000000000abc" {
		t.Fatalf("orchestrator table dir: %+v, %v", cfg, err)
	}
	for _, required := range []string{a2a.EnvHubHost, a2a.EnvHubPort, a2a.EnvInboundToken, a2a.EnvMember,
		a2a.EnvMemberGeneration, a2a.EnvGroup, a2a.EnvGroupGeneration} {
		var without []string
		for _, kv := range env {
			if !strings.HasPrefix(kv, required+"=") {
				without = append(without, kv)
			}
		}
		if _, err := LoadConfig(without); err == nil || !strings.Contains(err.Error(), required+" is required") {
			t.Errorf("orchestrator without %s: %v", required, err)
		}
	}
}

func TestLoadConfigRejects(t *testing.T) {
	cases := map[string]struct {
		set  string
		drop string
		want string
	}{
		"missing role":          {drop: a2a.EnvRole, want: a2a.EnvRole + " is required"},
		"bad role":              {set: a2a.EnvRole + "=host", want: "must be orchestrator or worker"},
		"bad group":             {set: a2a.EnvGroup + "=Review!", want: "not a valid group name"},
		"bad generation":        {set: a2a.EnvMemberGeneration + "=abc", want: "16 lowercase hex"},
		"missing outbound":      {drop: a2a.EnvOutboundToken, want: a2a.EnvOutboundToken + " is required"},
		"missing table dir":     {drop: a2a.EnvTableDir, want: a2a.EnvTableDir + " is required"},
		"bad port":              {set: a2a.EnvHubPort + "=70000", want: "must be a TCP port"},
		"negative count":        {set: a2a.EnvQueueDepth + "=-1", want: "non-negative decimal"},
		"bad duration":          {set: a2a.EnvTaskDeadline + "=soon", want: "non-negative Go duration"},
		"missing inbound token": {drop: a2a.EnvInboundToken, want: a2a.EnvInboundToken + " is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var env []string
			for _, kv := range workerEnv() {
				key, _, _ := strings.Cut(kv, "=")
				if key == tc.drop {
					continue
				}
				if setKey, _, _ := strings.Cut(tc.set, "="); setKey == key {
					continue
				}
				env = append(env, kv)
			}
			if tc.set != "" {
				env = append(env, tc.set)
			}
			_, err := LoadConfig(env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
