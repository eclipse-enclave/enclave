// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/a2a"
)

const repoRoot = "../../.."

var claudeExtension = filepath.Join(repoRoot, "extensions", "tools", "claude")

func TestClaudeA2AMCPTemplate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(claudeExtension, "templates", "a2a-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// No timeout or env: a per-server timeout would change Claude's abort floors
	// (P3), and the server inherits ENCLAVE_A2A_* from the session.
	want := map[string]any{"mcpServers": map[string]any{"enclave-a2a": map[string]any{
		"command": "enclave-a2a-agent",
		"args":    []any{"mcp"},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("a2a-mcp.json = %v, want %v", got, want)
	}
}

// Every image bakes every template, so the A2A one must not change what a
// non-A2A session gets: the settings template stays free of A2A content, and
// baking with or without the A2A template yields identical files otherwise.
func TestClaudeA2ATemplateLeavesSettingsUnchanged(t *testing.T) {
	settings, err := os.ReadFile(filepath.Join(claudeExtension, "templates", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(settings, []byte("mcpServers")) || bytes.Contains(bytes.ToLower(settings), []byte("a2a")) {
		t.Fatal("templates/settings.json is applied to every session and must not carry A2A wiring")
	}
	spec, err := os.ReadFile(filepath.Join(claudeExtension, "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(spec, []byte("settingsFile: claude-settings.json\n")) {
		t.Fatal("spec.yaml settingsFile is no longer claude-settings.json; recheck that the A2A template cannot be selected")
	}

	withA2A := bakeTemplates(t, filepath.Join(repoRoot, "extensions", "tools"))
	tools := t.TempDir()
	if err := os.CopyFS(filepath.Join(tools, "claude", "templates"), os.DirFS(filepath.Join(claudeExtension, "templates"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(tools, "claude", "templates", "a2a-mcp.json")); err != nil {
		t.Fatal(err)
	}
	withoutA2A := bakeTemplates(t, tools)

	if _, ok := withA2A["claude-a2a-mcp.json"]; !ok {
		t.Fatal("claude-a2a-mcp.json not baked")
	}
	if !bytes.Equal(withA2A["claude-settings.json"], settings) {
		t.Fatal("baked claude-settings.json differs from templates/settings.json")
	}
	delete(withA2A, "claude-a2a-mcp.json")
	for name := range withA2A {
		if !strings.HasPrefix(name, "claude-") {
			delete(withA2A, name)
		}
	}
	if !reflect.DeepEqual(withA2A, withoutA2A) {
		t.Fatalf("baked claude templates differ beyond claude-a2a-mcp.json: %v vs %v", keys(withA2A), keys(withoutA2A))
	}
}

func bakeTemplates(t *testing.T, toolsDir string) map[string][]byte {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	out := t.TempDir()
	script, err := filepath.Abs(filepath.Join(repoRoot, "runtime-assets", "build-scripts", "install-tool-templates.sh"))
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(toolsDir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "ENCLAVE_TOOLS_DIR="+abs, "ENCLAVE_TEMPLATES_DIR="+out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install-tool-templates.sh: %v\n%s", err, msg)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string][]byte, len(entries))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(out, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = data
	}
	return files
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestProvenanceSkillMatchesHeader(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(claudeExtension, "skills", "a2a-provenance", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := string(raw)
	if !strings.HasPrefix(skill, "---\nname: a2a-provenance\ndescription: ") {
		t.Fatal("SKILL.md frontmatter must start with name a2a-provenance and a description")
	}
	header := fmt.Sprintf(a2a.ProvenanceFormat, "<group>", "<member>", "<role>", "<generation>", "<taskID>", "<contextID>")
	if !strings.Contains(skill, header) {
		t.Fatalf("SKILL.md does not show the header the executor writes: %s", header)
	}
	for _, tool := range Tools {
		if !strings.Contains(skill, tool.Name) {
			t.Errorf("SKILL.md does not mention tool %s", tool.Name)
		}
	}
}
