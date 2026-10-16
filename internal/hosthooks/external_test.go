// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hosthooks

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func installExample(t *testing.T, name string) Provider {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3 unavailable")
	}
	source := filepath.Join("..", "..", "examples", "host-hooks", "features", name)
	root := t.TempDir()
	dest := filepath.Join(root, name)
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Load("feature:"+name, filepath.Join(dest, "hooks", "host"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestExternalOnlyProviders(t *testing.T) {
	herdr := installExample(t, "herdr")
	herdr.Config.Settings = map[string]any{"agents": map[string]any{"company": "claude-alias"}}
	request := Request{Action: "run", Tool: Tool{Name: "company", Entrypoint: []string{"wrapper", "literal argument"}}}
	var warnings []string
	runner := Runner{Warn: func(w string) { warnings = append(warnings, w) }}
	env, restart := runner.Prepare(context.Background(), []Provider{herdr}, request, []string{"PATH=" + os.Getenv("PATH"), "HERDR_ENV=1"})
	if !restart || EnvironmentMap(env)["HERDR_AGENT"] != "claude-alias" || len(warnings) > 0 {
		t.Fatalf("external prepare: %v %v %v", env, restart, warnings)
	}
	tint := installExample(t, "session-tint")
	tint.Config.Settings = map[string]any{"color": "#2a0f12"}
	var output bytes.Buffer
	scope := runner.Begin(context.Background(), []Provider{tint}, Request{Terminal: Terminal{StdoutIsTTY: true}}, os.Environ(), &output)
	// Cleanup data must survive deletion of the entire external package.
	if err := os.RemoveAll(tint.Dir); err != nil {
		t.Fatal(err)
	}
	scope.Close(Outcome{Status: "completed"})
	if output.String() != "\x1b]11;#2a0f12\x07\x1b]111\x07" {
		t.Fatalf("external tint %q", output.String())
	}
}

func TestExternalTitleAndTintHaveIndependentCleanup(t *testing.T) {
	title := installExample(t, "session-title")
	tint := installExample(t, "session-tint")
	tint.Config.Settings = map[string]any{"color": "#2a0f12"}
	request := Request{
		Action:   "attach",
		Tool:     Tool{Name: "codex"},
		Project:  Project{Path: "/work/payment-service"},
		Session:  &Session{Name: "target-session"},
		Terminal: Terminal{StdoutIsTTY: true},
	}
	titleEnter := "\x1b[22;2t\x1b]2;Enclave · codex · payment-service\x07"
	tintEnter := "\x1b]11;#2a0f12\x07"
	var output bytes.Buffer
	runner := Runner{Warn: func(w string) { t.Errorf("unexpected provider warning: %s", w) }}
	scope := runner.Begin(context.Background(), []Provider{title, tint}, request, os.Environ(), &output)
	if got := output.String(); got != titleEnter+tintEnter {
		t.Fatalf("title/tint enter = %q", got)
	}
	if err := os.RemoveAll(title.Dir); err != nil {
		t.Fatal(err)
	}
	scope.Close(Outcome{Status: "unknown"})
	scope.Close(Outcome{Status: "unknown"})
	if got := output.String(); got != titleEnter+tintEnter+"\x1b]111\x07\x1b[23;2t" {
		t.Fatalf("stored resets did not unwind independently exactly once: %q", got)
	}
}
