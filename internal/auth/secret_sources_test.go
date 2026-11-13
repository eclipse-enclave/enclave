// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestSecretSourcesResolve(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "x", "token"), []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	sources := SecretSources{
		Home:      home,
		Layers:    []SecretsLayer{{Path: "global.env", Values: map[string]string{"B_TOKEN": "from-secrets"}}},
		Persisted: map[string]string{"C_TOKEN": "from-store"},
		Getenv:    func(name string) string { return env[name] },
	}
	ref := SecretRef{ID: "x", EnvVars: []string{"A_TOKEN", "C_TOKEN"}, File: &model.SecretFileSource{Path: "~/.config/x/token"}}

	for _, tc := range []struct {
		name       string
		priority   string
		env        map[string]string
		vars       []string
		wantValue  string
		wantSource string
	}{
		{"env store", "", nil, []string{"C_TOKEN"}, "from-store", "persisted"},
		{"secrets file", "", nil, []string{"B_TOKEN", "C_TOKEN"}, "from-secrets", "secrets"},
		{"host env", "", map[string]string{"C_TOKEN": "from-env"}, []string{"B_TOKEN", "C_TOKEN"}, "from-env", "env"},
		{"file without aliases", "", nil, []string{"A_TOKEN"}, "from-file", "file"},
		{"file first", model.SecretPriorityFileFirst, map[string]string{"A_TOKEN": "from-env"}, []string{"A_TOKEN"}, "from-file", "file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env = tc.env
			r := ref
			r.Priority, r.EnvVars = tc.priority, tc.vars
			value, source, found, err := sources.Resolve(r)
			if err != nil || !found || strings.TrimSpace(value) != tc.wantValue || source != tc.wantSource {
				t.Fatalf("Resolve = %q, %q, %v, %v, want %q from %s", value, source, found, err, tc.wantValue, tc.wantSource)
			}
		})
	}

	env = map[string]string{"A_TOKEN": "one", "C_TOKEN": "two"}
	if _, _, _, err := sources.Resolve(ref); err == nil || !strings.Contains(err.Error(), `secret "x" has conflicting values`) {
		t.Fatalf("conflicting aliases in one layer = %v", err)
	}
	env = nil
	if _, _, found, err := sources.Resolve(SecretRef{ID: "x", EnvVars: []string{"A_TOKEN"}}); found || err != nil {
		t.Fatalf("missing credential = %v, %v", found, err)
	}
}
