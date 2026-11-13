// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/filter"
)

// MaxPresetChain is the longest extends chain, counting the selected preset.
const MaxPresetChain = 8

// Preset is a resolved preset: the extends chain merged, defaults applied,
// and every enabled capability's constraints set.
type Preset struct {
	Name        string
	Description string
	// Path is the file of the selected preset. Shipped tells whether it comes
	// from the filter rather than the user's preset directory.
	Path    string
	Shipped bool
	// Chain lists the presets of the extends chain, selected preset first.
	Chain []PresetLink
	// Capabilities maps each enabled capability to its constraint values.
	Capabilities map[string]map[string]filter.ConstraintValue
	// Quotas maps counters to per-session limits.
	Quotas map[string]int64
}

// PresetLink is one preset of an extends chain.
type PresetLink struct {
	Name    string
	Path    string
	Shipped bool
}

// PresetInfo describes an available preset.
type PresetInfo struct {
	Name        string
	Description string
	Path        string
	Shipped     bool
	// Shadows is set for a user preset that hides a shipped one.
	Shadows bool
}

type presetRef struct {
	name    string
	shipped bool
}

func (r presetRef) String() string {
	if r.shipped {
		return r.name + " (shipped)"
	}
	return r.name + " (user)"
}

// ShippedPresetsDir is the filter's own presets directory.
func (f *Filter) ShippedPresetsDir() string {
	if f.Dir == "" {
		return ""
	}
	return filepath.Join(f.Dir, PresetsDir)
}

// Presets lists the user and shipped presets, sorted by name. userDir may be
// empty. Files that are not <valid name>.yaml are skipped.
func (f *Filter) Presets(userDir string) ([]PresetInfo, error) {
	byName := map[string]PresetInfo{}
	shipped, err := presetNames(f.ShippedPresetsDir())
	if err != nil {
		return nil, err
	}
	for _, name := range shipped {
		path := filepath.Join(f.ShippedPresetsDir(), name+PresetExt)
		doc, err := readPreset(path)
		if err != nil {
			return nil, err
		}
		byName[name] = PresetInfo{Name: name, Description: doc.Description, Path: path, Shipped: true}
	}
	user, err := presetNames(userDir)
	if err != nil {
		return nil, err
	}
	for _, name := range user {
		path := filepath.Join(userDir, name+PresetExt)
		doc, err := readPreset(path)
		if err != nil {
			return nil, err
		}
		_, shadows := byName[name]
		byName[name] = PresetInfo{Name: name, Description: doc.Description, Path: path, Shadows: shadows}
	}
	var out []PresetInfo
	for _, name := range filter.SortedKeys(byName) {
		out = append(out, byName[name])
	}
	return out, nil
}

func presetNames(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), PresetExt)
		if !ok || entry.IsDir() || filter.ValidateName(name) != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func readPreset(path string) (presetDocument, error) {
	data, err := readRegularFile(path)
	if err != nil {
		return presetDocument{}, err
	}
	var doc presetDocument
	if err := decodeYAML(data, &doc); err != nil {
		return presetDocument{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc, nil
}

// ResolvePreset loads the named preset, user presets in userDir shadowing the
// shipped ones, follows extends, merges, and applies constraint defaults.
// userDir may be empty to resolve shipped presets only.
func (f *Filter) ResolvePreset(name string, userDir string) (*Preset, error) {
	if err := filter.ValidateName(name); err != nil {
		return nil, fmt.Errorf("filter %s: preset: %w", f.Name, err)
	}
	ref, path, err := f.lookupPreset(name, userDir, false)
	if err != nil {
		return nil, err
	}
	type level struct {
		name    string
		path    string
		shipped bool
		doc     presetDocument
	}
	var chain []level
	visited := map[presetRef]bool{}
	for {
		if visited[ref] {
			return nil, fmt.Errorf("filter %s: preset %s: extends cycle through %s", f.Name, name, ref)
		}
		visited[ref] = true
		if len(chain) == MaxPresetChain {
			return nil, fmt.Errorf("filter %s: preset %s: extends chain is longer than %d presets", f.Name, name, MaxPresetChain)
		}
		doc, err := readPreset(path)
		if err != nil {
			return nil, err
		}
		if err := f.validatePresetDoc(doc); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		chain = append(chain, level{name: ref.name, path: path, shipped: ref.shipped, doc: doc})
		if doc.Extends == "" {
			break
		}
		// A user preset extending its own name extends the shipped one,
		// never itself through the shadowing.
		skipUser := doc.Extends == ref.name && !ref.shipped
		if doc.Extends == ref.name && ref.shipped {
			return nil, fmt.Errorf("%s: extends: a preset cannot extend itself", path)
		}
		ref, path, err = f.lookupPreset(doc.Extends, userDir, skipUser)
		if err != nil {
			return nil, fmt.Errorf("%s: extends: %w", chain[len(chain)-1].path, err)
		}
	}

	p := &Preset{
		Name:         name,
		Description:  chain[0].doc.Description,
		Path:         chain[0].path,
		Shipped:      chain[0].shipped,
		Capabilities: map[string]map[string]filter.ConstraintValue{},
		Quotas:       map[string]int64{},
	}
	for _, l := range chain {
		p.Chain = append(p.Chain, PresetLink{Name: l.name, Path: l.path, Shipped: l.shipped})
	}
	for i := len(chain) - 1; i >= 0; i-- {
		doc := chain[i].doc
		for _, capName := range doc.Deny {
			delete(p.Capabilities, capName)
		}
		for _, capName := range filter.SortedKeys(doc.Allow) {
			values, _ := f.parseAllow(capName, doc.Allow[capName])
			p.Capabilities[capName] = values
		}
		for counter, quota := range doc.Quotas {
			p.Quotas[counter] = quota
		}
	}
	if err := f.applyDefaults(p); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Path, err)
	}
	return p, nil
}

func (f *Filter) lookupPreset(name string, userDir string, skipUser bool) (presetRef, string, error) {
	if err := filter.ValidateName(name); err != nil {
		return presetRef{}, "", err
	}
	if userDir != "" && !skipUser {
		path := filepath.Join(userDir, name+PresetExt)
		if _, err := os.Stat(path); err == nil {
			return presetRef{name: name}, path, nil
		}
	}
	if dir := f.ShippedPresetsDir(); dir != "" {
		path := filepath.Join(dir, name+PresetExt)
		if _, err := os.Stat(path); err == nil {
			return presetRef{name: name, shipped: true}, path, nil
		}
	}
	if skipUser {
		return presetRef{}, "", fmt.Errorf("filter %s ships no preset %q%s", f.Name, name, f.availablePresets(""))
	}
	return presetRef{}, "", fmt.Errorf("filter %s has no preset %q%s", f.Name, name, f.availablePresets(userDir))
}

// availablePresets lists the preset names for an error message, the user
// presets in userDir included when it is set.
func (f *Filter) availablePresets(userDir string) string {
	presets, err := f.Presets(userDir)
	if err != nil {
		return ""
	}
	if len(presets) == 0 {
		return " (it has none)"
	}
	names := make([]string, 0, len(presets))
	for _, p := range presets {
		names = append(names, p.Name)
	}
	return " (available: " + strings.Join(names, ", ") + ")"
}

// validatePresetDoc checks one preset file on its own.
func (f *Filter) validatePresetDoc(doc presetDocument) error {
	if doc.Extends != "" {
		if err := filter.ValidateName(doc.Extends); err != nil {
			return fmt.Errorf("extends: %w", err)
		}
	}
	for _, capName := range filter.SortedKeys(doc.Allow) {
		if _, err := f.parseAllow(capName, doc.Allow[capName]); err != nil {
			return fmt.Errorf("allow.%s: %w", capName, err)
		}
	}
	denied := map[string]bool{}
	for _, capName := range doc.Deny {
		c, ok := f.Capabilities[capName]
		if !ok {
			return fmt.Errorf("deny: unknown capability %q", capName)
		}
		if c.Public {
			return fmt.Errorf("deny: %s is public and always enabled", capName)
		}
		if _, alsoAllowed := doc.Allow[capName]; alsoAllowed {
			return fmt.Errorf("deny: %s is also in allow", capName)
		}
		if denied[capName] {
			return fmt.Errorf("deny: duplicate capability %q", capName)
		}
		denied[capName] = true
	}
	for _, counter := range filter.SortedKeys(doc.Quotas) {
		if _, ok := f.Counters[counter]; !ok {
			return fmt.Errorf("quotas: unknown counter %q", counter)
		}
		if doc.Quotas[counter] < 0 {
			return fmt.Errorf("quotas.%s: must not be negative", counter)
		}
	}
	return nil
}

// parseAllow parses an allow entry: true, {}, or a map of constraint values.
func (f *Filter) parseAllow(capName string, raw json.RawMessage) (map[string]filter.ConstraintValue, error) {
	c, ok := f.Capabilities[capName]
	if !ok {
		return nil, fmt.Errorf("unknown capability")
	}
	if c.Public {
		return nil, fmt.Errorf("%s is public and always enabled", capName)
	}
	decoded, err := filter.DecodeJSON(raw)
	if err != nil {
		return nil, err
	}
	values := map[string]filter.ConstraintValue{}
	switch x := decoded.(type) {
	case bool:
		if !x {
			return nil, fmt.Errorf("false is not allowed, remove the entry or use deny")
		}
		return values, nil
	case map[string]any:
	default:
		return nil, fmt.Errorf("use {}, true, or a map of constraint values")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	for _, cname := range filter.SortedKeys(obj) {
		con, ok := c.Constraints[cname]
		if !ok {
			return nil, fmt.Errorf("unknown constraint %q%s", cname, didYouMean(cname, filter.SortedKeys(c.Constraints)))
		}
		value, err := parseConstraintValue(obj[cname], con.Kind, con.Resolver != "")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cname, err)
		}
		values[cname] = value
	}
	return values, nil
}

func (f *Filter) applyDefaults(p *Preset) error {
	for _, capName := range filter.SortedKeys(p.Capabilities) {
		values := p.Capabilities[capName]
		for _, cname := range filter.SortedKeys(f.Capabilities[capName].Constraints) {
			if _, set := values[cname]; set {
				continue
			}
			con := f.Capabilities[capName].Constraints[cname]
			if con.Default == nil {
				return fmt.Errorf("preset %s enables %s without a value for constraint %s, and the filter declares no default", p.Name, capName, cname)
			}
			values[cname] = *con.Default
		}
	}
	return nil
}

// validateShippedPresets resolves every shipped preset on its own and checks
// defaultPreset.
func (f *Filter) validateShippedPresets() error {
	names, err := presetNames(f.ShippedPresetsDir())
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err := f.ResolvePreset(name, ""); err != nil {
			return err
		}
	}
	if dir := f.ShippedPresetsDir(); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				name, ok := strings.CutSuffix(entry.Name(), PresetExt)
				if !ok || entry.IsDir() || filter.ValidateName(name) != nil {
					f.Warnings = append(f.Warnings, fmt.Sprintf("%s is ignored: presets are <name>%s files", filepath.Join(dir, entry.Name()), PresetExt))
				}
			}
		}
	}
	if f.DefaultPreset == "" {
		if len(names) > 0 {
			return fmt.Errorf("%s: defaultPreset: is required when the filter ships presets", f.SpecPath)
		}
		return nil
	}
	for _, name := range names {
		if name == f.DefaultPreset {
			return nil
		}
	}
	return fmt.Errorf("%s: defaultPreset: %q is not a shipped preset", f.SpecPath, f.DefaultPreset)
}
