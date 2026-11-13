// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"enclave/internal/filter"
	"enclave/internal/filter/spec"
	"enclave/internal/model"
)

// filterCapabilities is what a request filter lets a session do: which hosts
// it governs, which credential it injects there, and which operations its
// capabilities allow.
type filterCapabilities struct {
	Hosts []string
	// Confine lists the domains whose ungoverned names the filter blocks.
	Confine        []string
	CredentialType string
	// Credential is the credential type plus where it is injected.
	Credential string
	// Scopes is the union of every OAuth scope a preset can request.
	Scopes []string
	// Env are the container env vars that receive the placeholder.
	Env []string
	// AuthURL, TokenURL, and RevokeURL are the OAuth endpoints. The token
	// endpoint receives the refresh token and the client secret.
	AuthURL   string
	TokenURL  string
	RevokeURL string
	// StaticEnv and StaticFile are where the host value of a static
	// credential is read. StaticFile is the path plus its parser.
	StaticEnv  []string
	StaticFile string
	// Capabilities lists every capability, public ones marked.
	Capabilities []string
	// Operations maps each route to its capability as "op -> capability",
	// so an update that adds a route to an existing capability shows up.
	Operations []string
	// Presets lists the shipped presets, the default one marked.
	Presets []string
	// PresetGrants maps each shipped preset to what it grants, resolved as
	// at session start. A grant that covers a preset keeps covering it when
	// an update widens the preset, so every change is diffed.
	PresetGrants map[string]presetGrants
}

// presetGrants is what a resolved preset lets a session do.
type presetGrants struct {
	// Capabilities maps each enabled capability to its constraint values,
	// formatted by describeConstraintValue.
	Capabilities map[string]map[string]string
	// Quotas maps counters to per-session limits. A missing counter is
	// unlimited.
	Quotas map[string]int64
}

// inspectFilter summarizes a filter directory through the filter loader.
func inspectFilter(dir string, name string) (*filterCapabilities, error) {
	f, err := spec.Load(dir, name)
	if err != nil {
		return nil, err
	}
	caps := &filterCapabilities{
		Hosts:          append([]string(nil), f.Hosts...),
		Confine:        append([]string(nil), f.Confine...),
		CredentialType: f.Credential.Type,
		Credential:     describeFilterCredential(f.Credential),
		Env:            append([]string(nil), f.Credential.Env...),
	}
	if o := f.Credential.OAuth2; o != nil {
		caps.AuthURL, caps.TokenURL, caps.RevokeURL = o.AuthURL, o.TokenURL, o.RevokeURL
	}
	if s := f.Credential.Static; s != nil {
		caps.StaticEnv = append([]string(nil), s.Env...)
		if s.File != nil {
			caps.StaticFile = describeStaticFile(*s.File)
		}
	}
	for _, capName := range filter.SortedKeys(f.Capabilities) {
		c := f.Capabilities[capName]
		label := capName
		if c.Public {
			label += " (public)"
		}
		caps.Capabilities = append(caps.Capabilities, label)
		caps.Scopes = append(caps.Scopes, c.Scopes...)
	}
	for _, r := range f.Resolvers {
		caps.Scopes = append(caps.Scopes, r.Scopes...)
	}
	caps.Scopes = dedupeSorted(caps.Scopes)
	for _, r := range f.Routes {
		target := r.Capability
		if r.Action == filter.ActionDeny {
			target = "deny"
		}
		caps.Operations = append(caps.Operations, r.Op+" -> "+target)
	}
	sort.Strings(caps.Operations)
	presets, err := f.Presets("")
	if err != nil {
		return nil, err
	}
	caps.PresetGrants = map[string]presetGrants{}
	for _, info := range presets {
		label := info.Name
		if info.Name == f.DefaultPreset {
			label += " (default)"
		}
		caps.Presets = append(caps.Presets, label)
		p, err := f.ResolvePreset(info.Name, "")
		if err != nil {
			return nil, err
		}
		caps.PresetGrants[info.Name] = describePresetGrants(p)
	}
	return caps, nil
}

func describePresetGrants(p *spec.Preset) presetGrants {
	g := presetGrants{Capabilities: map[string]map[string]string{}, Quotas: maps.Clone(p.Quotas)}
	for capName, values := range p.Capabilities {
		described := map[string]string{}
		for constraint, value := range values {
			described[constraint] = describeConstraintValue(value)
		}
		g.Capabilities[capName] = described
	}
	return g
}

// describeConstraintValue formats a constraint value as a preset writes it,
// lists sorted: "*", ["a", "b"], or {ids: ["a"], within: ["b"]}.
func describeConstraintValue(v filter.ConstraintValue) string {
	if v.Any {
		return `"*"`
	}
	if len(v.IDs)+len(v.Within) == 0 {
		return quotedList(v.Values)
	}
	var parts []string
	if len(v.IDs) > 0 {
		parts = append(parts, "ids: "+quotedList(v.IDs))
	}
	if len(v.Within) > 0 {
		parts = append(parts, "within: "+quotedList(v.Within))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func quotedList(values []string) string {
	sorted := slices.Compact(slices.Sorted(slices.Values(values)))
	quoted := make([]string, 0, len(sorted))
	for _, value := range sorted {
		quoted = append(quoted, strconv.Quote(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func describeStaticFile(file model.SecretFileSource) string {
	if file.Parser == "" {
		return file.Path
	}
	return file.Path + " (" + file.Parser + ")"
}

// endpointHost is the host of an OAuth endpoint URL, which spec loading has
// validated as https.
func endpointHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

// credentialEndpoints are the OAuth endpoints with what each one receives.
var credentialEndpoints = []struct {
	label    string
	url      func(*filterCapabilities) string
	receives string
}{
	{"token endpoint", func(f *filterCapabilities) string { return f.TokenURL }, "receives the refresh token and client secret"},
	{"auth endpoint", func(f *filterCapabilities) string { return f.AuthURL }, "opened in your browser by `" + model.AppName + " filters login`"},
	{"revoke endpoint", func(f *filterCapabilities) string { return f.RevokeURL }, "receives the refresh token at logout"},
}

func describeFilterCredential(c spec.Credential) string {
	header := c.Inject.Header
	if c.Inject.Format != "" {
		header += ": " + strings.ReplaceAll(c.Inject.Format, "%s", "<credential>")
	}
	return fmt.Sprintf("%s, injected as %s", c.Type, header)
}

// renderRows adds the filter rows of the capability summary.
func (f *filterCapabilities) renderRows(row func(label string, value string)) {
	if f == nil {
		return
	}
	hosts := strings.Join(f.Hosts, ", ")
	if hosts != "" {
		hosts += " (with subdomains, every request is inspected, the credential is added to allowed ones)"
	}
	row("governed hosts", hosts)
	confine := strings.Join(f.Confine, ", ")
	if confine != "" {
		confine += " (names below that no filter governs are blocked)"
	}
	row("confined domains", confine)
	row("credential", f.Credential)
	for _, endpoint := range credentialEndpoints {
		if host := endpointHost(endpoint.url(f)); host != "" {
			row(endpoint.label, host+" ("+endpoint.receives+")")
		}
	}
	staticEnv := strings.Join(f.StaticEnv, ", ")
	if staticEnv != "" {
		staticEnv += " (host env or Enclave secrets, read for the credential)"
	}
	row("credential env", staticEnv)
	staticFile := f.StaticFile
	if staticFile != "" {
		staticFile += " (host file, read for the credential)"
	}
	row("credential file", staticFile)
	row("oauth scopes", strings.Join(f.Scopes, ", "))
	env := strings.Join(f.Env, ", ")
	if env != "" {
		env += " (receive a placeholder, never the credential)"
	}
	row("placeholder env", env)
	row("capabilities", strings.Join(f.Capabilities, ", "))
	row("presets", strings.Join(f.Presets, ", "))
}

// diffFilter reports what an update changes about a filter.
func diffFilter(before *filterCapabilities, after *filterCapabilities) []string {
	if before == nil {
		before = &filterCapabilities{}
	}
	if after == nil {
		after = &filterCapabilities{}
	}
	// Where the credential comes from and goes to leads: a changed endpoint
	// host or source is what a malicious update would change.
	var changes []string
	for _, endpoint := range credentialEndpoints {
		changes = append(changes, diffEndpoint(endpoint.label, endpoint.url(before), endpoint.url(after), endpoint.receives)...)
	}
	changes = append(changes, diffList("credential env var", before.StaticEnv, after.StaticEnv)...)
	changes = append(changes, diffScalar("credential file", before.StaticFile, after.StaticFile)...)
	changes = append(changes, diffList("governed host", before.Hosts, after.Hosts)...)
	changes = append(changes, diffList("confined domain", before.Confine, after.Confine)...)
	changes = append(changes, diffScalar("credential", before.Credential, after.Credential)...)
	changes = append(changes, diffList("oauth scope", before.Scopes, after.Scopes)...)
	changes = append(changes, diffList("placeholder env var", before.Env, after.Env)...)
	changes = append(changes, diffList("capability", before.Capabilities, after.Capabilities)...)
	changes = append(changes, diffList("operation", before.Operations, after.Operations)...)
	changes = append(changes, diffList("preset", before.Presets, after.Presets)...)
	changes = append(changes, diffPresetGrants(before.PresetGrants, after.PresetGrants)...)
	return changes
}

// diffPresetGrants reports what changes about the grants of each preset that
// exists before and after. Added and dropped presets are diffList rows.
func diffPresetGrants(before map[string]presetGrants, after map[string]presetGrants) []string {
	var changes []string
	for _, name := range filter.SortedKeys(after) {
		old, ok := before[name]
		if !ok {
			continue
		}
		prefix := "preset " + name + ": "
		for _, change := range diffOnePreset(old, after[name]) {
			changes = append(changes, prefix+change)
		}
	}
	return changes
}

func diffOnePreset(before presetGrants, after presetGrants) []string {
	var changes []string
	for _, capName := range filter.SortedKeys(after.Capabilities) {
		values := after.Capabilities[capName]
		oldValues, enabled := before.Capabilities[capName]
		if !enabled {
			changes = append(changes, "enables "+capName+describeConstraintValues(values))
			continue
		}
		for _, constraint := range filter.SortedKeys(values) {
			label := capName + " " + constraint
			oldValue, set := oldValues[constraint]
			switch {
			case !set:
				changes = append(changes, fmt.Sprintf("adds %s %s", label, values[constraint]))
			case oldValue != values[constraint]:
				changes = append(changes, fmt.Sprintf("changes %s from %s to %s", label, oldValue, values[constraint]))
			}
		}
		for _, constraint := range filter.SortedKeys(oldValues) {
			if _, set := values[constraint]; !set {
				changes = append(changes, fmt.Sprintf("drops %s %s %s", capName, constraint, oldValues[constraint]))
			}
		}
	}
	for _, capName := range filter.SortedKeys(before.Capabilities) {
		if _, enabled := after.Capabilities[capName]; !enabled {
			changes = append(changes, "disables "+capName)
		}
	}
	for _, counter := range filter.SortedKeys(after.Quotas) {
		oldQuota, set := before.Quotas[counter]
		switch {
		case !set:
			changes = append(changes, fmt.Sprintf("adds quota %s %d", counter, after.Quotas[counter]))
		case oldQuota != after.Quotas[counter]:
			changes = append(changes, fmt.Sprintf("changes quota %s from %d to %d", counter, oldQuota, after.Quotas[counter]))
		}
	}
	for _, counter := range filter.SortedKeys(before.Quotas) {
		if _, set := after.Quotas[counter]; !set {
			changes = append(changes, fmt.Sprintf("drops quota %s %d", counter, before.Quotas[counter]))
		}
	}
	return changes
}

// describeConstraintValues is " with c1 v1, c2 v2", or "" for none.
func describeConstraintValues(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, constraint := range filter.SortedKeys(values) {
		parts = append(parts, constraint+" "+values[constraint])
	}
	return " with " + strings.Join(parts, ", ")
}

// diffEndpoint reports a changed OAuth endpoint. A new host is spelled out
// with what it receives. A change within the same host shows the URLs.
func diffEndpoint(label string, before string, after string, receives string) []string {
	if before == after {
		return nil
	}
	beforeHost, afterHost := endpointHost(before), endpointHost(after)
	switch {
	case afterHost == "":
		return []string{fmt.Sprintf("drops %s %s", label, beforeHost)}
	case beforeHost == "":
		return []string{fmt.Sprintf("adds %s %s (%s)", label, afterHost, receives)}
	case beforeHost != afterHost:
		return []string{fmt.Sprintf("moves %s from %s to %s (%s)", label, beforeHost, afterHost, receives)}
	default:
		return diffScalar(label, before, after)
	}
}

// filterUpdateWarnings flags an update that sends credential material to a
// new host, reads the credential from a new host source, stops confining a
// domain, or changes what an existing preset grants. They are listed as warnings, ahead of the diff and
// in the JSON result, so they cannot drown in a long list of route changes.
func filterUpdateWarnings(before *filterCapabilities, after *filterCapabilities) []string {
	if before == nil || after == nil {
		return nil
	}
	var warnings []string
	for _, endpoint := range credentialEndpoints {
		beforeHost, afterHost := endpointHost(endpoint.url(before)), endpointHost(endpoint.url(after))
		switch {
		case afterHost == "" || afterHost == beforeHost:
		case beforeHost == "":
			warnings = append(warnings, fmt.Sprintf("%s is now %s (%s)", endpoint.label, afterHost, endpoint.receives))
		default:
			warnings = append(warnings, fmt.Sprintf("%s moves from %s to %s (%s)", endpoint.label, beforeHost, afterHost, endpoint.receives))
		}
	}
	for _, name := range after.StaticEnv {
		if !slices.Contains(before.StaticEnv, name) {
			warnings = append(warnings, fmt.Sprintf("the credential is now read from host env var %s", name))
		}
	}
	if after.StaticFile != "" && after.StaticFile != before.StaticFile {
		warnings = append(warnings, fmt.Sprintf("the credential is now read from host file %s", after.StaticFile))
	}
	for _, host := range after.Hosts {
		if !slices.Contains(before.Hosts, host) {
			warnings = append(warnings, fmt.Sprintf("the credential is now sent to governed host %s", host))
		}
	}
	for _, domain := range before.Confine {
		if !slices.Contains(after.Confine, domain) {
			warnings = append(warnings, fmt.Sprintf("names below %s that no filter governs are no longer blocked", domain))
		}
	}
	// A widened preset cannot be told from a narrowed one reliably, so every
	// change is a warning.
	warnings = append(warnings, diffPresetGrants(before.PresetGrants, after.PresetGrants)...)
	return warnings
}
