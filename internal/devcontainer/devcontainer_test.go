// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package devcontainer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestStripJSONCValidJSONUnchanged(t *testing.T) {
	input := []byte(`{"image":"node:22","remoteUser":"node"}`)
	if got := stripJSONC(input); !bytes.Equal(got, input) {
		t.Fatalf("expected valid JSON to remain unchanged, got %q", string(got))
	}
}

func TestStripJSONCParsesCommentsAndTrailingCommas(t *testing.T) {
	input := []byte(`{
  // line comment
  "image": "node:22",
  "runArgs": ["--foo",],
  /* block comment */
  "remoteUser": "node",
}`)

	var cfg config
	if err := json.Unmarshal(stripJSONC(input), &cfg); err != nil {
		t.Fatalf("unmarshal stripped JSONC: %v", err)
	}
	if cfg.Image != "node:22" {
		t.Fatalf("unexpected image: %q", cfg.Image)
	}
	if len(cfg.RunArgs) != 1 || cfg.RunArgs[0] != "--foo" {
		t.Fatalf("unexpected run args: %#v", cfg.RunArgs)
	}
	if cfg.RemoteUser != "node" {
		t.Fatalf("unexpected remote user: %q", cfg.RemoteUser)
	}
}

func TestStripJSONCPreservesStringContent(t *testing.T) {
	input := []byte(`{
  "image": "node:22",
  "containerEnv": {
    "RAW": "value,} and value,] and // not comment and /* not block */"
  },
}`)

	var cfg config
	if err := json.Unmarshal(stripJSONC(input), &cfg); err != nil {
		t.Fatalf("unmarshal stripped JSONC: %v", err)
	}
	if cfg.ContainerEnv["RAW"] != "value,} and value,] and // not comment and /* not block */" {
		t.Fatalf("string content changed: %q", cfg.ContainerEnv["RAW"])
	}
}

func TestResolveSpecParsesJSONCWithTrailingCommas(t *testing.T) {
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, model.DevcontainerDir, model.DevcontainerFilename)
	mustWriteFile(t, configPath, `{
  // comment
  "image": "node:22",
  "remoteUser": "node",
}`)

	spec, found, err := ResolveSpec(model.Project{RealDir: projectRoot, Dir: "/workspace/project"}, ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveSpec returned error: %v", err)
	}
	if !found {
		t.Fatalf("expected devcontainer config to be found")
	}
	if spec.Image != "node:22" {
		t.Fatalf("unexpected image: %q", spec.Image)
	}
	if spec.RuntimeConfig.RemoteUser != "node" {
		t.Fatalf("unexpected remote user: %q", spec.RuntimeConfig.RemoteUser)
	}
}

func TestResolveSpecParseErrorSingleQuoteHint(t *testing.T) {
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, model.DevcontainerDir, model.DevcontainerFilename)
	mustWriteFile(t, configPath, `{
  'image': "node:22"
}`)

	_, _, err := ResolveSpec(model.Project{RealDir: projectRoot, Dir: "/workspace/project"}, ResolveOptions{})
	if err == nil {
		t.Fatalf("expected ResolveSpec to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "invalid devcontainer.json") || !strings.Contains(msg, "line") {
		t.Fatalf("expected line information in error, got: %q", msg)
	}
	if !strings.Contains(msg, "single-quoted strings are not valid JSON") {
		t.Fatalf("expected single-quote hint, got: %q", msg)
	}
}

func TestResolveSpecParseErrorCommaHint(t *testing.T) {
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, model.DevcontainerDir, model.DevcontainerFilename)
	mustWriteFile(t, configPath, `{
  "image": "node:22",
  "runArgs": ["--foo",,]
}`)

	_, _, err := ResolveSpec(model.Project{RealDir: projectRoot, Dir: "/workspace/project"}, ResolveOptions{})
	if err == nil {
		t.Fatalf("expected ResolveSpec to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "invalid devcontainer.json") || !strings.Contains(msg, "line") {
		t.Fatalf("expected line information in error, got: %q", msg)
	}
	if !strings.Contains(msg, "unexpected comma") {
		t.Fatalf("expected comma hint, got: %q", msg)
	}
}

func TestResolveSpecHashStableAcrossCheckoutPaths(t *testing.T) {
	projectA := t.TempDir()
	projectB := t.TempDir()

	writeDockerfileSpecProject(t, projectA)
	writeDockerfileSpecProject(t, projectB)

	specA, found, err := ResolveSpec(model.Project{RealDir: projectA, Dir: "/workspace/project"}, ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveSpec project A: %v", err)
	}
	if !found {
		t.Fatalf("expected project A config to be found")
	}

	specB, found, err := ResolveSpec(model.Project{RealDir: projectB, Dir: "/workspace/project"}, ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveSpec project B: %v", err)
	}
	if !found {
		t.Fatalf("expected project B config to be found")
	}

	if specA.Hash == "" || specB.Hash == "" {
		t.Fatalf("expected non-empty hashes")
	}
	if specA.Hash != specB.Hash {
		t.Fatalf("expected identical hash across checkout paths, got %q and %q", specA.Hash, specB.Hash)
	}
}

func writeDockerfileSpecProject(t *testing.T, root string) {
	t.Helper()
	configPath := filepath.Join(root, model.DevcontainerDir, model.DevcontainerFilename)
	dockerfilePath := filepath.Join(root, model.DevcontainerDir, "Dockerfile")
	mustWriteFile(t, configPath, `{
  "build": {
    "dockerfile": "Dockerfile",
    "context": ".",
    "args": {
      "A": "1",
      "B": "2"
    }
  }
}`)
	mustWriteFile(t, dockerfilePath, "FROM debian:trixie-slim\n")
}

func TestResolveSpecRejectsLikelyNonDebianImage(t *testing.T) {
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, model.DevcontainerDir, model.DevcontainerFilename)
	mustWriteFile(t, configPath, `{"image":"node:22-alpine"}`)

	_, _, err := ResolveSpec(model.Project{RealDir: projectRoot, Dir: "/workspace/project"}, ResolveOptions{})
	if err == nil {
		t.Fatalf("expected ResolveSpec to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "appears non-Debian") {
		t.Fatalf("expected non-Debian guidance, got: %q", msg)
	}
	if !strings.Contains(msg, "--force-base-image") {
		t.Fatalf("expected force override hint, got: %q", msg)
	}
}

func TestResolveSpecAllowsLikelyNonDebianImageWithForce(t *testing.T) {
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, model.DevcontainerDir, model.DevcontainerFilename)
	mustWriteFile(t, configPath, `{"image":"node:22-alpine"}`)

	spec, found, err := ResolveSpec(model.Project{RealDir: projectRoot, Dir: "/workspace/project"}, ResolveOptions{
		ForceBaseImage: true,
	})
	if err != nil {
		t.Fatalf("ResolveSpec returned error: %v", err)
	}
	if !found {
		t.Fatalf("expected devcontainer config to be found")
	}
	if spec.BaseImage != "node:22-alpine" {
		t.Fatalf("unexpected base image: %q", spec.BaseImage)
	}
}

func TestResolveSpecAcceptsLikelyDebianImages(t *testing.T) {
	tests := []string{
		"node:22",
		"ubuntu:24.04",
		"debian:slim-buster",
		"ghcr.io/acme/custom-runtime:v1",
	}
	for _, image := range tests {
		t.Run(image, func(t *testing.T) {
			projectRoot := t.TempDir()
			configPath := filepath.Join(projectRoot, model.DevcontainerDir, model.DevcontainerFilename)
			mustWriteFile(t, configPath, `{"image":"`+image+`"}`)

			spec, found, err := ResolveSpec(model.Project{RealDir: projectRoot, Dir: "/workspace/project"}, ResolveOptions{})
			if err != nil {
				t.Fatalf("ResolveSpec returned error: %v", err)
			}
			if !found {
				t.Fatalf("expected devcontainer config to be found")
			}
			if spec.BaseImage != image {
				t.Fatalf("unexpected base image: %q", spec.BaseImage)
			}
		})
	}
}

func TestResolveSpecRejectsInvalidImageReference(t *testing.T) {
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, model.DevcontainerDir, model.DevcontainerFilename)
	mustWriteFile(t, configPath, `{"image":"node:22 alpine"}`)

	_, _, err := ResolveSpec(model.Project{RealDir: projectRoot, Dir: "/workspace/project"}, ResolveOptions{})
	if err == nil {
		t.Fatalf("expected ResolveSpec to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "invalid") {
		t.Fatalf("expected invalid image error, got: %q", msg)
	}
	if !strings.Contains(msg, "whitespace") {
		t.Fatalf("expected whitespace hint, got: %q", msg)
	}
}

func TestSummarizeUnsupportedDevcontainerFeatures(t *testing.T) {
	ignored, suggested := summarizeUnsupportedDevcontainerFeatures(map[string]interface{}{
		"ghcr.io/devcontainers/features/python:1":            map[string]interface{}{},
		"ghcr.io/devcontainers/features/node:2":              map[string]interface{}{},
		"ghcr.io/devcontainers/features/node:1":              map[string]interface{}{},
		" ghcr.io/devcontainers/features/github-cli@sha256 ": map[string]interface{}{},
		"example.com/devcontainer/features/unknown:latest":   map[string]interface{}{},
	})

	wantIgnored := []string{
		"example.com/devcontainer/features/unknown:latest",
		"ghcr.io/devcontainers/features/github-cli@sha256",
		"ghcr.io/devcontainers/features/node:1",
		"ghcr.io/devcontainers/features/node:2",
		"ghcr.io/devcontainers/features/python:1",
	}
	if !reflect.DeepEqual(ignored, wantIgnored) {
		t.Fatalf("ignored mismatch:\n got: %v\nwant: %v", ignored, wantIgnored)
	}

	wantSuggested := []string{"github-cli", "node-dev", "python-dev"}
	if !reflect.DeepEqual(suggested, wantSuggested) {
		t.Fatalf("suggested mismatch:\n got: %v\nwant: %v", suggested, wantSuggested)
	}
}

func TestNormalizeDevcontainerFeatureKeepsRegistryPort(t *testing.T) {
	in := "registry.example.com:5000/devcontainers/features/node:1"
	want := "registry.example.com:5000/devcontainers/features/node"
	if got := normalizeDevcontainerFeature(in); got != want {
		t.Fatalf("normalize mismatch: got %q want %q", got, want)
	}
}

func TestMapDevcontainerFeatureUnknown(t *testing.T) {
	if mapped, ok := mapDevcontainerFeature("ghcr.io/devcontainers/features/go:1"); ok || mapped != "" {
		t.Fatalf("expected unknown feature to return empty mapping, got %q (ok=%v)", mapped, ok)
	}
}

func TestExpandVarsLocalEnvAllowsSafeNames(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("USER", "tester")
	t.Setenv("LANG", "en_US.UTF-8")

	project := model.Project{RealDir: "/tmp/proj", Dir: "/workspace/project"}

	tests := map[string]struct {
		input string
		want  string
	}{
		"home":  {"${localEnv:HOME}", "/home/tester"},
		"user":  {"${localEnv:USER}", "tester"},
		"lang":  {"${localEnv:LANG}", "en_US.UTF-8"},
		"mixed": {"prefix-${localEnv:USER}-suffix", "prefix-tester-suffix"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := expandVars(tc.input, project, "/workspace/project", true); got != tc.want {
				t.Fatalf("expandVars(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestExpandVarsLocalEnvBlocksCredentialNames(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "AKIA-secret")
	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret")
	t.Setenv("MY_SECRET", "hunter2")
	t.Setenv("SOME_PASSWORD", "hunter2")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh.sock")
	t.Setenv("KUBECONFIG", "/home/tester/.kube/config")

	project := model.Project{RealDir: "/tmp/proj", Dir: "/workspace/project"}

	blocked := []string{
		"${localEnv:AWS_SECRET_ACCESS_KEY}",
		"${localEnv:GITHUB_TOKEN}",
		"${localEnv:OPENAI_API_KEY}",
		"${localEnv:ANTHROPIC_API_KEY}",
		"${localEnv:MY_SECRET}",
		"${localEnv:SOME_PASSWORD}",
		"${localEnv:SSH_AUTH_SOCK}",
		"${localEnv:KUBECONFIG}",
	}
	for _, in := range blocked {
		t.Run(in, func(t *testing.T) {
			if got := expandVars(in, project, "/workspace/project", true); got != "" {
				t.Fatalf("expandVars(%q) = %q, want empty", in, got)
			}
		})
	}
}

func TestExpandVarsLocalEnvCaseInsensitive(t *testing.T) {
	t.Setenv("my_token", "leak-me")

	project := model.Project{RealDir: "/tmp/proj", Dir: "/workspace/project"}

	if got := expandVars("${localEnv:my_token}", project, "/workspace/project", true); got != "" {
		t.Fatalf("expected lowercase _token to be blocked, got %q", got)
	}
}

func TestExpandVarsLocalEnvUnsetExpandsEmpty(t *testing.T) {
	if err := os.Unsetenv("ENCLAVE_TEST_UNSET"); err != nil {
		t.Fatalf("unset env: %v", err)
	}

	project := model.Project{RealDir: "/tmp/proj", Dir: "/workspace/project"}

	if got := expandVars("${localEnv:ENCLAVE_TEST_UNSET}", project, "/workspace/project", true); got != "" {
		t.Fatalf("expected unset var to expand to empty, got %q", got)
	}
}

func TestExpandVarsMountSourceDisablesLocalEnv(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	project := model.Project{RealDir: "/tmp/proj", Dir: "/workspace/project"}

	mountSrc := "type=bind,source=${localEnv:HOME}/.aws,target=/aws"
	got := expandVars(mountSrc, project, "/workspace/project", false)
	want := "type=bind,source=/.aws,target=/aws"
	if got != want {
		t.Fatalf("expandVars(mount) = %q, want %q (localEnv must not expand in mount sources)", got, want)
	}
}

func TestExpandMountsRejectsLocalEnvSource(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	project := model.Project{RealDir: "/tmp/proj", Dir: "/workspace/project"}
	mnts := mounts{
		{Raw: "type=bind,source=${localEnv:HOME}/.aws,target=/aws"},
	}
	got := expandMounts(mnts, project, "/workspace/project")
	if len(got) != 1 {
		t.Fatalf("expected 1 mount entry, got %d (%v)", len(got), got)
	}
	if strings.Contains(got[0], "/home/tester") {
		t.Fatalf("expected localEnv to NOT expand in mount source, got %q", got[0])
	}
}

func TestExpandArgsRejectsLocalEnv(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("DATABASE_URL", "postgres://user:pw@db/x")
	t.Setenv("USER", "tester")

	project := model.Project{RealDir: "/tmp/proj", Dir: "/workspace/project"}
	args := []string{
		"-v", "${localEnv:HOME}/.aws:/aws:ro",
		"-e", "STOLEN=${localEnv:DATABASE_URL}",
		"--label", "u=${localEnv:USER}",
	}
	got := expandArgs(args, project, "/workspace/project")
	for _, a := range got {
		if strings.Contains(a, "/home/tester") || strings.Contains(a, "postgres://") || strings.Contains(a, "tester") {
			t.Fatalf("expected localEnv NOT to expand in runArgs, got %q (full: %v)", a, got)
		}
	}
}

func TestIsLocalEnvNameAllowed(t *testing.T) {
	allowed := []string{"HOME", "USER", "LANG", "PATH", "TERM", "TZ"}
	for _, name := range allowed {
		if !isLocalEnvNameAllowed(name) {
			t.Errorf("expected %q to be allowed", name)
		}
	}
	denied := []string{
		"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "OPENAI_API_KEY",
		"ANTHROPIC_API_KEY", "MY_SECRET", "SOME_PASSWORD",
		"NPM_TOKEN", "PYPI_TOKEN", "DOCKER_AUTH_CONFIG",
		"SSH_AUTH_SOCK", "KUBECONFIG",
		"my_token", "lower_secret",
	}
	for _, name := range denied {
		if isLocalEnvNameAllowed(name) {
			t.Errorf("expected %q to be denied", name)
		}
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
