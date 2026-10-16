// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"fmt"
	"sort"

	"enclave/internal/hosthooks"
	"enclave/internal/hosthooks/builtin"
	"enclave/internal/model"
)

func validateHostHooks(path string, defaults *Defaults) []string {
	var warnings []string
	validate := func(configs hosthooks.ConfigMap) {
		var ids []string
		for id := range configs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if !hosthooks.ValidID(id) {
				delete(configs, id)
				warnings = append(warnings, fmt.Sprintf("Ignoring host_hooks provider %q in %s: invalid provider ID", id, path))
			}
		}
	}
	validate(defaults.HostHooks)
	for name, override := range defaults.ToolOverrides {
		validate(override.HostHooks)
		defaults.ToolOverrides[name] = override
	}
	return warnings
}
func applyHostHookDefaults(opts *model.Options, defaults Defaults, source model.OptionSource, sources *model.OptionSources) {
	if len(defaults.HostHooks) == 0 {
		return
	}
	opts.HostHooks = hosthooks.Merge(opts.HostHooks, defaults.HostHooks)
	copied := map[string]map[string]model.OptionSource{}
	for id, fields := range opts.HostHookSources {
		copied[id] = map[string]model.OptionSource{}
		for k, v := range fields {
			copied[id][k] = v
		}
	}
	for id, c := range defaults.HostHooks {
		fields := copied[id]
		if fields == nil {
			fields = map[string]model.OptionSource{}
			copied[id] = fields
		}
		if c.Enabled != nil {
			fields["enabled"] = source
		}
		if c.Order != nil {
			fields["order"] = source
		}
		if c.Settings != nil {
			fields["settings"] = source
		}
	}
	opts.HostHookSources = copied
	sources.HostHooks = source
}
func hostHookValue(value any) (string, bool) {
	data, _ := json.Marshal(value)
	if string(data) == "null" {
		return "{}", false
	}
	return string(data), true
}

// EffectiveHostHooks isolates the legacy session_tint adapter from runtime.
func EffectiveHostHooks(opts model.Options) hosthooks.ConfigMap {
	return builtin.Compatibility(opts.HostHooks, opts.SessionTint)
}

func EffectiveHostHookSources(opts model.Options) map[string]map[string]model.OptionSource {
	sources := map[string]map[string]model.OptionSource{}
	if c := EffectiveHostHooks(opts)["builtin:session-tint"]; c.Enabled != nil && opts.SessionTint != "" {
		sources["builtin:session-tint"] = map[string]model.OptionSource{"enabled": opts.Sources.SessionTint, "settings": opts.Sources.SessionTint}
	}
	for id, fields := range opts.HostHookSources {
		if sources[id] == nil {
			sources[id] = map[string]model.OptionSource{}
		}
		for field, source := range fields {
			sources[id][field] = source
		}
	}
	return sources
}
