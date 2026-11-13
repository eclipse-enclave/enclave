// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"fmt"
	"strings"

	"enclave/internal/config"
	"enclave/internal/filter/spec"
)

// UserPresetsDir is where the user's presets of a filter live, or "" without
// a host home.
func UserPresetsDir(home string, filterName string) string {
	if strings.TrimSpace(home) == "" {
		return ""
	}
	return config.HostFilterPresetsDir(home, filterName)
}

// ResolvePreset resolves preset of f with the user's presets shadowing the
// shipped ones. An empty preset selects the filter's defaultPreset.
func ResolvePreset(f *spec.Filter, home string, preset string) (*spec.Preset, error) {
	if preset == "" {
		preset = f.DefaultPreset
	}
	if preset == "" {
		return nil, fmt.Errorf("filter %s has no default preset: select %s:<preset>", f.Name, f.Name)
	}
	return f.ResolvePreset(preset, UserPresetsDir(home, f.Name))
}

// LoginScopes returns the preset name a login is for and the scopes it
// requests: the given scopes, or the required scopes of the preset (the
// default one when neither is given), minimized through scopeImplies.
func LoginScopes(f *spec.Filter, home string, preset string, scopes []string) (string, []string, error) {
	if f.Credential.OAuth2 == nil {
		return "", nil, fmt.Errorf("filter %s uses a static credential: login only applies to oauth2 filters", f.Name)
	}
	if len(scopes) > 0 {
		if preset != "" {
			return "", nil, fmt.Errorf("pass --preset or --scopes, not both")
		}
		return "", f.MinimizeScopes(scopes), nil
	}
	p, err := ResolvePreset(f, home, preset)
	if err != nil {
		return "", nil, err
	}
	required := f.RequiredScopes(p)
	if len(required) == 0 {
		return "", nil, fmt.Errorf("preset %s of filter %s needs no scopes: pass --scopes to request some", p.Name, f.Name)
	}
	return p.Name, f.MinimizeScopes(required), nil
}

// SelectGrant picks the stored grant that covers required, see
// spec.Filter.SelectGrant. It also returns the grant files that do not load,
// for the caller to report, since one of them may be the grant the user
// expects.
func SelectGrant(f *spec.Filter, store Store, required []string) (Grant, bool, []string, error) {
	grants, problems, err := store.Grants()
	if err != nil {
		return Grant{}, false, nil, err
	}
	candidate, ok := f.SelectGrant(GrantCandidates(grants), required)
	if !ok {
		return Grant{}, false, problems, nil
	}
	for _, g := range grants {
		if g.ID == candidate.ID {
			return g, true, problems, nil
		}
	}
	return Grant{}, false, problems, nil
}

// reportGrantProblems hands each problem of SelectGrant to report, if set.
func reportGrantProblems(problems []string, report func(string)) {
	if report == nil {
		return
	}
	for _, problem := range problems {
		report(problem)
	}
}

// LoginCommand is the command that creates a grant for preset of filter.
func LoginCommand(filterName string, preset string) string {
	if preset == "" {
		return "enclave filters login " + filterName
	}
	return fmt.Sprintf("enclave filters login %s --preset %s", filterName, preset)
}
