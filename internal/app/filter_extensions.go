// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"

	"enclave/internal/config"
	"enclave/internal/extinstall"
	"enclave/internal/filterhost"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// runFilterExtensions lists the installed request filters.
func runFilterExtensions(input *CommandInput) int {
	ctx := input.Ctx
	exts, err := filterhost.List(ctx.Paths, warnStderr)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	inventory, err := extensionInventory(ctx.Paths, model.KindFilter, exts)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	// Enabled means selected by the effective filters option of a run with
	// these options: the config layers, the tool's tool_overrides, and
	// --filters.
	selected := map[string]bool{}
	selections, err := filterhost.ParseSelection(input.Options.Filters)
	if err != nil {
		logx.Warnf("A run with these options would fail: %v", err)
	}
	for _, sel := range selections {
		selected[sel.Name] = true
	}
	enabled := func(name string) bool { return selected[name] }

	if input.ExtRequest != nil && input.ExtRequest.JSON {
		selection := &extensionListSelection{
			Directives: input.Options.Filters,
			Source:     sourceLabel(input.Sources.Filters),
		}
		if !toolUnset(input.Options.Tool) {
			selection.Tool = &input.Options.Tool
		}
		entries := filterListJSONEntries(ctx, exts, inventory, enabled)
		if err := renderExtensionListJSON(os.Stdout, model.KindFilter, selection, entries); err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		return 0
	}

	if len(exts) == 0 {
		fmt.Printf("No filters installed (add one with `%s filters add <source>`)\n", model.AppName)
		return 0
	}
	for _, ext := range exts {
		mark := " "
		if enabled(ext.Name) {
			mark = "✓"
		}
		fmt.Printf("%s %s%s\n", mark, ext.Name, listingSuffix(inventory[ext.Name]))
	}
	return 0
}

// filterListJSONEntries is extensionListJSONEntries plus the filter-only
// fields.
func filterListJSONEntries(ctx *AppContext, exts []model.Extension, inventory map[string]extinstall.Managed, enabled func(string) bool) []extensionListJSONExt {
	entries := extensionListJSONEntries(exts, inventory, enabled)
	home := ""
	if host, err := ctx.Host(); err == nil {
		home = host.Home
	}
	for i, ext := range exts {
		f, err := filterhost.Load(ctx.Paths, ext.Name)
		if err != nil {
			continue
		}
		entry := &entries[i]
		entry.GovernedHosts = append([]string{}, f.Hosts...)
		entry.ConfinedDomains = append([]string(nil), f.Confine...)
		entry.CredentialType = f.Credential.Type
		if o := f.Credential.OAuth2; o != nil {
			entry.CredentialEndpoints = &filterCredentialEndpoints{AuthURL: o.AuthURL, TokenURL: o.TokenURL, RevokeURL: o.RevokeURL}
		}
		if s := f.Credential.Static; s != nil {
			entry.CredentialSources = &filterCredentialSources{Env: append([]string(nil), s.Env...), File: s.File, Priority: s.Priority}
		}
		entry.DefaultPreset = f.DefaultPreset
		userDir := ""
		if home != "" {
			userDir = config.HostFilterPresetsDir(home, f.Name)
		}
		presets, err := f.Presets(userDir)
		if err != nil {
			continue
		}
		entry.Presets = []string{}
		for _, p := range presets {
			entry.Presets = append(entry.Presets, p.Name)
		}
	}
	return entries
}

// warnStderr reports a non-fatal listing problem on stderr, so a `--json`
// envelope on stdout stays parseable.
func warnStderr(msg string) {
	logx.Warnf("%s", msg)
}
