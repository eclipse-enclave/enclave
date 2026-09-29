// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntrypointGitIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       []string
		wantName  string
		wantEmail string
	}{
		{"forwarded", []string{"ENCLAVE_GIT_NAME=Host User", "ENCLAVE_GIT_EMAIL=host@example.org"}, "Host User", "host@example.org"},
		{"absent", nil, "", ""},
		{"partial", []string{"ENCLAVE_GIT_NAME=Partial User"}, "Partial User", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			env := append([]string{"USER=agent"}, tc.env...)
			output, err := runEntrypointCommandInHome(t, home, env, "true")
			if err != nil {
				t.Fatalf("entrypoint: %v\n%s", err, output)
			}
			configPath := filepath.Join(home, ".gitconfig")
			for key, want := range map[string]string{"user.name": tc.wantName, "user.email": tc.wantEmail} {
				cmd := exec.Command("git", "config", "--file", configPath, "--get", key)
				got, err := cmd.Output()
				if want == "" && err == nil {
					t.Errorf("%s unexpectedly set to %q", key, got)
				} else if want != "" && (err != nil || strings.TrimSpace(string(got)) != want) {
					t.Errorf("%s = %q (error %v), want %q", key, got, err, want)
				}
			}
			cmd := exec.Command("git", "config", "--file", configPath, "--get", "user.useConfigOnly")
			if got, err := cmd.Output(); err != nil || strings.TrimSpace(string(got)) != "true" {
				t.Errorf("user.useConfigOnly = %q (error %v)", got, err)
			}
			if tc.wantEmail == "" {
				cmd = exec.Command("git", "var", "GIT_COMMITTER_IDENT")
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
				if got, err := cmd.CombinedOutput(); err == nil {
					t.Errorf("Git accepted an incomplete identity: %s", got)
				}
			}
		})
	}
}

func TestEntrypointGitIdentityRespectsRepositoryConfig(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", projectDir},
		{"-C", projectDir, "config", "user.name", "Repository User"},
		{"-C", projectDir, "config", "user.email", "repo@example.org"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	configPath := filepath.Join(projectDir, ".git", "config")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	output, err := runEntrypointCommandInHome(t, home, []string{
		"USER=agent",
		"ENCLAVE_GIT_NAME=Host User",
		"ENCLAVE_GIT_EMAIL=host@example.org",
	}, "git", "-C", projectDir, "var", "GIT_COMMITTER_IDENT")
	if err != nil {
		t.Fatalf("entrypoint: %v\n%s", err, output)
	}
	if !strings.Contains(output, "Repository User <repo@example.org>") {
		t.Errorf("repository identity did not win: %s", output)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("entrypoint changed the repository's .git/config")
	}
}

func TestEntrypointGitIdentityUsesRepositoryConfigWithoutGlobalIdentity(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	for _, args := range [][]string{
		{"init", "-q", projectDir},
		{"-C", projectDir, "config", "user.name", "Repository User"},
		{"-C", projectDir, "config", "user.email", "repo@example.org"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	output, err := runEntrypointCommandInHome(t, home, []string{
		"USER=agent",
	}, "git", "-C", projectDir, "var", "GIT_COMMITTER_IDENT")
	if err != nil {
		t.Fatalf("entrypoint: %v\n%s", err, output)
	}
	if !strings.Contains(output, "Repository User <repo@example.org>") {
		t.Errorf("repository identity was not used: %s", output)
	}
	for _, key := range []string{"user.name", "user.email"} {
		if got, err := exec.Command("git", "config", "--file", filepath.Join(home, ".gitconfig"), "--get", key).Output(); err == nil {
			t.Errorf("%s unexpectedly set globally to %q", key, got)
		}
	}
}

func TestEntrypointGitIdentityReplacesDuplicateValues(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, ".gitconfig")
	gitconfigSource := filepath.Join(t.TempDir(), "host_gitconfig")
	writeFile(t, gitconfigSource, "[alias]\n short-status = status --short\n[user]\n name = First\n email = first@example.org\n[user]\n name = Second\n email = second@example.org\n")
	output, err := runEntrypointCommandInHome(t, home, []string{
		"USER=agent",
		"ENCLAVE_HOST_GITCONFIG_PATH=" + gitconfigSource,
		"ENCLAVE_GIT_NAME=Host User",
		"ENCLAVE_GIT_EMAIL=host@example.org",
	}, "true")
	if err != nil {
		t.Fatalf("entrypoint: %v\n%s", err, output)
	}
	for key, want := range map[string]string{
		"user.name":          "Host User\n",
		"user.email":         "host@example.org\n",
		"alias.short-status": "status --short\n",
	} {
		got, err := exec.Command("git", "config", "--file", configPath, "--get-all", key).Output()
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (error %v), want %q", key, got, err, want)
		}
	}
}
