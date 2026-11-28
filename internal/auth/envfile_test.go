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
	"slices"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestEditEnvContent(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		remove      []string
		set         []string
		value       string
		want        string
		wantRemoved []string
	}{
		{
			name:  "append to empty",
			set:   []string{"A"},
			value: "1",
			want:  "A=1\n",
		},
		{
			name:        "replace aliases and keep the rest",
			content:     "# keys\nexport CODEX_API_KEY='old'\nOTHER=x\n OPENAI_API_KEY = older\n",
			remove:      []string{"CODEX_API_KEY", "OPENAI_API_KEY"},
			set:         []string{"CODEX_API_KEY"},
			value:       "new",
			want:        "# keys\nOTHER=x\nCODEX_API_KEY=new\n",
			wantRemoved: []string{"CODEX_API_KEY", "OPENAI_API_KEY"},
		},
		{
			name:        "remove duplicates",
			content:     "A=1\nA=2\nB=3",
			remove:      []string{"A"},
			want:        "B=3\n",
			wantRemoved: []string{"A"},
		},
		{
			name:        "nothing left",
			content:     "A=1\n\n",
			remove:      []string{"A"},
			want:        "",
			wantRemoved: []string{"A"},
		},
		{
			name:    "comment mentioning the key stays",
			content: "# A=old\nB=1\n",
			remove:  []string{"A"},
			want:    "# A=old\nB=1\n",
		},
		{
			name:    "nothing to remove leaves content as is",
			content: "B=3",
			remove:  []string{"A"},
			want:    "B=3",
		},
		{
			name:    "several variables get one value",
			content: "B=3\n",
			set:     []string{"OPENAI_API_KEY", "CODEX_API_KEY"},
			value:   "sk",
			want:    "B=3\nOPENAI_API_KEY=sk\nCODEX_API_KEY=sk\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, removed := EditEnvContent(tt.content, tt.remove, tt.set, tt.value)
			if got != tt.want {
				t.Fatalf("content = %q, want %q", got, tt.want)
			}
			if !slices.Equal(removed, tt.wantRemoved) {
				t.Fatalf("removed = %v, want %v", removed, tt.wantRemoved)
			}
		})
	}
}

func TestEditEnvFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "secrets", "global", "claude.env")

	if _, err := EditEnvFile(root, path, []string{"KEY"}, []string{"KEY"}, "v1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", dir.Mode().Perm())
	}
	values, err := ReadAllEnvFromFile(path)
	if err != nil || values["KEY"] != "v1" {
		t.Fatalf("values = %v, err = %v", values, err)
	}

	removed, err := EditEnvFile(root, path, []string{"KEY"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Key != "KEY" || removed[0].Value != "v1" {
		t.Fatalf("removed = %v", removed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty secrets file should be deleted, stat err = %v", err)
	}

	removed, err = EditEnvFile(root, path, []string{"KEY"}, nil, "")
	if err != nil || len(removed) != 0 {
		t.Fatalf("unset of missing file: removed = %v, err = %v", removed, err)
	}
}

func TestEditEnvFileKeepsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "dotfiles", "claude.env")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("OTHER=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "secrets", "global", "claude.env")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := EditEnvFile(root, link, []string{"KEY"}, []string{"KEY"}, "v"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced: info = %v, err = %v", info, err)
	}
	values, err := ReadAllEnvFromFile(target)
	if err != nil || values["KEY"] != "v" || values["OTHER"] != "1" {
		t.Fatalf("target values = %v, err = %v", values, err)
	}
}

func TestEditEnvFileNoopLeavesFileAlone(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "claude.env")
	if err := os.WriteFile(path, []byte("B=3"), 0o640); err != nil {
		t.Fatal(err)
	}
	removed, err := EditEnvFile(root, path, []string{"A"}, nil, "")
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != "B=3" || info.Mode().Perm() != 0o640 {
		t.Fatalf("file changed: %q, mode %v", data, info.Mode().Perm())
	}

	blank := filepath.Join(root, "blank.env")
	if err := os.WriteFile(blank, []byte("\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EditEnvFile(root, blank, []string{"A"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(blank); err != nil {
		t.Fatalf("blank file removed by a no-op unset: %v", err)
	}
}

func TestEditEnvFileSymlinkSurvivesEmptying(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "dotfiles", "ai.env")
	link := filepath.Join(root, "secrets", "global.env")
	for _, dir := range []string{filepath.Dir(target), filepath.Dir(link)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(target, []byte("KEY=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../dotfiles/ai.env", link); err != nil {
		t.Fatal(err)
	}

	if _, err := EditEnvFile(root, link, []string{"KEY"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || len(data) != 0 {
		t.Fatalf("target should be emptied, not deleted: %q, %v", data, err)
	}
	if _, err := EditEnvFile(root, link, nil, []string{"KEY"}, "new"); err != nil {
		t.Fatalf("set after unset: %v", err)
	}

	// A dangling link is followed to the file it names.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, err := EditEnvFile(root, link, nil, []string{"KEY"}, "again"); err != nil {
		t.Fatalf("set through dangling link: %v", err)
	}
	values, err := ReadAllEnvFromFile(target)
	if err != nil || values["KEY"] != "again" {
		t.Fatalf("target values = %v, err = %v", values, err)
	}

	resolved, linked, err := resolveEnvFileTarget(link)
	if err != nil || !linked || filepath.Clean(resolved) != filepath.Clean(target) {
		t.Fatalf("resolve = %q, %v, %v", resolved, linked, err)
	}
}

func TestResolveSecretsLayersProjectShared(t *testing.T) {
	home := t.TempDir()
	write := func(path string, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(config.HostSecretsGlobalSharedFile(home), "KEY=global\n")
	write(config.HostSecretsGlobalFile(home, "claude"), "KEY=global-tool\n")
	write(config.HostSecretsProjectSharedFile(home, "abc123"), "KEY=project\n")

	winner := func(scope string) string {
		t.Helper()
		layers, err := ResolveSecretsLayers(home, "abc123", "claude", scope)
		if err != nil {
			t.Fatal(err)
		}
		return layers[len(layers)-1].Values["KEY"]
	}
	if got := winner(model.SecretsScopeBoth); got != "project" {
		t.Fatalf("both: winner = %q, want the shared project file over global/<tool>", got)
	}
	if got := winner(model.SecretsScopeGlobal); got != "global-tool" {
		t.Fatalf("global: winner = %q", got)
	}

	write(config.HostSecretsProjectFile(home, "abc123", "claude"), "KEY=project-tool\n")
	if got := winner(model.SecretsScopeProject); got != "project-tool" {
		t.Fatalf("project: winner = %q", got)
	}
}

func TestResolveSecretReportsDriftWithoutFailing(t *testing.T) {
	lookup := SecretLookup{ID: "openai-api-key", EnvVars: []string{"CODEX_API_KEY", "OPENAI_API_KEY"}}
	layers := []SecretsLayer{
		{Path: "/secrets/global.env", Values: map[string]string{"OPENAI_API_KEY": "old"}},
		{Path: "/secrets/projects/h.env", Values: map[string]string{"CODEX_API_KEY": "new"}},
	}
	resolved, found, err := ResolveSecret(lookup, "", layers, nil, func(string) string { return "" })
	if err != nil || !found || resolved.Value != "new" || resolved.Layer != "/secrets/projects/h.env" {
		t.Fatalf("resolved = %+v, found = %v, err = %v", resolved, found, err)
	}
	if len(resolved.Drift) != 1 {
		t.Fatalf("drift = %q, want one notice about the ignored lower layer", resolved.Drift)
	}
}

func TestCanonicalLockTargetFollowsDirectoryLinks(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "dotfiles", "secrets")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "secrets")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	viaLink := canonicalLockTarget(filepath.Join(link, "global.env"))
	direct := canonicalLockTarget(filepath.Join(realDir, "global.env"))
	if viaLink != direct {
		t.Fatalf("lock targets differ: %q vs %q", viaLink, direct)
	}
}
