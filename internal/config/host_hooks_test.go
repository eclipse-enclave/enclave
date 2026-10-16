// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"testing"

	"enclave/internal/hosthooks"
	"enclave/internal/model"
)

func hookDefaults(t *testing.T, data string) Defaults {
	t.Helper()
	var d Defaults
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		t.Fatal(err)
	}
	return d
}
func TestHostHooksLayersAndCompatibility(t *testing.T) {
	global := hookDefaults(t, `{"host_hooks":{"user:a":{"enabled":true,"order":3,"settings":{"old":true}},"user:b":{"enabled":true}},"tool_overrides":{"claude":{"host_hooks":{"user:a":{"settings":{"tool":"global"}}}}},"session_tint":"#123456"}`)
	project := hookDefaults(t, `{"host_hooks":{"user:a":{"enabled":false,"settings":{}}},"tool_overrides":{"claude":{"host_hooks":{"user:a":{"order":0,"settings":{}}}}}}`)
	opts, _, _ := ResolveOptionsForTool(model.Options{}, model.OptionSources{}, global, project, "claude")
	c := opts.HostHooks["user:a"]
	if c.Enabled == nil || *c.Enabled || c.Order == nil || *c.Order != 0 || c.Settings == nil || len(c.Settings) != 0 {
		t.Fatalf("merge=%+v", c)
	}
	if opts.HostHookSources["user:a"]["enabled"] != model.SourceProject || opts.HostHookSources["user:a"]["settings"] != model.SourceToolOverride {
		t.Fatal(opts.HostHookSources)
	}
	if !*opts.HostHooks["user:b"].Enabled {
		t.Fatal("lost inherited provider")
	}
	effective := EffectiveHostHooks(opts)
	if !*effective["builtin:session-tint"].Enabled || effective["builtin:session-tint"].Settings["color"] != "#123456" {
		t.Fatal(effective)
	}
	disabled := false
	opts.HostHooks["builtin:session-tint"] = hosthooks.Config{Enabled: &disabled}
	if *EffectiveHostHooks(opts)["builtin:session-tint"].Enabled {
		t.Fatal("compatibility override ignored")
	}
	empty := hosthooks.Merge(opts.HostHooks, hosthooks.ConfigMap{})
	if len(empty) != len(opts.HostHooks) {
		t.Fatal(empty)
	}
}
func TestHostHooksRejectMalformedEntries(t *testing.T) {
	for _, data := range []string{`null`, `{"enabled":null}`, `{"enabled":"yes"}`, `{"order":1.5}`, `{"settings":[]}`, `{"settings":null}`, `{"unknown":true}`} {
		var c hosthooks.Config
		if err := json.Unmarshal([]byte(data), &c); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}
