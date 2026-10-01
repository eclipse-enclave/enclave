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
	"sort"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProtectGitConfigFiles(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "linked worktree"}[linked], func(t *testing.T) {
			home := resolvedTempDir(t)
			main := filepath.Join(resolvedTempDir(t), "main")
			git := func(dir string, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
				cmd.Env = append(os.Environ(), "HOME="+home)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSuffix(string(out), "\n")
			}
			if err := os.MkdirAll(main, 0o755); err != nil {
				t.Fatal(err)
			}
			git(main, "init", "-q")
			git(main, "config", "user.name", "Test User")
			git(main, "config", "user.email", "test@example.org")
			git(main, "commit", "-q", "--allow-empty", "-m", "initial")
			project := main
			if linked {
				project = filepath.Join(resolvedTempDir(t), "linked")
				git(main, "worktree", "add", "-q", "-b", "linked", project)
			}
			git(project, "config", "extensions.worktreeConfig", "true")
			git(main, "config", "--worktree", "user.name", "Main User")
			sibling := filepath.Join(resolvedTempDir(t), "sibling")
			git(main, "worktree", "add", "-q", "-b", "sibling", sibling)
			git(sibling, "config", "--worktree", "user.name", "Sibling User")
			git(project, "config", "--worktree", "user.name", "Worktree User")
			included := filepath.Join(project, "identity include")
			writeFile(t, included, "[user]\n email = included@example.org\n")
			git(project, "config", "--add", "include.path", included)
			empty := filepath.Join(project, "empty include")
			writeFile(t, empty, "")
			git(project, "config", "--add", "include.path", empty)
			unexposed := filepath.Join(home, "outside")
			writeFile(t, unexposed, "[alias]\n s = status\n")
			git(project, "config", "--add", "include.path", unexposed)
			writeFile(t, filepath.Join(project, ".gitconfig"), "[alias]\n s = status\n")
			git(project, "config", "--add", "include.path", filepath.Join(project, ".gitconfig"))
			r := &Runtime{host: model.Host{Home: home}, project: model.Project{Dir: project, RealDir: project}}
			acc := newMountAccumulator([]backend.Mount{bindMount(project, project, false), bindMount(project, "/workspace", false)}, nil)
			r.addWorktreeMetadataMounts(acc)
			if err := r.protectGitConfigFiles(acc); err != nil {
				t.Fatal(err)
			}
			for _, source := range []string{git(project, "rev-parse", "--git-path", "config"), git(project, "rev-parse", "--git-path", "config.worktree"), included, empty, filepath.Join(project, ".gitconfig")} {
				if !filepath.IsAbs(source) {
					source = filepath.Join(project, source)
				}
				found := false
				for _, mount := range acc.Mounts() {
					if mount.Source == source && mount.ContainerPath == source && mount.ReadOnly {
						found = true
					}
				}
				if !found {
					t.Errorf("missing read-only config mount for %s: %+v", source, acc.Mounts())
				}
			}
			for _, source := range []string{
				filepath.Join(main, ".git", "config.worktree"),
				filepath.Join(main, ".git", "worktrees", "sibling", "config.worktree"),
				filepath.Join(main, ".git", "worktrees", "sibling", "commondir"),
				filepath.Join(main, ".git", "worktrees", "sibling", "gitdir"),
			} {
				found := false
				for _, mount := range acc.Mounts() {
					found = found || (mount.Source == source && mount.ReadOnly)
				}
				if !found {
					t.Errorf("missing protected sibling metadata %s", source)
				}
			}
			for _, mount := range acc.Mounts() {
				if mount.Source == unexposed {
					t.Error("config protection exposed an unmounted host include")
				}
			}
			if acc.Mounts()[0].ReadOnly {
				t.Fatal("project must remain writable")
			}
			if linked {
				withoutMetadata := newMountAccumulator([]backend.Mount{bindMount(project, project, false)}, nil)
				if err := r.protectGitConfigFiles(withoutMetadata); err != nil {
					t.Fatal(err)
				}
				for _, mount := range withoutMetadata.Mounts() {
					if !strings.HasPrefix(mount.Source, project+string(filepath.Separator)) && mount.Source != project {
						t.Errorf("protection exposed unmounted worktree metadata: %+v", mount)
					}
				}
			}
			t.Run("mount integration", func(t *testing.T) {
				testReadOnlyGitConfigWrites(t, project, acc.Mounts())
			})
		})
	}
}

func TestHostGitConfigPathsPreservesNewlines(t *testing.T) {
	home, project := resolvedTempDir(t), resolvedTempDir(t)
	gitDir := filepath.Join(resolvedTempDir(t), "metadata\nwith-newline")
	cmd := exec.Command("git", "init", "-q", "--separate-git-dir", gitDir, project)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	paths, err := hostGitConfigPaths(home, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config", "config.worktree", "commondir", "gitdir"} {
		want := filepath.Join(gitDir, name)
		found := false
		for _, path := range paths {
			found = found || path == want
		}
		if !found {
			t.Errorf("missing intact path %q in %q", want, paths)
		}
	}
}

func TestProtectEmptyHostGitConfigFiles(t *testing.T) {
	for _, mode := range []string{"defaults", "overrides", "system disabled"} {
		t.Run(mode, func(t *testing.T) {
			root := resolvedTempDir(t)
			home, project := filepath.Join(root, "home"), filepath.Join(root, "project")
			xdg := filepath.Join(home, "custom-xdg")
			for _, dir := range []string{home, project} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			homeConfig := filepath.Join(home, ".gitconfig")
			xdgConfig := filepath.Join(xdg, "git", "config")
			globalOverride := filepath.Join(project, "global")
			systemOverride := filepath.Join(project, "system")
			for _, path := range []string{homeConfig, xdgConfig, globalOverride, systemOverride} {
				writeFile(t, path, "")
			}
			t.Setenv("XDG_CONFIG_HOME", xdg)
			t.Setenv("GIT_CONFIG_GLOBAL", "")
			if err := os.Unsetenv("GIT_CONFIG_GLOBAL"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_SYSTEM", "system")
			t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
			want := map[string]bool{
				homeConfig: true, xdgConfig: true,
				globalOverride: false, systemOverride: true,
			}
			if mode == "overrides" {
				t.Setenv("GIT_CONFIG_GLOBAL", "global")
				want[homeConfig], want[xdgConfig], want[globalOverride] = false, false, true
			}
			if mode == "system disabled" {
				t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
				want[systemOverride] = false
			}
			r := &Runtime{host: model.Host{Home: home}, project: model.Project{Dir: project}}
			acc := newMountAccumulator([]backend.Mount{bindMount(root, root, false)}, nil)
			if err := r.protectGitConfigFiles(acc); err != nil {
				t.Fatal(err)
			}
			for path, protected := range want {
				found := false
				for _, mount := range acc.Mounts() {
					found = found || (mount.Source == path && mount.ReadOnly)
				}
				if found != protected {
					t.Errorf("protection for %s = %v, want %v", path, found, protected)
				}
			}
		})
	}
}

func TestUnreferencedProjectGitConfigRemainsWritable(t *testing.T) {
	home, project := resolvedTempDir(t), resolvedTempDir(t)
	cmd := exec.Command("git", "init", "-q", project)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	config := filepath.Join(project, ".gitconfig")
	writeFile(t, config, "[alias]\n s = status\n")
	r := &Runtime{host: model.Host{Home: home}, project: model.Project{Dir: project}}
	acc := newMountAccumulator([]backend.Mount{bindMount(project, project, false)}, nil)
	if err := r.protectGitConfigFiles(acc); err != nil {
		t.Fatal(err)
	}
	for _, mount := range acc.Mounts() {
		if mount.Source == config || (mount.ContainerPath == project && mount.ReadOnly) {
			t.Fatalf("unreferenced project config was protected: %+v", mount)
		}
	}
}

func TestProtectGitConfigOutsideRepositoryDoesNotCreateFiles(t *testing.T) {
	project := t.TempDir()
	r := &Runtime{host: model.Host{Home: t.TempDir()}, project: model.Project{Dir: project}}
	acc := newMountAccumulator([]backend.Mount{bindMount(project, project, false)}, nil)
	if err := r.protectGitConfigFiles(acc); err != nil {
		t.Fatal(err)
	}
	if len(acc.Mounts()) != 1 || acc.Mounts()[0].ReadOnly {
		t.Fatalf("unexpected mounts: %+v", acc.Mounts())
	}
	files, err := os.ReadDir(project)
	if err != nil || len(files) != 0 {
		t.Fatalf("protection created files: %v, %v", files, err)
	}
}

func testReadOnlyGitConfigWrites(t *testing.T, project string, mounts []backend.Mount) {
	t.Helper()
	if out, err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").CombinedOutput(); err != nil {
		t.Skipf("mount integration check unavailable: %v: %s", err, out)
	}
	args := []string{"--user", "--map-root-user", "--mount", "sh", "-eu", "-c", `
project=$1
shift
mount --bind "$project" "$project"
for file do
  mount --bind "$file" "$file"
  if [ -f "$file" ]; then mount -o remount,bind,ro "$file"; fi
done
cd "$project"
config=$(git rev-parse --git-path config)
common=$(git rev-parse --git-common-dir)
for file in "$common/config.worktree" "$common"/worktrees/*/config.worktree "$common"/worktrees/*/commondir "$common"/worktrees/*/gitdir; do
  [ -f "$file" ] || continue
  if printf 'modified' >> "$file"; then exit 1; fi
done
if git config --local core.hooksPath /untrusted; then exit 1; fi
if git config --worktree user.email attacker@example.org; then exit 1; fi
if printf 'modified' > "$config"; then exit 1; fi
if rm "$config"; then exit 1; fi
printf 'replacement' > "$config.replacement"
if mv "$config.replacement" "$config"; then exit 1; fi
if mv .git .git-moved; then exit 1; fi
if printf 'modified' > .gitconfig; then exit 1; fi
printf 'content' > tracked.txt
git add tracked.txt
git -c user.name=Tester -c user.email=test@example.org commit -q -m test
`, "git-config-test", project}
	// Match the parent-first ordering used by both backends.
	mounts = append([]backend.Mount(nil), mounts...)
	sort.SliceStable(mounts, func(i, j int) bool {
		return strings.Count(mounts[i].ContainerPath, "/") < strings.Count(mounts[j].ContainerPath, "/")
	})
	for _, mount := range mounts {
		if mount.Source == mount.ContainerPath && mount.Source != project {
			args = append(args, mount.Source)
		}
	}
	if out, err := exec.Command("unshare", args...).CombinedOutput(); err != nil {
		t.Fatalf("read-only config integration: %v\n%s", err, out)
	}
}

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
			if wantMounts == 1 && len(mounts.Mounts()) == 1 {
				mount := mounts.Mounts()[0]
				if mount.Source != filepath.Join(home, ".gitconfig") || mount.ContainerPath != "/tmp/host_gitconfig" || !mount.ReadOnly {
					t.Errorf("host Git config must be mounted read-only at the staging path: %+v", mount)
				}
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
