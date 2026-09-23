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

	"enclave/internal/model"
)

func TestAddGitConfigMountForwardsHostGlobalIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		homeConfig string
		xdgConfig  string
		customXDG  bool
		globalFile string
		wantName   string
		wantEmail  string
	}{
		{
			name:       "home config",
			homeConfig: "[user]\n name = Home User\n email = home@example.org\n",
			wantName:   "Home User",
			wantEmail:  "home@example.org",
		},
		{
			name:      "default XDG config without home config",
			xdgConfig: "[user]\n name = XDG User\n email = xdg@example.org\n",
			wantName:  "XDG User",
			wantEmail: "xdg@example.org",
		},
		{
			name:      "custom XDG config",
			xdgConfig: "[user]\n name = Custom User\n email = custom@example.org\n",
			customXDG: true,
			wantName:  "Custom User",
			wantEmail: "custom@example.org",
		},
		{
			name:       "preferences in home and identity in XDG",
			homeConfig: "[core]\n editor = vi\n",
			xdgConfig:  "[user]\n name = XDG User\n email = xdg@example.org\n",
			wantName:   "XDG User",
			wantEmail:  "xdg@example.org",
		},
		{
			name:       "GIT_CONFIG_GLOBAL overrides home and XDG",
			homeConfig: "[user]\n name = Home User\n email = home@example.org\n",
			xdgConfig:  "[user]\n name = XDG User\n email = xdg@example.org\n",
			globalFile: "[user]\n name = Override User\n email = override@example.org\n",
			wantName:   "Override User",
			wantEmail:  "override@example.org",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.homeConfig != "" {
				writeFile(t, filepath.Join(home, ".gitconfig"), tc.homeConfig)
			}
			if tc.xdgConfig != "" {
				xdg := filepath.Join(home, ".config")
				if tc.customXDG {
					xdg = filepath.Join(t.TempDir(), "custom-xdg")
					t.Setenv("XDG_CONFIG_HOME", xdg)
				}
				writeFile(t, filepath.Join(xdg, "git", "config"), tc.xdgConfig)
			}
			if tc.globalFile != "" {
				global := filepath.Join(t.TempDir(), "global-git-config")
				writeFile(t, global, tc.globalFile)
				t.Setenv("GIT_CONFIG_GLOBAL", global)
			}

			r := &Runtime{host: model.Host{Home: home}, project: model.Project{Dir: t.TempDir()}}
			mounts := newMountAccumulator(nil, nil)
			identity, err := r.addGitConfigMount(mounts)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateGitIdentity(identity, mounts.Env()); err != nil {
				t.Fatal(err)
			}
			if got, ok := lookupEnv(mounts.Env(), model.EnvGitName); !ok || got != tc.wantName {
				t.Errorf("forwarded name = %q, present = %t; want %q", got, ok, tc.wantName)
			}
			if got, ok := lookupEnv(mounts.Env(), model.EnvGitEmail); !ok || got != tc.wantEmail {
				t.Errorf("forwarded email = %q, present = %t; want %q", got, ok, tc.wantEmail)
			}
			wantMounts := 0
			if tc.homeConfig != "" {
				wantMounts = 1
			}
			if len(mounts.Mounts()) != wantMounts {
				t.Errorf("mount count = %d, want %d", len(mounts.Mounts()), wantMounts)
			}
		})
	}
}

func TestAddGitConfigMountResolvesIncludedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		xdg         bool
		conditional bool
	}{
		{name: "home include"},
		{name: "XDG include with home preferences", xdg: true},
		{name: "project gitdir includeIf", conditional: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			projectDir := filepath.Join(home, "work", "project")
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command("git", "init", "-q", projectDir).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, output)
			}
			writeFile(t, filepath.Join(home, ".git-identity"), "[user]\n name = Included User\n email = included@example.org\n")
			include := "[include]\n path = ~/.git-identity\n"
			if tc.conditional {
				include = "[includeIf \"gitdir:" + filepath.Join(home, "work") + "/\"]\n path = ~/.git-identity\n"
			}
			if tc.xdg {
				writeFile(t, filepath.Join(home, ".gitconfig"), "[core]\n editor = vi\n")
				writeFile(t, filepath.Join(home, ".config", "git", "config"), include)
			} else {
				writeFile(t, filepath.Join(home, ".gitconfig"), include)
			}
			mounts := newMountAccumulator(nil, nil)
			r := &Runtime{host: model.Host{Home: home}, project: model.Project{Dir: projectDir}}
			identity, err := r.addGitConfigMount(mounts)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateGitIdentity(identity, mounts.Env()); err != nil {
				t.Fatal(err)
			}
			if got, ok := lookupEnv(mounts.Env(), model.EnvGitName); !ok || got != "Included User" {
				t.Errorf("name = %q, present = %t", got, ok)
			}
			if got, ok := lookupEnv(mounts.Env(), model.EnvGitEmail); !ok || got != "included@example.org" {
				t.Errorf("email = %q, present = %t", got, ok)
			}
		})
	}
}

func TestGitIdentityPreflightRejectsIncompleteIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		config     string
		wantReason string
		noGit      bool
	}{
		{"no config", "", "git identity is incomplete", false},
		{"name only", "[user]\n name = Partial User\n", "git identity is incomplete", false},
		{"email only", "[user]\n email = partial@example.org\n", "git identity is incomplete", false},
		{"unreadable config", "[user\n", "bad config", false},
		{"Git unavailable", "", "executable file not found", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.config != "" {
				writeFile(t, filepath.Join(home, ".gitconfig"), tc.config)
			}
			if tc.noGit {
				t.Setenv("PATH", t.TempDir())
			}
			mounts := newMountAccumulator(nil, nil)
			r := &Runtime{host: model.Host{Home: home}, project: model.Project{Dir: t.TempDir()}}
			identity, err := r.addGitConfigMount(mounts)
			if err == nil {
				err = validateGitIdentity(identity, mounts.Env())
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("preflight error = %v, want %q", err, tc.wantReason)
			}
		})
	}
}

func TestGitIdentityPreflightAcceptsRepositoryIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		homeConfig string
		localName  string
		localEmail string
	}{
		{"local only", "", "Repository User", "repo@example.org"},
		{"global name with local email", "[user]\n name = Global User\n", "", "repo@example.org"},
		{"global email with local name", "[user]\n email = global@example.org\n", "Repository User", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			projectDir := filepath.Join(t.TempDir(), "project")
			if output, err := exec.Command("git", "init", "-q", projectDir).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, output)
			}
			if tc.homeConfig != "" {
				writeFile(t, filepath.Join(home, ".gitconfig"), tc.homeConfig)
			}
			for key, value := range map[string]string{"user.name": tc.localName, "user.email": tc.localEmail} {
				if value == "" {
					continue
				}
				if output, err := exec.Command("git", "-C", projectDir, "config", "--local", key, value).CombinedOutput(); err != nil {
					t.Fatalf("set %s: %v\n%s", key, err, output)
				}
			}
			r := &Runtime{host: model.Host{Home: home}, project: model.Project{Dir: projectDir}}
			mounts := newMountAccumulator(nil, nil)
			identity, err := r.addGitConfigMount(mounts)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateGitIdentity(identity, mounts.Env()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitIdentityPreflightAcceptsWorktreeIdentity(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	if output, err := exec.Command("git", "init", "-q", projectDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	for _, setting := range []struct{ scope, key, value string }{
		{"--local", "extensions.worktreeConfig", "true"},
		{"--worktree", "user.name", "Worktree User"},
		{"--worktree", "user.email", "worktree@example.org"},
	} {
		args := []string{"-C", projectDir, "config", setting.scope, setting.key, setting.value}
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("set %s: %v\n%s", setting.key, err, output)
		}
	}
	r := &Runtime{host: model.Host{Home: t.TempDir()}, project: model.Project{Dir: projectDir}}
	mounts := newMountAccumulator(nil, nil)
	identity, err := r.addGitConfigMount(mounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitIdentity(identity, mounts.Env()); err != nil {
		t.Fatal(err)
	}
	if identity.effectiveName != "Worktree User" || identity.effectiveEmail != "worktree@example.org" {
		t.Errorf("effective identity = %q <%s>", identity.effectiveName, identity.effectiveEmail)
	}
	if _, ok := lookupEnv(mounts.Env(), model.EnvGitName); ok {
		t.Error("forwarded worktree name as global identity")
	}
	if _, ok := lookupEnv(mounts.Env(), model.EnvGitEmail); ok {
		t.Error("forwarded worktree email as global identity")
	}
}

func TestGitIdentityPreflightAcceptsSessionEnv(t *testing.T) {
	r := &Runtime{host: model.Host{Home: t.TempDir()}, project: model.Project{Dir: t.TempDir()}}
	mounts := newMountAccumulator(nil, []string{
		"GIT_AUTHOR_NAME=Author", "GIT_AUTHOR_EMAIL=author@example.org",
		"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@example.org",
	})
	identity, err := r.addGitConfigMount(mounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitIdentity(identity, mounts.Env()); err != nil {
		t.Fatal(err)
	}
	mounts = newMountAccumulator(nil, nil)
	identity, err = r.addGitConfigMount(mounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitIdentity(identity, []string{
		"GIT_AUTHOR_NAME=Author", "GIT_AUTHOR_EMAIL=author@example.org",
		"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@example.org",
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateGitIdentity(identity, []string{"GIT_AUTHOR_NAME=Author"}); err == nil {
		t.Error("accepted an incomplete environment identity")
	}
}

func TestGitIdentityPreflightForwardsSystemIdentity(t *testing.T) {
	systemConfig := filepath.Join(t.TempDir(), "system-gitconfig")
	writeFile(t, systemConfig, "[user]\n name = System User\n email = system@example.org\n")
	t.Setenv("GIT_CONFIG_SYSTEM", systemConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	r := &Runtime{host: model.Host{Home: t.TempDir()}, project: model.Project{Dir: t.TempDir()}}
	mounts := newMountAccumulator(nil, nil)
	identity, err := r.addGitConfigMount(mounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitIdentity(identity, mounts.Env()); err != nil {
		t.Fatal(err)
	}
	if got, _ := lookupEnv(mounts.Env(), model.EnvGitName); got != "System User" {
		t.Errorf("forwarded name = %q", got)
	}
	if got, _ := lookupEnv(mounts.Env(), model.EnvGitEmail); got != "system@example.org" {
		t.Errorf("forwarded email = %q", got)
	}
}

func TestGitIdentityPreflightRejectsEmptyLocalOverride(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".gitconfig"), "[user]\n name = Global User\n email = global@example.org\n")
	projectDir := filepath.Join(t.TempDir(), "project")
	if output, err := exec.Command("git", "init", "-q", projectDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	if output, err := exec.Command("git", "-C", projectDir, "config", "--local", "user.name", "").CombinedOutput(); err != nil {
		t.Fatalf("clear local name: %v\n%s", err, output)
	}
	r := &Runtime{host: model.Host{Home: home}, project: model.Project{Dir: projectDir}}
	mounts := newMountAccumulator(nil, nil)
	identity, err := r.addGitConfigMount(mounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitIdentity(identity, mounts.Env()); err == nil {
		t.Error("accepted an empty repository override")
	}
}
