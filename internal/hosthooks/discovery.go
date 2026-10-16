// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hosthooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Provider struct {
	ID      string
	Dir     string
	Phases  map[string]string
	Config  Config
	Builtin func(context.Context, Request) (Response, error)
}
type Manifest struct {
	SchemaVersion string            `json:"schemaVersion"`
	Phases        map[string]string `json:"phases"`
}

// Load validates without executing. The provider directory anchors every handler.
func Load(id, dir string) (Provider, error) {
	p := Provider{ID: id, Dir: dir}
	info, err := os.Stat(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return p, err
	}
	if !info.Mode().IsRegular() {
		return p, fmt.Errorf("manifest must be a regular file")
	}
	// #nosec G304 -- dir is a caller-supplied installed/user provider root; the filename is fixed.
	f, err := os.Open(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return p, err
	}
	defer func() { _ = f.Close() }()
	data, err := readBounded(f, MaxMessage)
	if err != nil {
		return p, err
	}
	var m Manifest
	if err := decodeObject(data, &m); err != nil {
		return p, err
	}
	if m.SchemaVersion != Version || len(m.Phases) == 0 {
		return p, fmt.Errorf("unsupported or empty host hook manifest")
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return p, err
	}
	for phase, path := range m.Phases {
		if phase != Prepare && phase != Enter && phase != Leave {
			return p, fmt.Errorf("unknown host phase %q", phase)
		}
		if path == "" || filepath.IsAbs(path) {
			return p, fmt.Errorf("handler path must be relative")
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(dir, path))
		if err != nil {
			return p, err
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p, fmt.Errorf("handler escapes provider directory")
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return p, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return p, fmt.Errorf("handler %q is not executable regular file", path)
		}
	}
	p.Phases = m.Phases
	return p, nil
}

func (p Provider) Capability() Capability {
	c := Capability{ID: p.ID}
	for phase := range p.Phases {
		c.Phases = append(c.Phases, phase)
	}
	sort.Strings(c.Phases)
	return c
}

// Extension reads only installed user roots supplied by the caller.
func Extension(id, extensionDir string) ([]Capability, []string) {
	hooks := filepath.Join(extensionDir, "hooks")
	entries, err := os.ReadDir(hooks)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{err.Error()}
	}
	var warnings []string
	var caps []Capability
	for _, e := range entries {
		if e.Name() != "host" {
			warnings = append(warnings, fmt.Sprintf("%s: ignoring hooks/%s (only hooks/host is supported)", id, e.Name()))
			continue
		}
		p, err := Load(id, filepath.Join(hooks, "host"))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: invalid host hooks: %v", id, err))
			continue
		}
		caps = append(caps, p.Capability())
	}
	return caps, warnings
}

type Roots struct {
	User     string
	Features string
	Tools    string
}

func Discover(roots Roots, tool string) ([]Provider, []string) {
	var providers []Provider
	var warnings []string
	scan := func(root, namespace string, extension bool) {
		if root == "" {
			return
		}
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			warnings = append(warnings, err.Error())
			return
		}
		for _, entry := range entries {
			if namespace == "tool" && entry.Name() != tool {
				continue
			}
			id := namespace + ":" + entry.Name()
			if !ValidID(id) {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			if extension {
				_, ws := Extension(id, dir)
				warnings = append(warnings, ws...)
				dir = filepath.Join(dir, "hooks", "host")
			}
			if _, err := os.Lstat(dir); os.IsNotExist(err) {
				continue
			}
			p, err := Load(id, dir)
			if err != nil {
				if !extension {
					warnings = append(warnings, fmt.Sprintf("%s: %v", id, err))
				}
				continue
			}
			providers = append(providers, p)
		}
	}
	scan(roots.User, "user", false)
	scan(roots.Features, "feature", true)
	scan(roots.Tools, "tool", true)
	return providers, warnings
}

func Activate(providers []Provider, configs ConfigMap, warn func(string)) []Provider {
	known := map[string]bool{}
	var enabled []Provider
	for _, p := range providers {
		known[p.ID] = true
		c := configs[p.ID]
		if c.Enabled == nil || !*c.Enabled {
			continue
		}
		p.Config = c
		enabled = append(enabled, p)
	}
	var ids []string
	for id, c := range configs {
		if c.Enabled != nil && *c.Enabled && !known[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		warn(id + ": enabled host hook provider is unavailable; skipping")
	}
	sort.Slice(enabled, func(i, j int) bool {
		order := func(p Provider) int {
			if p.Config.Order != nil {
				return *p.Config.Order
			}
			return 100
		}
		a, b := order(enabled[i]), order(enabled[j])
		if a == b {
			return enabled[i].ID < enabled[j].ID
		}
		return a < b
	})
	return enabled
}

func configRequest(p Provider, req Request, phase string) Request {
	req.SchemaVersion = Version
	req.Phase = phase
	req.ProviderID = p.ID
	req.Settings = p.Config.Settings
	if req.Settings == nil {
		req.Settings = map[string]any{}
	}
	return req
}
