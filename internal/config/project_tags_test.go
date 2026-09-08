// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestProjectTagsShareOneNamespaceForExactMembers(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	featureDir := filepath.Join(root, "feature")
	childDir := filepath.Join(featureDir, "child")
	for _, dir := range []string{mainDir, featureDir, childDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	mainTag, changed, err := AssignProjectTag(home, mainDir, "enclave")
	if err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if !changed {
		t.Fatal("expected first assignment to change registry")
	}
	registryInfo, err := os.Stat(HostProjectTagsPath(home))
	if err != nil {
		t.Fatalf("stat registry: %v", err)
	}
	if registryInfo.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode = %o, want 600", registryInfo.Mode().Perm())
	}
	mainCanonical := canonicalPathForTest(t, mainDir)
	if mainTag.Namespace != ProjectHashForPath(mainCanonical) {
		t.Fatalf("tag namespace = %q, want main fallback", mainTag.Namespace)
	}

	if _, changed, err := AssignProjectTag(home, featureDir, "enclave"); err != nil {
		t.Fatalf("tag feature: %v", err)
	} else if !changed {
		t.Fatal("expected feature assignment to change registry")
	}

	mainDescription, err := DescribeProjectFromDir(home, mainDir)
	if err != nil {
		t.Fatalf("describe main: %v", err)
	}
	featureDescription, err := DescribeProjectFromDir(home, featureDir)
	if err != nil {
		t.Fatalf("describe feature: %v", err)
	}
	if mainDescription.Project.Hash != featureDescription.Project.Hash {
		t.Fatalf("tagged members use different namespaces: %q and %q", mainDescription.Project.Hash, featureDescription.Project.Hash)
	}
	if featureDescription.Tag == nil || featureDescription.Tag.Name != "enclave" {
		t.Fatalf("feature tag = %+v, want enclave", featureDescription.Tag)
	}

	t.Setenv("HOME", home)
	resolvedFeature, err := ResolveProjectFromDir(featureDir)
	if err != nil {
		t.Fatalf("resolve tagged feature through public resolver: %v", err)
	}
	if resolvedFeature.Hash != mainDescription.Project.Hash {
		t.Fatalf("public resolver namespace = %q, want %q", resolvedFeature.Hash, mainDescription.Project.Hash)
	}

	childDescription, err := DescribeProjectFromDir(home, childDir)
	if err != nil {
		t.Fatalf("describe child: %v", err)
	}
	if childDescription.Tag != nil {
		t.Fatalf("exact tag unexpectedly inherited by child: %+v", childDescription.Tag)
	}
	if childDescription.Project.Hash != ProjectHashForPath(childDescription.Project.RealDir) {
		t.Fatalf("child did not use its path-derived namespace: %q", childDescription.Project.Hash)
	}
}

func TestTaggedMemberLoadsSharedProjectDefaults(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	featureDir := filepath.Join(root, "feature")
	for _, dir := range []string{mainDir, featureDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	tag, _, err := AssignProjectTag(home, mainDir, "sample")
	if err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}
	configPath := HostProjectConfigJSONPath(home, tag.Namespace)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("{\"slim\":true}\n"), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	resolvedFeature, err := ResolveProjectFromDir(featureDir)
	if err != nil {
		t.Fatalf("resolve feature project: %v", err)
	}
	_, projectDefaults, _, err := LoadDefaultsForProject(resolvedFeature)
	if err != nil {
		t.Fatalf("load feature defaults: %v", err)
	}
	if projectDefaults.Slim == nil || !*projectDefaults.Slim {
		t.Fatalf("tagged feature did not load shared project defaults: %+v", projectDefaults.Slim)
	}
}

func TestProjectTagsWorkWithBareRepositoryWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	bareDir := filepath.Join(root, "project.git")
	seedDir := filepath.Join(root, "seed")
	mainDir := filepath.Join(root, "worktrees", "main")
	featureDir := filepath.Join(root, "worktrees", "feature")

	runGitInDir(t, root, "init", "--bare", bareDir)
	runGitInDir(t, root, "clone", bareDir, seedDir)
	runGitInDir(t, seedDir, "config", "user.email", "test@example.com")
	runGitInDir(t, seedDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write seed readme: %v", err)
	}
	runGitInDir(t, seedDir, "add", "README.md")
	runGitInDir(t, seedDir, "commit", "-m", "init")
	runGitInDir(t, seedDir, "push", "origin", "HEAD")
	runGitInDir(t, bareDir, "worktree", "add", mainDir, "HEAD")
	runGitInDir(t, bareDir, "worktree", "add", "-b", "feature", featureDir, "HEAD")

	mainTag, _, err := AssignProjectTag(home, mainDir, "bare-project")
	if err != nil {
		t.Fatalf("tag main worktree: %v", err)
	}
	if _, _, err := AssignProjectTag(home, featureDir, "bare-project"); err != nil {
		t.Fatalf("tag feature worktree: %v", err)
	}
	feature, err := DescribeProjectFromDir(home, featureDir)
	if err != nil {
		t.Fatalf("describe feature worktree: %v", err)
	}
	if feature.Project.Hash != mainTag.Namespace {
		t.Fatalf("feature namespace = %q, want %q", feature.Project.Hash, mainTag.Namespace)
	}
}

func TestAssignProjectTagRejectsUnexpectedNamespace(t *testing.T) {
	unsetXDGEnv(t)

	t.Run("existing tag changed", func(t *testing.T) {
		home := t.TempDir()
		mainDir := t.TempDir()
		featureDir := t.TempDir()
		tag, _, err := AssignProjectTag(home, mainDir, "sample")
		if err != nil {
			t.Fatalf("tag main: %v", err)
		}
		unexpectedNamespace := differentTestNamespace(tag.Namespace)
		if _, _, err := AssignProjectTagToNamespace(home, featureDir, "sample", unexpectedNamespace); err == nil {
			t.Fatal("expected changed destination namespace to fail")
		}
		description, err := DescribeProjectFromDir(home, featureDir)
		if err != nil {
			t.Fatalf("describe feature: %v", err)
		}
		if description.Tag != nil {
			t.Fatalf("feature was assigned after namespace mismatch: %+v", description.Tag)
		}
	})

	t.Run("new tag source changed", func(t *testing.T) {
		home := t.TempDir()
		projectDir := t.TempDir()
		unexpectedNamespace := differentTestNamespace(ProjectHashForPath(projectDir))
		if _, _, err := AssignProjectTagToNamespace(home, projectDir, "sample", unexpectedNamespace); err == nil {
			t.Fatal("expected changed source namespace to fail")
		}
		registry, err := LoadProjectTags(home)
		if err != nil {
			t.Fatalf("load registry: %v", err)
		}
		if len(registry.Tags) != 0 {
			t.Fatalf("namespace mismatch created a tag: %+v", registry.Tags)
		}
	})
}

func TestAssignProjectTagRejectsChangedMemberSnapshot(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	mainDir := t.TempDir()
	concurrentDir := t.TempDir()
	projectDir := t.TempDir()

	if _, _, err := AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	registry, err := LoadProjectTags(home)
	if err != nil {
		t.Fatalf("load registry snapshot: %v", err)
	}
	snapshot := ProjectTagByName(registry, "sample")
	if snapshot == nil {
		t.Fatal("tag missing from registry snapshot")
	}
	if _, _, err := AssignProjectTag(home, concurrentDir, "sample"); err != nil {
		t.Fatalf("tag concurrent member: %v", err)
	}

	if _, _, err := AssignProjectTagToSnapshot(home, projectDir, "sample", *snapshot); err == nil {
		t.Fatal("expected changed member snapshot to fail")
	}
	description, err := DescribeProjectFromDir(home, projectDir)
	if err != nil {
		t.Fatalf("describe project: %v", err)
	}
	if description.Tag != nil {
		t.Fatalf("project was assigned after member snapshot changed: %+v", description.Tag)
	}
}

func differentTestNamespace(namespace string) string {
	if namespace != "aaaaaaaaaaaa" {
		return "aaaaaaaaaaaa"
	}
	return "bbbbbbbbbbbb"
}

func TestProjectTagRenameStopsMatchingAndRecreationMatches(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	original := filepath.Join(root, "project")
	renamed := filepath.Join(root, "renamed")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if _, _, err := AssignProjectTag(home, original, "sample"); err != nil {
		t.Fatalf("tag project: %v", err)
	}
	if err := os.Rename(original, renamed); err != nil {
		t.Fatalf("rename project: %v", err)
	}

	description, err := DescribeProjectFromDir(home, renamed)
	if err != nil {
		t.Fatalf("describe renamed project: %v", err)
	}
	if description.Tag != nil {
		t.Fatalf("renamed path retained location tag: %+v", description.Tag)
	}

	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatalf("recreate original: %v", err)
	}
	description, err = DescribeProjectFromDir(home, original)
	if err != nil {
		t.Fatalf("describe recreated project: %v", err)
	}
	if description.Tag == nil || description.Tag.Name != "sample" {
		t.Fatalf("recreated path did not inherit location tag: %+v", description.Tag)
	}
}

func TestRemoveMissingProjectTagDoesNotCreateRegistry(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	projectDir := t.TempDir()
	if _, _, changed, err := RemoveProjectTag(home, projectDir, "", "", ""); err != nil {
		t.Fatalf("untag untagged project: %v", err)
	} else if changed {
		t.Fatal("untagging an untagged project reported a change")
	}
	if _, err := os.Stat(HostProjectTagsPath(home)); !os.IsNotExist(err) {
		t.Fatalf("no-op untag created a registry: %v", err)
	}
}

func TestRemoveProjectTagRestoresFallback(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	featureDir := filepath.Join(root, "feature")
	for _, dir := range []string{mainDir, featureDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if _, _, err := AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}

	if _, _, changed, err := RemoveProjectTag(home, featureDir, "", "", ""); err != nil {
		t.Fatalf("untag feature: %v", err)
	} else if !changed {
		t.Fatal("expected untag to change registry")
	}
	description, err := DescribeProjectFromDir(home, featureDir)
	if err != nil {
		t.Fatalf("describe feature: %v", err)
	}
	if description.Tag != nil {
		t.Fatalf("feature remains tagged: %+v", description.Tag)
	}
	if description.Project.Hash != description.FallbackNamespace {
		t.Fatalf("effective namespace %q, want fallback %q", description.Project.Hash, description.FallbackNamespace)
	}
}

func TestRemoveProjectTagAcceptsMissingMemberPath(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	featureDir := filepath.Join(root, "feature")
	for _, dir := range []string{mainDir, featureDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if _, _, err := AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}
	if err := os.Remove(featureDir); err != nil {
		t.Fatalf("remove feature: %v", err)
	}

	if _, member, changed, err := RemoveProjectTag(home, mainDir, featureDir, "", ""); err != nil {
		t.Fatalf("remove missing member: %v", err)
	} else if !changed || member != featureDir {
		t.Fatalf("missing member removal = (%q, %v), want (%q, true)", member, changed, featureDir)
	}
}

func TestRemoveProjectTagAcceptsStaleMemberReplacedBySymlink(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	featureDir := filepath.Join(root, "feature")
	targetDir := filepath.Join(root, "replacement")
	for _, dir := range []string{mainDir, featureDir, targetDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if _, _, err := AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}
	if err := os.Remove(featureDir); err != nil {
		t.Fatalf("remove feature: %v", err)
	}
	if err := os.Symlink(targetDir, featureDir); err != nil {
		t.Skipf("create replacement symlink: %v", err)
	}

	if _, member, changed, err := RemoveProjectTag(home, mainDir, featureDir, "", ""); err != nil {
		t.Fatalf("remove stale symlink member: %v", err)
	} else if !changed || member != featureDir {
		t.Fatalf("stale symlink member removal = (%q, %v), want (%q, true)", member, changed, featureDir)
	}
}

func TestRemoveProjectTagRefusesNamespaceOriginWhileShared(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	featureDir := filepath.Join(root, "feature")
	for _, dir := range []string{mainDir, featureDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if _, _, err := AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}

	if _, _, _, err := RemoveProjectTag(home, mainDir, "", "", ""); err == nil {
		t.Fatal("expected namespace-origin removal to fail while another member remains")
	}
}

func TestRemoveProjectTagRejectsExpectedTagMismatch(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	projectDir := t.TempDir()
	if _, _, err := AssignProjectTag(home, projectDir, "actual"); err != nil {
		t.Fatalf("tag project: %v", err)
	}

	if _, _, _, err := RemoveProjectTag(home, projectDir, "", "confirmed", ""); err == nil {
		t.Fatal("expected removal to fail when the member moved to another tag")
	}
	description, err := DescribeProjectFromDir(home, projectDir)
	if err != nil {
		t.Fatalf("describe project: %v", err)
	}
	if description.Tag == nil || description.Tag.Name != "actual" {
		t.Fatalf("member was removed despite tag mismatch: %+v", description.Tag)
	}

	if _, _, changed, err := RemoveProjectTag(home, projectDir, "", "actual", ""); err != nil || !changed {
		t.Fatalf("removal with matching expected tag = (%v, %v), want (true, nil)", changed, err)
	}
}

func TestRemoveProjectTagUsesExactConfirmedMember(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	mainDir := t.TempDir()
	confirmedDir := t.TempDir()
	retargetedDir := t.TempDir()
	for _, dir := range []string{mainDir, confirmedDir, retargetedDir} {
		if _, _, err := AssignProjectTag(home, dir, "sample"); err != nil {
			t.Fatalf("tag %s: %v", dir, err)
		}
	}

	tag, member, changed, err := RemoveProjectTag(home, mainDir, retargetedDir, "sample", confirmedDir)
	if err != nil {
		t.Fatalf("remove confirmed member: %v", err)
	}
	if !changed || tag.Name != "sample" || member != confirmedDir {
		t.Fatalf("confirmed removal = (%+v, %q, %v), want sample, %q, true", tag, member, changed, confirmedDir)
	}
	confirmed, _, err := FindProjectTagMember(home, mainDir, confirmedDir)
	if err != nil {
		t.Fatalf("find confirmed member: %v", err)
	}
	if confirmed != nil {
		t.Fatalf("confirmed member remains tagged: %+v", confirmed)
	}
	retargeted, _, err := FindProjectTagMember(home, mainDir, retargetedDir)
	if err != nil {
		t.Fatalf("find retargeted member: %v", err)
	}
	if retargeted == nil || retargeted.Name != "sample" {
		t.Fatalf("mutable path argument removed the wrong member: %+v", retargeted)
	}
}

func TestFindProjectTagMember(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	otherDir := filepath.Join(root, "other")
	for _, dir := range []string{mainDir, otherDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if _, _, err := AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}

	tag, stored, err := FindProjectTagMember(home, otherDir, mainDir)
	if err != nil {
		t.Fatalf("find member: %v", err)
	}
	if tag == nil || tag.Name != "sample" || stored != mainDir {
		t.Fatalf("member lookup = (%+v, %q), want tag sample with %q", tag, stored, mainDir)
	}

	tag, _, err = FindProjectTagMember(home, otherDir, "")
	if err != nil {
		t.Fatalf("find current dir: %v", err)
	}
	if tag != nil {
		t.Fatalf("untagged directory matched tag %+v", tag)
	}
}

func TestProjectTagNamespaceOrigin(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	featureDir := filepath.Join(root, "feature")
	for _, dir := range []string{mainDir, featureDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if _, _, err := AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	tag, _, err := AssignProjectTag(home, featureDir, "sample")
	if err != nil {
		t.Fatalf("tag feature: %v", err)
	}

	if origin := ProjectTagNamespaceOrigin(tag); origin != mainDir {
		t.Fatalf("namespace origin = %q, want %q", origin, mainDir)
	}
	if origin := ProjectTagNamespaceOrigin(ProjectTag{Namespace: "abc123abc123", Members: []string{mainDir}}); origin != "" {
		t.Fatalf("anchorless tag reported origin %q", origin)
	}
}

func TestProjectTagRegistryRejectsUnsafeFiles(t *testing.T) {
	unsetXDGEnv(t)

	t.Run("malformed", func(t *testing.T) {
		home := t.TempDir()
		path := HostProjectTagsPath(home)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir registry dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatalf("write registry: %v", err)
		}
		if _, err := LoadProjectTags(home); err == nil {
			t.Fatal("expected malformed registry to fail")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		home := t.TempDir()
		path := HostProjectTagsPath(home)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir registry dir: %v", err)
		}
		target := filepath.Join(home, "target.json")
		if err := os.WriteFile(target, []byte("{\"version\":1,\"tags\":[]}"), 0o600); err != nil {
			t.Fatalf("write target: %v", err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("symlink registry: %v", err)
		}
		if _, err := LoadProjectTags(home); err == nil {
			t.Fatal("expected symlinked registry to fail")
		}
	})

	t.Run("writable by group", func(t *testing.T) {
		home := t.TempDir()
		path := HostProjectTagsPath(home)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir registry dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("{\"version\":1,\"tags\":[]}"), 0o600); err != nil {
			t.Fatalf("write registry: %v", err)
		}
		if err := os.Chmod(path, 0o620); err != nil {
			t.Fatalf("chmod registry: %v", err)
		}
		if _, err := LoadProjectTags(home); err == nil {
			t.Fatal("expected group-writable registry to fail")
		}
	})
}

func TestProjectTagRegistryRejectsInvalidContents(t *testing.T) {
	unsetXDGEnv(t)
	tests := []struct {
		name string
		data string
	}{
		{name: "unknown version", data: `{"version":2,"tags":[]}`},
		{name: "unknown field", data: `{"version":1,"tags":[],"extra":true}`},
		{name: "invalid namespace", data: `{"version":1,"tags":[{"name":"sample","namespace":"not-a-hash","members":["/tmp/project"]}]}`},
		{name: "anchorless namespace", data: fmt.Sprintf(`{"version":1,"tags":[{"name":"sample","namespace":%q,"members":["/tmp/project"]}]}`, differentTestNamespace(ProjectHashForPath("/tmp/project")))},
		{name: "duplicate member", data: `{"version":1,"tags":[{"name":"one","namespace":"111111111111","members":["/tmp/project"]},{"name":"two","namespace":"222222222222","members":["/tmp/project"]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			path := HostProjectTagsPath(home)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("mkdir registry dir: %v", err)
			}
			if err := os.WriteFile(path, []byte(tt.data), 0o600); err != nil {
				t.Fatalf("write registry: %v", err)
			}
			if _, err := LoadProjectTags(home); err == nil {
				t.Fatalf("expected registry to reject %s", tt.name)
			}
		})
	}
}

func TestProjectTagRegistryRejectsOversizedFile(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	path := HostProjectTagsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir registry dir: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{' '}, projectTagsMaxBytes+1), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	if _, err := LoadProjectTags(home); err == nil {
		t.Fatal("expected oversized registry to fail")
	}
}

func TestConcurrentProjectTagReadsCoexistWithAtomicWriters(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	mainDir := t.TempDir()
	featureDir := t.TempDir()
	mainTag, _, err := AssignProjectTag(home, mainDir, "shared")
	if err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := AssignProjectTag(home, featureDir, "shared"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}

	const (
		mutationCount = 300
		readerCount   = 4
		readCount     = 5000
	)
	start := make(chan struct{})
	errs := make(chan error, readerCount+1)
	report := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for range mutationCount {
			if _, _, changed, err := RemoveProjectTag(home, featureDir, "", "", ""); err != nil {
				report(fmt.Errorf("remove feature member: %w", err))
				return
			} else if !changed {
				report(fmt.Errorf("remove feature member reported no change"))
				return
			}
			if _, changed, err := AssignProjectTag(home, featureDir, "shared"); err != nil {
				report(fmt.Errorf("restore feature member: %w", err))
				return
			} else if !changed {
				report(fmt.Errorf("restore feature member reported no change"))
				return
			}
		}
	}()
	for range readerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range readCount {
				registry, err := LoadProjectTags(home)
				if err != nil {
					report(fmt.Errorf("load registry: %w", err))
					return
				}
				tag := ProjectTagByName(registry, "shared")
				if tag == nil || tag.Namespace != mainTag.Namespace || len(tag.Members) < 1 || len(tag.Members) > 2 {
					report(fmt.Errorf("unexpected registry snapshot: %+v", registry))
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentProjectTagAssignmentsKeepEveryMember(t *testing.T) {
	unsetXDGEnv(t)
	home := t.TempDir()
	root := t.TempDir()
	const count = 12
	dirs := make([]string, count)
	for i := range dirs {
		dirs[i] = filepath.Join(root, fmt.Sprintf("project-%02d", i))
		if err := os.Mkdir(dirs[i], 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dirs[i], err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, count)
	for _, dir := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := AssignProjectTag(home, dir, "shared")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent assignment: %v", err)
		}
	}

	registry, err := LoadProjectTags(home)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	tag := ProjectTagByName(registry, "shared")
	if tag == nil || len(tag.Members) != count {
		t.Fatalf("tag after concurrent assignments = %+v, want %d members", tag, count)
	}
}
