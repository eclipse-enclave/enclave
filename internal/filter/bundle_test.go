// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import "testing"

func TestBundleFileNames(t *testing.T) {
	if PolicyFileName("gws") != "gws.policy.json" || CredentialFileName("gws") != "gws.credential.json" {
		t.Fatal("unexpected bundle file names")
	}
	for file, want := range map[string]struct {
		name       string
		credential bool
		ok         bool
	}{
		"gws.policy.json":         {name: "gws", ok: true},
		"gws.credential.json":     {name: "gws", credential: true, ok: true},
		"my-filter.policy.json":   {name: "my-filter", ok: true},
		".policy.json":            {},
		"GWS.policy.json":         {},
		"../gws.policy.json":      {},
		"gws.policy.json.swp":     {},
		"gws.json":                {},
		"gws.credential.json.tmp": {},
	} {
		name, credential, ok := ParseBundleFileName(file)
		if ok != want.ok || (ok && (name != want.name || credential != want.credential)) {
			t.Errorf("ParseBundleFileName(%q) = %q, %v, %v, want %+v", file, name, credential, ok, want)
		}
	}
}
