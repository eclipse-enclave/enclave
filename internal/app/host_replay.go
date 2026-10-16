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

	"enclave/internal/config"
	"enclave/internal/hosthooks"
	"enclave/internal/model"
)

type replayRecord struct {
	WarningKeys   []string           `json:"warningKeys,omitempty"`
	Version       int                `json:"version"`
	PID           int                `json:"pid"`
	InvocationID  string             `json:"invocationId"`
	Tool          string             `json:"tool"`
	Backend       string             `json:"backend"`
	ToolSource    model.OptionSource `json:"toolSource"`
	BackendSource model.OptionSource `json:"backendSource"`
}

func consumeHostReplay() *replayRecord {
	raw := os.Getenv(hosthooks.ReplayEnv)
	_ = os.Unsetenv(hosthooks.ReplayEnv)
	if raw == "" {
		return nil
	}
	if len(raw) > 8192 {
		hookWarning("ignoring oversized internal replay record")
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	var record replayRecord
	if err != nil || json.Unmarshal(data, &record) != nil || record.Version != 1 || record.PID != os.Getpid() || record.Tool == "" || record.Backend == "" || record.InvocationID == "" || config.ValidateExtensionName(record.Tool) != nil || validateBackendName(record.Backend) != nil || record.ToolSource > model.SourceCLI || record.BackendSource > model.SourceCLI {
		hookWarning("ignoring invalid internal replay record")
		return nil
	}
	return &record
}
func replayHost(args, env []string, record replayRecord) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return fmt.Errorf("internal replay record exceeds limit")
	}
	values := hosthooks.EnvironmentMap(env)
	values[hosthooks.ReplayEnv] = base64.RawURLEncoding.EncodeToString(data)
	argv := append([]string{exe}, args...)
	return hostReexec(exe, argv, hosthooks.EnvironmentList(values))
}
