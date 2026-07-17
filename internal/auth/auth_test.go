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

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestReadAllEnvFromFile(t *testing.T) {
	t.Run("normal file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.env")
		if err := os.WriteFile(path, []byte("FOO=bar\nBAZ=qux\n"), 0644); err != nil {
			t.Fatal(err)
		}
		env, err := ReadAllEnvFromFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(env) != 2 {
			t.Fatalf("expected 2 keys, got %d", len(env))
		}
		if env["FOO"] != "bar" {
			t.Errorf("FOO = %q, want %q", env["FOO"], "bar")
		}
		if env["BAZ"] != "qux" {
			t.Errorf("BAZ = %q, want %q", env["BAZ"], "qux")
		}
	})

	t.Run("comments and blank lines", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.env")
		content := "# comment\n\nFOO=bar\n# another comment\nBAZ=qux\n"
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		env, err := ReadAllEnvFromFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(env) != 2 {
			t.Fatalf("expected 2 keys, got %d", len(env))
		}
	})

	t.Run("export prefix", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.env")
		if err := os.WriteFile(path, []byte("export FOO=bar\nexport BAZ=qux\n"), 0644); err != nil {
			t.Fatal(err)
		}
		env, err := ReadAllEnvFromFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if env["FOO"] != "bar" {
			t.Errorf("FOO = %q, want %q", env["FOO"], "bar")
		}
	})

	t.Run("quoted values", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.env")
		content := "FOO=\"bar baz\"\nBAZ='qux quux'\n"
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		env, err := ReadAllEnvFromFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if env["FOO"] != "bar baz" {
			t.Errorf("FOO = %q, want %q", env["FOO"], "bar baz")
		}
		if env["BAZ"] != "qux quux" {
			t.Errorf("BAZ = %q, want %q", env["BAZ"], "qux quux")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		env, err := ReadAllEnvFromFile("/nonexistent/path/test.env")
		if err != nil {
			t.Fatal(err)
		}
		if env != nil {
			t.Errorf("expected nil map for missing file, got %v", env)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.env")
		if err := os.WriteFile(path, []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
		env, err := ReadAllEnvFromFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(env) != 0 {
			t.Errorf("expected empty map, got %d keys", len(env))
		}
	})
}

func TestResolveLayeredSecrets(t *testing.T) {
	t.Run("two-layer merge", func(t *testing.T) {
		home := t.TempDir()

		// Layer 1: global.env
		globalDir := filepath.Dir(config.HostSecretsGlobalSharedFile(home))
		if err := os.MkdirAll(globalDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalSharedFile(home), []byte("GITHUB_TOKEN=global-token\nSHARED=from-global\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Layer 2: per-tool
		perToolDir := filepath.Dir(config.HostSecretsGlobalFile(home, "claude"))
		if err := os.MkdirAll(perToolDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalFile(home, "claude"), []byte("TOOL_KEY=tool-value\n"), 0644); err != nil {
			t.Fatal(err)
		}

		secrets, err := ResolveLayeredSecrets(home, "proj", "claude", model.SecretsScopeBoth)
		if err != nil {
			t.Fatal(err)
		}
		if secrets["GITHUB_TOKEN"] != "global-token" {
			t.Errorf("GITHUB_TOKEN = %q, want %q", secrets["GITHUB_TOKEN"], "global-token")
		}
		if secrets["SHARED"] != "from-global" {
			t.Errorf("SHARED = %q, want %q", secrets["SHARED"], "from-global")
		}
		if secrets["TOOL_KEY"] != "tool-value" {
			t.Errorf("TOOL_KEY = %q, want %q", secrets["TOOL_KEY"], "tool-value")
		}
	})

	t.Run("per-tool overrides global", func(t *testing.T) {
		home := t.TempDir()

		globalDir := filepath.Dir(config.HostSecretsGlobalSharedFile(home))
		if err := os.MkdirAll(globalDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalSharedFile(home), []byte("GITHUB_TOKEN=global-token\n"), 0644); err != nil {
			t.Fatal(err)
		}

		perToolDir := filepath.Dir(config.HostSecretsGlobalFile(home, "claude"))
		if err := os.MkdirAll(perToolDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalFile(home, "claude"), []byte("GITHUB_TOKEN=tool-token\n"), 0644); err != nil {
			t.Fatal(err)
		}

		secrets, err := ResolveLayeredSecrets(home, "proj", "claude", model.SecretsScopeBoth)
		if err != nil {
			t.Fatal(err)
		}
		if secrets["GITHUB_TOKEN"] != "tool-token" {
			t.Errorf("GITHUB_TOKEN = %q, want %q (per-tool should override global)", secrets["GITHUB_TOKEN"], "tool-token")
		}
	})

	t.Run("missing layers", func(t *testing.T) {
		home := t.TempDir()

		secrets, err := ResolveLayeredSecrets(home, "proj", "claude", model.SecretsScopeBoth)
		if err != nil {
			t.Fatal(err)
		}
		if len(secrets) != 0 {
			t.Errorf("expected empty map when no files exist, got %d keys", len(secrets))
		}
	})

	t.Run("only global layer", func(t *testing.T) {
		home := t.TempDir()

		globalDir := filepath.Dir(config.HostSecretsGlobalSharedFile(home))
		if err := os.MkdirAll(globalDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalSharedFile(home), []byte("TOKEN=abc\n"), 0644); err != nil {
			t.Fatal(err)
		}

		secrets, err := ResolveLayeredSecrets(home, "proj", "claude", model.SecretsScopeBoth)
		if err != nil {
			t.Fatal(err)
		}
		if secrets["TOKEN"] != "abc" {
			t.Errorf("TOKEN = %q, want %q", secrets["TOKEN"], "abc")
		}
	})

	t.Run("only per-tool layer", func(t *testing.T) {
		home := t.TempDir()

		perToolDir := filepath.Dir(config.HostSecretsGlobalFile(home, "claude"))
		if err := os.MkdirAll(perToolDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalFile(home, "claude"), []byte("TOKEN=def\n"), 0644); err != nil {
			t.Fatal(err)
		}

		secrets, err := ResolveLayeredSecrets(home, "proj", "claude", model.SecretsScopeBoth)
		if err != nil {
			t.Fatal(err)
		}
		if secrets["TOKEN"] != "def" {
			t.Errorf("TOKEN = %q, want %q", secrets["TOKEN"], "def")
		}
	})

	t.Run("project per-tool overrides global per-tool", func(t *testing.T) {
		home := t.TempDir()

		// Layer 1: global.env
		globalDir := filepath.Dir(config.HostSecretsGlobalSharedFile(home))
		if err := os.MkdirAll(globalDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalSharedFile(home), []byte("TOKEN=global\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Layer 2: global per-tool
		globalToolDir := filepath.Dir(config.HostSecretsGlobalFile(home, "claude"))
		if err := os.MkdirAll(globalToolDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalFile(home, "claude"), []byte("TOKEN=global-tool\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Layer 3: project per-tool
		projectToolDir := filepath.Dir(config.HostSecretsProjectFile(home, "proj", "claude"))
		if err := os.MkdirAll(projectToolDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsProjectFile(home, "proj", "claude"), []byte("TOKEN=project-tool\n"), 0644); err != nil {
			t.Fatal(err)
		}

		secrets, err := ResolveLayeredSecrets(home, "proj", "claude", model.SecretsScopeBoth)
		if err != nil {
			t.Fatal(err)
		}
		if secrets["TOKEN"] != "project-tool" {
			t.Errorf("TOKEN = %q, want %q", secrets["TOKEN"], "project-tool")
		}
	})

	t.Run("scope project skips global per-tool", func(t *testing.T) {
		home := t.TempDir()

		// Layer 1: global.env (always read)
		globalDir := filepath.Dir(config.HostSecretsGlobalSharedFile(home))
		if err := os.MkdirAll(globalDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalSharedFile(home), []byte("BASE=yes\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Layer 2: global per-tool (should be skipped with scope=project)
		globalToolDir := filepath.Dir(config.HostSecretsGlobalFile(home, "claude"))
		if err := os.MkdirAll(globalToolDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalFile(home, "claude"), []byte("TOOL=global-tool\n"), 0644); err != nil {
			t.Fatal(err)
		}

		secrets, err := ResolveLayeredSecrets(home, "proj", "claude", model.SecretsScopeProject)
		if err != nil {
			t.Fatal(err)
		}
		if secrets["BASE"] != "yes" {
			t.Errorf("BASE = %q, want %q", secrets["BASE"], "yes")
		}
		if _, ok := secrets["TOOL"]; ok {
			t.Errorf("TOOL should not be present with scope=project, got %q", secrets["TOOL"])
		}
	})

	t.Run("scope global skips project per-tool", func(t *testing.T) {
		home := t.TempDir()

		// Layer 2: global per-tool
		globalToolDir := filepath.Dir(config.HostSecretsGlobalFile(home, "claude"))
		if err := os.MkdirAll(globalToolDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsGlobalFile(home, "claude"), []byte("TOOL=global-tool\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Layer 3: project per-tool (should be skipped with scope=global)
		projectToolDir := filepath.Dir(config.HostSecretsProjectFile(home, "proj", "claude"))
		if err := os.MkdirAll(projectToolDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.HostSecretsProjectFile(home, "proj", "claude"), []byte("PROJ=project-tool\n"), 0644); err != nil {
			t.Fatal(err)
		}

		secrets, err := ResolveLayeredSecrets(home, "proj", "claude", model.SecretsScopeGlobal)
		if err != nil {
			t.Fatal(err)
		}
		if secrets["TOOL"] != "global-tool" {
			t.Errorf("TOOL = %q, want %q", secrets["TOOL"], "global-tool")
		}
		if _, ok := secrets["PROJ"]; ok {
			t.Errorf("PROJ should not be present with scope=global, got %q", secrets["PROJ"])
		}
	})
}

func TestParseEnvLines(t *testing.T) {
	t.Run("comments and blank lines", func(t *testing.T) {
		input := "# comment\n\nFOO=bar\n# another\nBAZ=qux\n"
		env, err := parseEnvLines(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(env) != 2 {
			t.Fatalf("expected 2 keys, got %d", len(env))
		}
		if env["FOO"] != "bar" {
			t.Errorf("FOO = %q, want %q", env["FOO"], "bar")
		}
		if env["BAZ"] != "qux" {
			t.Errorf("BAZ = %q, want %q", env["BAZ"], "qux")
		}
	})

	t.Run("export prefix", func(t *testing.T) {
		input := "export FOO=bar\nexport BAZ=qux\n"
		env, err := parseEnvLines(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		if env["FOO"] != "bar" {
			t.Errorf("FOO = %q, want %q", env["FOO"], "bar")
		}
		if env["BAZ"] != "qux" {
			t.Errorf("BAZ = %q, want %q", env["BAZ"], "qux")
		}
	})

	t.Run("quoted values", func(t *testing.T) {
		input := "FOO=\"bar baz\"\nBAZ='qux quux'\n"
		env, err := parseEnvLines(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		if env["FOO"] != "bar baz" {
			t.Errorf("FOO = %q, want %q", env["FOO"], "bar baz")
		}
		if env["BAZ"] != "qux quux" {
			t.Errorf("BAZ = %q, want %q", env["BAZ"], "qux quux")
		}
	})

	t.Run("equals in value", func(t *testing.T) {
		input := "FOO=bar=baz\n"
		env, err := parseEnvLines(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		if env["FOO"] != "bar=baz" {
			t.Errorf("FOO = %q, want %q", env["FOO"], "bar=baz")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		env, err := parseEnvLines(strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		if len(env) != 0 {
			t.Errorf("expected empty map, got %d keys", len(env))
		}
	})

	t.Run("line without equals", func(t *testing.T) {
		input := "NOEQUALSSIGN\nFOO=bar\n"
		env, err := parseEnvLines(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(env) != 1 {
			t.Fatalf("expected 1 key, got %d", len(env))
		}
		if env["FOO"] != "bar" {
			t.Errorf("FOO = %q, want %q", env["FOO"], "bar")
		}
	})
}

func TestResolveLayeredSecrets_APIKeyInGlobalShared(t *testing.T) {
	// Regression test: API key vars stored in global.env must be found
	// by ResolveLayeredSecrets. The old ResolveAPIKey function missed
	// this layer entirely.
	home := t.TempDir()

	globalDir := filepath.Dir(config.HostSecretsGlobalSharedFile(home))
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.HostSecretsGlobalSharedFile(home), []byte("ANTHROPIC_API_KEY=sk-ant-global\n"), 0644); err != nil {
		t.Fatal(err)
	}

	secrets, err := ResolveLayeredSecrets(home, "proj", "claude", model.SecretsScopeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if secrets["ANTHROPIC_API_KEY"] != "sk-ant-global" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want %q", secrets["ANTHROPIC_API_KEY"], "sk-ant-global")
	}
}
