// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gitworktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, bare bool) (anchor, checkout string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	anchor = filepath.Join(root, "repo")
	git(t, root, "init", anchor)
	git(t, anchor, "commit", "--allow-empty", "-m", "initial")
	if bare {
		common := filepath.Join(root, ".bare")
		git(t, root, "clone", "--bare", anchor, common)
		anchor = common
	}
	checkout = filepath.Join(root, "sibling")
	git(t, anchor, "worktree", "add", "-b", "sibling", checkout)
	return
}

func TestReadRegularFileRejectsOversizedMetadata(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, ".git"), strings.Repeat("x", maxMetadataBytes+1))
	if _, err := readRegularFile(dir, ".git", maxMetadataBytes); err == nil {
		t.Fatal("accepted oversized metadata")
	}
}

func TestResolveLayouts(t *testing.T) {
	for _, bare := range []bool{false, true} {
		t.Run(map[bool]string{true: "bare", false: "ordinary"}[bare], func(t *testing.T) {
			anchor, checkout := fixture(t, bare)
			result := Resolve(checkout)
			if result.Status != Verified || result.Anchor != anchor {
				t.Fatalf("%+v; want anchor %s", result, anchor)
			}
			second := filepath.Join(filepath.Dir(checkout), "main")
			git(t, anchor, "worktree", "add", "-b", "second", second)
			if r := Resolve(second); r.Anchor != anchor {
				t.Fatalf("second: %+v", r)
			}
			alias := filepath.Join(filepath.Dir(checkout), "alias")
			if err := os.Symlink(checkout, alias); err != nil {
				t.Fatal(err)
			}
			if r := Resolve(alias); r.Anchor != anchor {
				t.Fatalf("alias: %+v", r)
			}
			// Exercise all-relative pointers, including the back-pointer.
			rel, _ := filepath.Rel(checkout, result.GitDir)
			put(t, filepath.Join(checkout, ".git"), "gitdir: "+rel+"\n")
			rel, _ = filepath.Rel(result.GitDir, filepath.Join(checkout, ".git"))
			put(t, filepath.Join(result.GitDir, "gitdir"), rel+"\n")
			if r := Resolve(checkout); r.Status != Verified {
				t.Fatalf("relative: %+v", r)
			}
			// Resolver must not consult poisoned Git environment or execute hooks.
			t.Setenv("GIT_DIR", "/missing")
			t.Setenv("GIT_COMMON_DIR", "/missing")
			t.Setenv("PATH", "")
			if r := Resolve(checkout); r.Anchor != anchor {
				t.Fatalf("environment: %+v", r)
			}
		})
	}
}

// Sessions can write HEAD and refs in the common directory, so those paths
// must not be able to drop the anchor.
func TestResolveAnchorIgnoresWritableRepositoryPaths(t *testing.T) {
	for _, bare := range []bool{false, true} {
		t.Run(map[bool]string{true: "bare", false: "ordinary"}[bare], func(t *testing.T) {
			anchor, checkout := fixture(t, bare)
			common := Resolve(checkout).CommonDir
			head := filepath.Join(common, "HEAD")
			if err := os.Remove(head); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("refs/heads/master", head); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(common, "refs"), filepath.Join(common, "refs.moved")); err != nil {
				t.Fatal(err)
			}
			if r := Resolve(checkout); r.Status != Verified || r.Anchor != anchor {
				t.Fatalf("writable paths changed the anchor: %+v", r)
			}
		})
	}
}

func TestResolveRejectsAdversarialMetadata(t *testing.T) {
	cases := []string{"forged", "missing-back", "stale-back", "empty-back", "symlink-back", "symlink-common", "symlink-dotgit", "escape-common", "nested-entry", "canonical-escape"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			anchor, checkout := fixture(t, false)
			valid := Resolve(checkout)
			switch name {
			case "forged":
				attacker := filepath.Join(filepath.Dir(checkout), "attacker")
				put(t, filepath.Join(attacker, ".git"), "gitdir: "+valid.GitDir)
				checkout = attacker
			case "missing-back":
				if err := os.Remove(filepath.Join(valid.GitDir, "gitdir")); err != nil {
					t.Fatal(err)
				}
			case "stale-back":
				put(t, filepath.Join(valid.GitDir, "gitdir"), "/missing/.git")
			case "empty-back":
				put(t, filepath.Join(valid.GitDir, "gitdir"), "\n")
			case "symlink-back", "symlink-common", "symlink-dotgit":
				path := filepath.Join(valid.GitDir, "gitdir")
				if name == "symlink-common" {
					path = filepath.Join(valid.GitDir, "commondir")
				}
				if name == "symlink-dotgit" {
					path = filepath.Join(checkout, ".git")
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				put(t, path+"-target", string(data))
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-target", path); err != nil {
					t.Fatal(err)
				}
			case "escape-common":
				put(t, filepath.Join(valid.GitDir, "commondir"), filepath.Dir(anchor))
			case "nested-entry":
				nested := filepath.Join(valid.GitDir, "nested")
				put(t, filepath.Join(nested, "commondir"), valid.CommonDir)
				put(t, filepath.Join(nested, "gitdir"), filepath.Join(checkout, ".git"))
				put(t, filepath.Join(checkout, ".git"), "gitdir: "+nested)
			case "canonical-escape":
				external := filepath.Join(filepath.Dir(checkout), "external")
				put(t, filepath.Join(external, "commondir"), valid.CommonDir)
				put(t, filepath.Join(external, "gitdir"), filepath.Join(checkout, ".git"))
				link := filepath.Join(valid.CommonDir, "worktrees", "escape")
				if err := os.Symlink(external, link); err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(checkout, ".git"), "gitdir: "+link)
			}
			if r := Resolve(checkout); r.Status != Invalid || r.Anchor != "" {
				t.Fatalf("accepted: %+v", r)
			}
		})
	}
}

func TestResolveRepositoryConfigCompatibility(t *testing.T) {
	for _, bare := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "bare"}[bare], func(t *testing.T) {
			anchor, checkout := fixture(t, bare)
			valid := Resolve(checkout)
			bareValue := "false"
			if bare {
				bareValue = "true"
			}
			core := "[core]\n bare = " + bareValue + "\n"
			large := core
			for i := range 100 {
				large += fmt.Sprintf("[branch \"feature-%d\"]\n remote = origin\n merge = refs/heads/feature-%d\n", i, i)
			}
			if len(large) <= maxMetadataBytes {
				t.Fatal("large config does not exceed pointer limit")
			}
			cases := []struct {
				name, contents string
				want           Status
			}{
				{"large config", large, Verified},
				{"inline comments", "[core] ; section comment\n bare = " + bareValue + " ; value comment\n", Verified},
				{"quoted comment characters", core + "[remote \"hash#semi;colon\"] # comment\n url = \"https://example.com/repo#fragment\" ; comment\n", Verified},
				{"include", core + "[include]\n path = /missing\n", VerifiedUnanchored},
				{"conditional include", core + "[includeIf \"gitdir:/missing/\"] # comment\n path = /missing\n", VerifiedUnanchored},
				{"ambiguous bare", core + " bare = true\n", VerifiedUnanchored},
				{"unrecognized config", "[core\n bare = " + bareValue + "\n", VerifiedUnanchored},
				{"oversized config", strings.Repeat("#", maxRepositoryConfigBytes+1), VerifiedUnanchored},
			}
			if bare {
				cases = append(cases, struct {
					name, contents string
					want           Status
				}{"custom bare layout", core + " worktree = ../main\n", VerifiedUnanchored})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					put(t, filepath.Join(valid.CommonDir, "config"), tc.contents)
					r := Resolve(checkout)
					if r.Status != tc.want || !r.Relationship() {
						t.Fatalf("unexpected resolution: %+v", r)
					}
					if tc.want == Verified && r.Anchor != anchor {
						t.Fatalf("wrong anchor: %+v", r)
					}
					if tc.want == VerifiedUnanchored && (r.Anchor != "" || r.Reason == "") {
						t.Fatalf("unanchored relationship has wrong context: %+v", r)
					}
				})
			}
		})
	}
}

func TestResolveSeparateGitDirWorktrees(t *testing.T) {
	for _, customWorktree := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate metadata", true: "custom core.worktree"}[customWorktree], func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			main, metadata, linked := filepath.Join(root, "main"), filepath.Join(root, "metadata"), filepath.Join(root, "linked")
			git(t, root, "init", "--separate-git-dir", metadata, main)
			git(t, main, "commit", "--allow-empty", "-m", "initial")
			git(t, main, "worktree", "add", "-b", "linked", linked)
			if customWorktree {
				git(t, main, "config", "core.worktree", main)
			}
			result := Resolve(linked)
			if result.Status != VerifiedUnanchored || !result.Relationship() || result.Anchor != "" || result.CommonDir != metadata {
				t.Fatalf("unsupported anchor rejected a valid relationship: %+v", result)
			}
			if r := Resolve(main); r.Status != NotLinked {
				t.Fatalf("original checkout changed behavior: %+v", r)
			}
			attacker := filepath.Join(root, "attacker")
			put(t, filepath.Join(attacker, ".git"), "gitdir: "+result.GitDir)
			if r := Resolve(attacker); r.Status != Invalid || r.Relationship() {
				t.Fatalf("forged separate metadata accepted: %+v", r)
			}
		})
	}
}

func TestResolveNonWorktrees(t *testing.T) {
	root := t.TempDir()
	if r := Resolve(root); r.Status != NotLinked {
		t.Fatal(r)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, ".git", "commondir"), "/etc")
	if r := Resolve(root); r.Status != NotLinked {
		t.Fatal(r)
	}
	sub := filepath.Join(root, "sub")
	metadata := filepath.Join(root, ".git", "modules", "sub")
	if err := os.MkdirAll(metadata, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(sub, ".git"), "gitdir: "+metadata)
	if r := Resolve(sub); r.Status != NotLinked {
		t.Fatal(r)
	}
}
