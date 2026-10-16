// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package hosthooks implements the optional host lifecycle protocol, version 1.
package hosthooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

const (
	Version        = "1"
	Prepare        = "prepare"
	Enter          = "terminal-enter"
	Leave          = "terminal-leave"
	ChildEnv       = "ENCLAVE_INTERNAL_HOST_HOOK_CHILD"
	ReplayEnv      = "ENCLAVE_INTERNAL_HOST_HOOK_REPLAY"
	MaxMessage     = 64 * 1024
	MaxDiagnostics = 16 * 1024
	MaxPayload     = 4 * 1024
)

type Config struct {
	Enabled  *bool          `json:"enabled,omitempty"`
	Order    *int           `json:"order,omitempty"`
	Settings map[string]any `json:"settings,omitempty"`
}

type ConfigMap map[string]Config

var idPattern = regexp.MustCompile(`^(user|tool|feature):[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func ValidID(id string) bool { return id == "builtin:session-tint" || idPattern.MatchString(id) }

func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return fmt.Errorf("host hook entry must be an object")
	}
	for k, v := range raw {
		if string(v) == "null" {
			return fmt.Errorf("host hook %s must not be null", k)
		}
		if k != "enabled" && k != "order" && k != "settings" {
			return fmt.Errorf("unknown host hook field %q", k)
		}
	}
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*c = Config(value)
	return nil
}

// Merge inherits omitted fields; settings replace rather than recursively merge.
func Merge(base, incoming ConfigMap) ConfigMap {
	result := ConfigMap{}
	for id, c := range base {
		result[id] = c
	}
	for id, c := range incoming {
		old := result[id]
		if c.Enabled != nil {
			old.Enabled = c.Enabled
		}
		if c.Order != nil {
			old.Order = c.Order
		}
		if c.Settings != nil {
			old.Settings = c.Settings
		}
		result[id] = old
	}
	return result
}

type Tool struct {
	Name       string   `json:"name"`
	Entrypoint []string `json:"entrypoint"`
}
type Command struct {
	Argv     []string `json:"argv"`
	Explicit bool     `json:"explicit"`
}
type Project struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}
type Session struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
}
type Terminal struct {
	StdoutIsTTY     bool `json:"stdoutIsTTY"`
	ColorSuppressed bool `json:"colorSuppressed"`
}
type Outcome struct {
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode,omitempty"`
}
type Request struct {
	SchemaVersion string          `json:"schemaVersion"`
	Phase         string          `json:"phase"`
	InvocationID  string          `json:"invocationId"`
	ProviderID    string          `json:"providerId"`
	Action        string          `json:"action"`
	Backend       string          `json:"backend"`
	Background    bool            `json:"background"`
	Shell         bool            `json:"shell"`
	Tool          Tool            `json:"tool"`
	Command       Command         `json:"command"`
	Project       Project         `json:"project"`
	Session       *Session        `json:"session"`
	Terminal      Terminal        `json:"terminal"`
	Settings      map[string]any  `json:"settings"`
	AttachmentID  string          `json:"attachmentId,omitempty"`
	State         json.RawMessage `json:"state,omitempty"`
	Outcome       *Outcome        `json:"outcome,omitempty"`
}
type Environment struct {
	Set         map[string]string `json:"set,omitempty"`
	SetIfAbsent map[string]string `json:"setIfAbsent,omitempty"`
	Unset       []string          `json:"unset,omitempty"`
}
type Effect struct {
	Resource string `json:"resource"`
	Enter    string `json:"enter"`
	Leave    string `json:"leave"`
}
type Response struct {
	SchemaVersion      string          `json:"schemaVersion"`
	HostEnvironment    *Environment    `json:"hostEnvironment,omitempty"`
	RestartHostProcess bool            `json:"restartHostProcess,omitempty"`
	TerminalEffects    []Effect        `json:"terminalEffects,omitempty"`
	State              json.RawMessage `json:"state,omitempty"`
}
type Capability struct {
	ID     string   `json:"id"`
	Phases []string `json:"phases"`
}

func (e *Environment) UnmarshalJSON(data []byte) error {
	type plain Environment
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return fmt.Errorf("hostEnvironment must be an object")
	}
	for _, key := range []string{"set", "setIfAbsent", "unset"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		if string(raw) == "null" {
			return fmt.Errorf("%s must not be null", key)
		}
		if key == "unset" {
			var entries []json.RawMessage
			if err := json.Unmarshal(raw, &entries); err != nil {
				return err
			}
			for _, entry := range entries {
				if string(entry) == "null" {
					return fmt.Errorf("unset keys must be strings")
				}
			}
		} else {
			var entries map[string]json.RawMessage
			if err := json.Unmarshal(raw, &entries); err != nil {
				return err
			}
			for _, entry := range entries {
				if string(entry) == "null" {
					return fmt.Errorf("environment values must be strings")
				}
			}
		}
	}
	var result plain
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	*e = Environment(result)
	return nil
}
func (e *Effect) UnmarshalJSON(data []byte) error {
	type plain Effect
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return fmt.Errorf("terminal effect must be an object")
	}
	for _, key := range []string{"resource", "enter", "leave"} {
		raw, ok := fields[key]
		if !ok || string(raw) == "null" {
			return fmt.Errorf("terminal effect requires string %s", key)
		}
	}
	var result plain
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	*e = Effect(result)
	return nil
}
