// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// listLayer is one precedence layer of the list fold: a config file, a tool
// override within one, or the command line.
type listLayer struct {
	source model.OptionSource
	scope  model.ConfigScope
	tool   string
	path   string
}

func (l listLayer) origin() model.EntryOrigin {
	return model.EntryOrigin{Source: l.source, Scope: l.scope, Tool: l.tool, Path: l.path}
}

func isListApply(kind ApplyKind) bool {
	switch kind {
	case ApplyListExtras, ApplySliceMergeFeature, ApplySliceMergeHost:
		return true
	default:
		return false
	}
}

func listOptionDefs() []OptionDef {
	var defs []OptionDef
	for _, def := range OptionDefs() {
		if def.Kind == OptionKindStringSlice && isListApply(def.Apply) {
			defs = append(defs, def)
		}
	}
	return defs
}

// listEntry is one entry of a resolved list and the layer that set it.
type listEntry struct {
	value  string
	origin model.EntryOrigin
}

// applyListLayer merges each list a config layer sets onto the inherited one.
func applyListLayer(opts *model.Options, defaults Defaults, layer listLayer, sources *model.OptionSources) {
	layer.path = defaults.path
	for _, def := range listOptionDefs() {
		incoming := optSlice(defaults, def.DefaultsField)
		if incoming == nil {
			continue
		}
		mergeListInto(opts, def, incoming, layer)
		setOptionSource(sources, def.SourceField, layer.source)
	}
}

func setOptionSource(sources *model.OptionSources, field string, source model.OptionSource) {
	reflect.ValueOf(sources).Elem().FieldByName(field).Set(reflect.ValueOf(source))
}

// mergeListInto merges one layer's raw list for def into opts, keeping
// opts.Origins, opts.ListProblems, and opts.DroppedRemovals in step.
func mergeListInto(opts *model.Options, def OptionDef, incoming []string, layer listLayer) {
	field := reflect.ValueOf(opts).Elem().FieldByName(def.OptionField)
	var base []listEntry
	if !field.IsNil() {
		values := field.Interface().([]string)
		base = make([]listEntry, 0, len(values))
		for _, value := range values {
			origin, _ := opts.Origins.Origin(def.Name, value)
			base = append(base, listEntry{value: value, origin: origin})
		}
	}

	merged, problems := mergeListLayer(def, base, incoming, layer)
	opts.ListProblems = append(opts.ListProblems, problems...)
	if def.Apply == ApplySliceMergeFeature {
		opts.DroppedRemovals = trackDroppedRemovals(opts.DroppedRemovals, def.Name, base, incoming, merged, layer)
	}
	if merged == nil {
		field.Set(reflect.Zero(field.Type()))
		delete(opts.Origins, def.Name)
		return
	}
	values := make([]string, 0, len(merged))
	origins := make(map[string]model.EntryOrigin, len(merged))
	for _, entry := range merged {
		values = append(values, entry.value)
		origins[entry.value] = entry.origin
	}
	field.Set(reflect.ValueOf(values))
	if opts.Origins == nil {
		opts.Origins = model.ListOrigins{}
	}
	opts.Origins[def.Name] = origins
}

// trackDroppedRemovals updates the removals of option that merging consumed. A
// replacing list discards the inherited ones along with the inherited
// selection; an amending list adds each of its removals that left no entry in
// merged, such as one against an explicit selection.
func trackDroppedRemovals(dropped []model.DroppedRemoval, option string, base []listEntry, incoming []string, merged []listEntry, layer listLayer) []model.DroppedRemoval {
	if !IsDirectiveOnly(incoming) {
		kept := dropped[:0]
		for _, removal := range dropped {
			if removal.Option != option {
				kept = append(kept, removal)
			}
		}
		return kept
	}
	if base == nil {
		return dropped
	}
	remaining := make(map[string]bool, len(merged))
	for _, entry := range merged {
		remaining[strings.TrimSpace(entry.value)] = true
	}
	for _, raw := range incoming {
		value := strings.TrimSpace(raw)
		if !strings.HasPrefix(value, "-") {
			continue
		}
		name := strings.TrimSpace(value[1:])
		if name == "" || remaining["-"+name] {
			continue
		}
		dropped = append(dropped, model.DroppedRemoval{Option: option, Name: name, Origin: layer.origin()})
	}
	return dropped
}

// mergeListLayer merges one layer onto base. A nil result means the list is
// still unset; an empty one means it was cleared.
func mergeListLayer(def OptionDef, base []listEntry, incoming []string, layer listLayer) ([]listEntry, []model.ListProblem) {
	if incoming == nil {
		return base, nil
	}
	switch def.Apply {
	case ApplyListExtras:
		m := extrasMerge{option: def.Name, layer: layer}
		if layer.source == model.SourceCLI && cliAddsPlainEntries[def.Name] {
			return m.mergeAdding(base, incoming), m.problems
		}
		return m.mergeReplacing(base, incoming), m.problems
	case ApplySliceMergeFeature:
		return mergeSelection(base, incoming, layer, featureSelection), nil
	case ApplySliceMergeHost:
		return mergeSelection(base, incoming, layer, hostConfigSelection), nil
	default:
		return base, nil
	}
}

// mergeListValues combines the two files' tool-override lists for display,
// keeping +/- prefixes so the result reads like a single layer: a replacing
// list stays bare, and directives over directives stay directives.
func mergeListValues(def OptionDef, base []string, incoming []string) []string {
	if base == nil || incoming == nil {
		if incoming == nil {
			return copyStringSlice(base)
		}
		return copyStringSlice(incoming)
	}
	if def.Apply == ApplyListExtras {
		return combineExtrasValues(def.Name, base, incoming)
	}
	rules := featureSelection
	switch def.Apply {
	case ApplySliceMergeHost:
		rules = hostConfigSelection
	}
	entries := make([]listEntry, 0, len(base))
	for _, value := range base {
		entries = append(entries, listEntry{value: value})
	}
	merged := mergeSelection(entries, incoming, listLayer{source: model.SourceToolOverride}, rules)
	values := make([]string, 0, len(merged))
	for _, entry := range merged {
		values = append(values, entry.value)
	}
	return values
}

// combineExtrasValues combines two extras lists into one that the fold applies
// the same way as the two in sequence, none and port clashes included.
func combineExtrasValues(option string, base []string, incoming []string) []string {
	if extrasListReplaces(incoming) {
		return copyStringSlice(incoming)
	}
	if extrasListReplaces(base) {
		// Distinct layers, so a clash replaces the base entry as in the fold.
		lower := extrasMerge{option: option, layer: listLayer{scope: model.ScopeGlobal}}
		upper := extrasMerge{option: option, layer: listLayer{scope: model.ScopeProject}}
		merged := upper.mergeReplacing(lower.mergeReplacing(nil, base), incoming)
		values := make([]string, 0, len(merged))
		for _, entry := range merged {
			values = append(values, entry.value)
		}
		return values
	}
	// Both amend: a later directive overrides an earlier one for the same entry,
	// and a later addition drops an earlier one it clashes with.
	values := make([]string, 0, len(base)+len(incoming))
	for _, raw := range base {
		lower, _ := parseListDirective(raw)
		superseded := false
		for _, next := range incoming {
			upper, _ := parseListDirective(next)
			if listEntryKey(option, lower.value) == listEntryKey(option, upper.value) ||
				(lower.op == '+' && upper.op == '+' && listEntriesClash(option, lower.value, upper.value)) {
				superseded = true
				break
			}
		}
		if !superseded {
			values = append(values, raw)
		}
	}
	return append(values, incoming...)
}

// extrasListReplaces reports whether an extras list replaces the inherited
// one rather than amending it.
func extrasListReplaces(values []string) bool {
	if len(values) == 0 {
		return true
	}
	for _, raw := range values {
		if d, isNone := parseListDirective(raw); isNone || d.op == 0 {
			return true
		}
	}
	return false
}

// listNone is the keyword that clears an extras list.
const listNone = "none"

// listDirective is one parsed entry of an extras list: a plain value, or a
// value to add (+) or remove (-). Exactly one prefix is stripped, so "++x"
// adds "+x" and "./+x" is a plain relative path.
type listDirective struct {
	op    byte
	value string
	raw   string
}

func parseListDirective(raw string) (listDirective, bool) {
	value := strings.TrimSpace(raw)
	if strings.EqualFold(value, listNone) {
		return listDirective{raw: raw}, true
	}
	d := listDirective{value: value, raw: raw}
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		d.op = value[0]
		d.value = strings.TrimSpace(value[1:])
	}
	return d, false
}

type extrasMerge struct {
	option   string
	layer    listLayer
	problems []model.ListProblem
}

// cliAddsPlainEntries lists the options whose CLI flag adds a plain value
// instead of replacing, because --add-dir is almost always used to mount one
// more directory next to the configured ones.
var cliAddsPlainEntries = map[string]bool{"add_dirs": true, "add_readonly_dirs": true}

// mergeReplacing applies a layer's list: [] or none clears, a list of plain
// entries replaces the inherited one, and a list of +/- entries amends it.
// Mixing the two, or none with anything, has no meaning of its own and is
// reported as an error.
func (m *extrasMerge) mergeReplacing(base []listEntry, incoming []string) []listEntry {
	directives, sawNone := m.parse(incoming)
	plain, prefixed := 0, 0
	firstPrefixed := ""
	for _, d := range directives {
		if d.op == 0 {
			plain++
			continue
		}
		if prefixed == 0 {
			firstPrefixed = strings.TrimSpace(d.raw)
		}
		prefixed++
	}
	switch {
	case sawNone && len(directives) > 0:
		m.problem(fmt.Sprintf("%q cannot be combined with other entries; it clears the list", listNone))
		return copyEntries(base)
	case plain > 0 && prefixed > 0:
		m.problem(fmt.Sprintf("%q mixes a +/- entry into a plain list; a plain list replaces the inherited one, so drop the prefixes or prefix every entry", firstPrefixed))
		return copyEntries(base)
	case len(incoming) == 0 || sawNone:
		return []listEntry{}
	}
	result := copyEntries(base)
	if plain > 0 {
		result = []listEntry{}
	}
	for _, d := range directives {
		if d.op == '-' {
			result = m.remove(result, d)
			continue
		}
		result = m.add(result, d.value)
	}
	return result
}

// mergeAdding applies the values of an additive CLI flag: plain and + entries
// add to the configured list and - entries remove from it. none drops the
// configured list, so the other values replace it.
func (m *extrasMerge) mergeAdding(base []listEntry, incoming []string) []listEntry {
	directives, sawNone := m.parse(incoming)
	result := copyEntries(base)
	if sawNone {
		result = []listEntry{}
	}
	for _, d := range directives {
		if d.op == '-' {
			result = m.remove(result, d)
			continue
		}
		result = m.add(result, d.value)
	}
	return result
}

func (m *extrasMerge) parse(incoming []string) ([]listDirective, bool) {
	directives := make([]listDirective, 0, len(incoming))
	sawNone := false
	for _, raw := range incoming {
		op, value, err := ValidateListEntry(m.option, raw)
		if err != nil {
			entry := RedactListEntry(m.option, raw)
			m.problem(fmt.Sprintf("invalid entry %q: %v", entry, err))
			continue
		}
		if op == "" && value == listNone {
			sawNone = true
			continue
		}
		d := listDirective{value: value, raw: raw}
		if op != "" {
			d.op = op[0]
		}
		directives = append(directives, d)
	}
	return directives, sawNone
}

// add appends value unless an entry with the same key exists. An entry from a
// lower layer that clashes with it is replaced in place; a clash within the
// layer is a problem.
func (m *extrasMerge) add(entries []listEntry, value string) []listEntry {
	origin := m.layer.origin()
	key := listEntryKey(m.option, value)
	placed := false
	result := entries[:0:0]
	for _, entry := range entries {
		sameKey := listEntryKey(m.option, entry.value) == key
		if !sameKey && !listEntriesClash(m.option, entry.value, value) {
			result = append(result, entry)
			continue
		}
		if entry.origin == origin {
			if !sameKey {
				m.problem(fmt.Sprintf("%q and %q both publish host port %s", entry.value, value, portSpecHostPort(value)))
			}
			// The first entry of the layer stays.
			result = append(result, entry)
			placed = true
			continue
		}
		if !placed {
			result = append(result, listEntry{value: value, origin: origin})
			placed = true
		}
	}
	if !placed {
		result = append(result, listEntry{value: value, origin: origin})
	}
	return result
}

// remove drops every entry with the same key as d.
func (m *extrasMerge) remove(entries []listEntry, d listDirective) []listEntry {
	key := listEntryKey(m.option, d.value)
	result := entries[:0:0]
	for _, entry := range entries {
		if listEntryKey(m.option, entry.value) != key {
			result = append(result, entry)
		}
	}
	if len(result) == len(entries) {
		logx.Debugf("%s: %q removes nothing; no inherited entry matches", m.option, strings.TrimSpace(d.raw))
	}
	return result
}

func (m *extrasMerge) problem(message string) {
	m.problems = append(m.problems, model.ListProblem{
		Option:  m.option,
		Origin:  m.layer.origin(),
		Message: message,
	})
}

func copyEntries(entries []listEntry) []listEntry {
	if entries == nil {
		return nil
	}
	return append([]listEntry{}, entries...)
}

// listEntryKey returns what two entries of option share when they name the
// same thing, so a re-added entry is deduplicated and a removal matches it.
func listEntryKey(option string, value string) string {
	value = strings.TrimSpace(value)
	switch option {
	case "ports":
		if binding, ok := parsePortBinding(value); ok {
			return binding.ip + "|" + binding.hostPort + "|" + binding.containerPort
		}
	case "bridge_ports":
		if n, err := strconv.Atoi(value); err == nil {
			return strconv.Itoa(n)
		}
	case "allow_domains":
		return strings.ToLower(value)
	case "add_dirs", "add_readonly_dirs", "sensitive_dir_exceptions":
		return resolvedDirKey(value)
	case "host_config_paths":
		return hostConfigSelection.key(value)
	}
	return value
}

func resolvedDirKey(value string) string {
	if home, err := os.UserHomeDir(); err == nil {
		value = util.ExpandTilde(value, home)
	}
	if abs, err := filepath.Abs(value); err == nil {
		return abs
	}
	return filepath.Clean(value)
}

type portBinding struct {
	ip, hostPort, containerPort string
}

// parsePortBinding parses a publish spec with Docker's defaults applied: an
// omitted host IP binds 127.0.0.1.
func parsePortBinding(value string) (portBinding, bool) {
	ip, hostPort, containerPort, ok := util.ParsePortSpec(value)
	if !ok {
		return portBinding{}, false
	}
	ip = strings.Trim(ip, "[]")
	if ip == "" {
		ip = "127.0.0.1"
	}
	host, _ := strconv.Atoi(hostPort)
	container, _ := strconv.Atoi(containerPort)
	return portBinding{ip: ip, hostPort: strconv.Itoa(host), containerPort: strconv.Itoa(container)}, true
}

func portSpecHostPort(value string) string {
	binding, _ := parsePortBinding(value)
	return binding.hostPort
}

// listEntriesClash reports whether two entries with different keys cannot
// both apply: two publish specs that bind the same host port on overlapping
// addresses. Host port 0 asks for a free port and never clashes.
func listEntriesClash(option string, a string, b string) bool {
	if option != "ports" {
		return false
	}
	left, okLeft := parsePortBinding(a)
	right, okRight := parsePortBinding(b)
	if !okLeft || !okRight || left.hostPort == "0" || left.hostPort != right.hostPort {
		return false
	}
	return left.ip == right.ip || isWildcardHostIP(left.ip) || isWildcardHostIP(right.ip)
}

func isWildcardHostIP(ip string) bool {
	return ip == "0.0.0.0" || ip == "::"
}

// selectionRules describes a selection list: its keywords, which stand for a
// set resolved later, and the key two entries for the same item share.
type selectionRules struct {
	keywords []string
	key      func(name string) string
}

var featureSelection = selectionRules{
	keywords: []string{model.SelectionDefault, model.FeatureSelectionAll},
	key:      func(name string) string { return name },
}

var hostConfigSelection = selectionRules{
	keywords: []string{model.SelectionDefault},
	key: func(name string) string {
		if normalized := normalizeHostConfigPath(name); normalized != "" {
			name = normalized
		}
		return strings.TrimSuffix(name, "/")
	},
}

func (r selectionRules) isKeyword(name string) bool {
	for _, keyword := range r.keywords {
		if strings.EqualFold(name, keyword) {
			return true
		}
	}
	return false
}

// mergeSelection merges a selection list layer. A list with a bare entry (a
// name or keyword), and an explicit empty list, replaces the inherited one. A
// list of only +/- directives amends it, and the directive for an item
// replaces whatever the inherited list said about that item, so the higher
// layer wins. Directives with nothing inherited stay as they are and are
// resolved later against the option's implicit base.
func mergeSelection(base []listEntry, incoming []string, layer listLayer, rules selectionRules) []listEntry {
	origin := layer.origin()
	if !IsDirectiveOnly(incoming) || base == nil {
		result := make([]listEntry, 0, len(incoming))
		for _, raw := range incoming {
			result = append(result, listEntry{value: raw, origin: origin})
		}
		return result
	}

	baseValues := make([]string, 0, len(base))
	for _, entry := range base {
		baseValues = append(baseValues, entry.value)
	}
	explicit := !IsDirectiveOnly(baseValues)
	hasKeyword := false
	for _, value := range baseValues {
		if rules.isKeyword(strings.TrimPrefix(strings.TrimSpace(value), "+")) {
			hasKeyword = true
		}
	}

	merged := copyEntries(base)
	for _, raw := range incoming {
		value := strings.TrimSpace(raw)
		if len(value) < 2 {
			continue
		}
		name := strings.TrimSpace(value[1:])
		if name == "" {
			continue
		}
		merged = dropSelectionEntries(merged, name, rules)
		switch {
		case value[0] == '+' && explicit:
			merged = append(merged, listEntry{value: name, origin: origin})
		case value[0] == '+':
			merged = append(merged, listEntry{value: "+" + name, origin: origin})
		case !explicit || hasKeyword:
			// A removal only matters while something can still expand to the item.
			merged = append(merged, listEntry{value: "-" + name, origin: origin})
		}
	}
	return merged
}

// IsDirectiveOnly reports whether values holds entries and all of them are
// +/- directives. An explicit empty list counts as a bare selection of nothing.
func IsDirectiveOnly(values []string) bool {
	sawDirective := false
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if !strings.HasPrefix(value, "+") && !strings.HasPrefix(value, "-") {
			return false
		}
		sawDirective = true
	}
	return sawDirective
}

func dropSelectionEntries(entries []listEntry, name string, rules selectionRules) []listEntry {
	key := rules.key(name)
	kept := entries[:0]
	for _, entry := range entries {
		value := strings.TrimSpace(entry.value)
		bare := value
		if strings.HasPrefix(bare, "+") || strings.HasPrefix(bare, "-") {
			bare = strings.TrimSpace(bare[1:])
		}
		if value != "" && rules.key(bare) == key {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}
