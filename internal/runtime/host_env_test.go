// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"testing"

	"enclave/internal/model"
)

func TestAgentHostEnvSkipsShells(t *testing.T) {
	env := map[string]string{"HERDR_AGENT": "claude"}
	if got := (&Runtime{run: model.RunOptions{HostEnv: env}}).agentHostEnv(); got["HERDR_AGENT"] != "claude" {
		t.Fatalf("agent run host_env = %v", got)
	}
	if got := (&Runtime{run: model.RunOptions{HostEnv: env, Shell: true}}).agentHostEnv(); got != nil {
		t.Fatalf("shell host_env = %v, want none", got)
	}
}
