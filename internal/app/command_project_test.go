// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/config"
)

func makeProjectDirs(t *testing.T, names ...string) []string {
	t.Helper()
	root := t.TempDir()
	dirs := make([]string, 0, len(names))
	for _, name := range names {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		dirs = append(dirs, dir)
	}
	return dirs
}

type callbackReader struct {
	beforeRead func()
	reader     *strings.Reader
}

func (r *callbackReader) Read(buffer []byte) (int, error) {
	if r.beforeRead != nil {
		callback := r.beforeRead
		r.beforeRead = nil
		callback()
	}
	return r.reader.Read(buffer)
}

func TestRunProjectTagSetPreservesFirstNamespaceAndJoinsSecond(t *testing.T) {
	home := t.TempDir()
	dirs := makeProjectDirs(t, "main", "feature")
	mainDir, featureDir := dirs[0], dirs[1]

	var stdout bytes.Buffer
	if code := runProjectTagSet(home, mainDir, "sample", projectTagSetOptions{}, strings.NewReader("y\n"), &stdout); code != 0 {
		t.Fatalf("tag main returned %d: %s", code, stdout.String())
	}
	for _, want := range []string{"does not exist yet", "namespace origin"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("create flow missing %q: %s", want, stdout.String())
		}
	}
	mainDescription, err := config.DescribeProjectFromDir(home, mainDir)
	if err != nil {
		t.Fatalf("describe main: %v", err)
	}
	if mainDescription.Project.Hash != mainDescription.FallbackNamespace {
		t.Fatalf("first member namespace = %q, want fallback %q", mainDescription.Project.Hash, mainDescription.FallbackNamespace)
	}
	if strings.Contains(stdout.String(), "sessions already running") {
		t.Fatalf("namespace-preserving first tag emitted a transition warning: %s", stdout.String())
	}

	stdout.Reset()
	if code := runProjectTagSet(home, featureDir, "sample", projectTagSetOptions{}, strings.NewReader("y\n"), &stdout); code != 0 {
		t.Fatalf("tag feature returned %d: %s", code, stdout.String())
	}
	featureDescription, err := config.DescribeProjectFromDir(home, featureDir)
	if err != nil {
		t.Fatalf("describe feature: %v", err)
	}
	if featureDescription.Project.Hash != mainDescription.Project.Hash {
		t.Fatalf("feature namespace = %q, want %q", featureDescription.Project.Hash, mainDescription.Project.Hash)
	}
	for _, want := range []string{
		"sessions already running",
		featureDescription.FallbackNamespace,
		mainDescription.Project.Hash,
		"must be restarted",
		"network apply --all-running",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("tag transition warning missing %q: %s", want, stdout.String())
		}
	}
}

func TestRunProjectTagSetConfirmsCreation(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()

	var stdout bytes.Buffer
	if code := runProjectTagSet(home, projectDir, "sample", projectTagSetOptions{}, strings.NewReader("n\n"), &stdout); code == 0 {
		t.Fatalf("expected declined creation to fail: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "does not exist yet") {
		t.Fatalf("create prompt did not state the tag is new: %s", stdout.String())
	}
	description, err := config.DescribeProjectFromDir(home, projectDir)
	if err != nil {
		t.Fatalf("describe project: %v", err)
	}
	if description.Tag != nil {
		t.Fatalf("declined creation still tagged the project: %+v", description.Tag)
	}

	stdout.Reset()
	if code := runProjectTagSet(home, projectDir, "sample", projectTagSetOptions{Yes: true}, strings.NewReader(""), &stdout); code != 0 {
		t.Fatalf("creation with --yes returned %d: %s", code, stdout.String())
	}
}

// Assigning an existing tag is a trust-domain join and must be confirmed even
// when the joining directory has no dormant data.
func TestRunProjectTagSetConfirmsJoinWithoutDormantData(t *testing.T) {
	home := t.TempDir()
	dirs := makeProjectDirs(t, "main", "feature")
	mainDir, featureDir := dirs[0], dirs[1]
	if _, _, err := config.AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}

	var stdout bytes.Buffer
	if code := runProjectTagSet(home, featureDir, "sample", projectTagSetOptions{}, strings.NewReader("n\n"), &stdout); code == 0 {
		t.Fatalf("expected declined join to fail: %s", stdout.String())
	}
	for _, want := range []string{"share its complete Enclave project scope", mainDir, "namespace origin", "Assign existing project tag"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("join prompt missing %q: %s", want, stdout.String())
		}
	}
	description, err := config.DescribeProjectFromDir(home, featureDir)
	if err != nil {
		t.Fatalf("describe feature: %v", err)
	}
	if description.Tag != nil {
		t.Fatalf("declined join still tagged the project: %+v", description.Tag)
	}

	stdout.Reset()
	if code := runProjectTagSet(home, featureDir, "sample", projectTagSetOptions{}, strings.NewReader("y\n"), &stdout); code != 0 {
		t.Fatalf("confirmed join returned %d: %s", code, stdout.String())
	}
}

func TestRunProjectTagSetRejectsMembershipChangeAfterPrompt(t *testing.T) {
	home := t.TempDir()
	dirs := makeProjectDirs(t, "main", "concurrent", "feature")
	mainDir, concurrentDir, featureDir := dirs[0], dirs[1], dirs[2]
	if _, _, err := config.AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}

	var mutationErr error
	stdin := &callbackReader{
		beforeRead: func() {
			_, _, mutationErr = config.AssignProjectTag(home, concurrentDir, "sample")
		},
		reader: strings.NewReader("y\n"),
	}
	var stdout bytes.Buffer
	if code := runProjectTagSet(home, featureDir, "sample", projectTagSetOptions{}, stdin, &stdout); code == 0 {
		t.Fatalf("assignment succeeded after confirmed membership changed: %s", stdout.String())
	}
	if mutationErr != nil {
		t.Fatalf("add concurrent member: %v", mutationErr)
	}
	description, err := config.DescribeProjectFromDir(home, featureDir)
	if err != nil {
		t.Fatalf("describe feature: %v", err)
	}
	if description.Tag != nil {
		t.Fatalf("feature was assigned after confirmed membership changed: %+v", description.Tag)
	}
}

func TestRunProjectTagSetIntentFlags(t *testing.T) {
	home := t.TempDir()
	dirs := makeProjectDirs(t, "main", "feature")
	mainDir, featureDir := dirs[0], dirs[1]

	var stdout bytes.Buffer
	if code := runProjectTagSet(home, mainDir, "sample", projectTagSetOptions{Existing: true, Yes: true}, strings.NewReader(""), &stdout); code == 0 {
		t.Fatalf("--existing created a new tag: %s", stdout.String())
	}
	registry, err := config.LoadProjectTags(home)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if config.ProjectTagByName(registry, "sample") != nil {
		t.Fatal("--existing failure still created the tag")
	}

	if _, _, err := config.AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	stdout.Reset()
	if code := runProjectTagSet(home, featureDir, "sample", projectTagSetOptions{New: true, Yes: true}, strings.NewReader(""), &stdout); code == 0 {
		t.Fatalf("--new assigned an existing tag: %s", stdout.String())
	}
	description, err := config.DescribeProjectFromDir(home, featureDir)
	if err != nil {
		t.Fatalf("describe feature: %v", err)
	}
	if description.Tag != nil {
		t.Fatalf("--new failure still assigned the tag: %+v", description.Tag)
	}
}

func TestRunProjectTagSetListsDormantData(t *testing.T) {
	home := t.TempDir()
	dirs := makeProjectDirs(t, "main", "feature")
	mainDir, featureDir := dirs[0], dirs[1]
	if _, _, err := config.AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	featureHash := config.ProjectHashForPath(featureDir)
	dataPath := config.HostProjectConfigJSONPath(home, featureHash)
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o700); err != nil {
		t.Fatalf("mkdir feature config: %v", err)
	}
	if err := os.WriteFile(dataPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write feature config: %v", err)
	}

	var stdout bytes.Buffer
	if code := runProjectTagSet(home, featureDir, "sample", projectTagSetOptions{}, strings.NewReader("n\n"), &stdout); code == 0 {
		t.Fatalf("expected declined assignment to fail: %s", stdout.String())
	}
	description, err := config.DescribeProjectFromDir(home, featureDir)
	if err != nil {
		t.Fatalf("describe feature: %v", err)
	}
	if description.Tag != nil {
		t.Fatalf("declined project was tagged: %+v", description.Tag)
	}
	if !strings.Contains(stdout.String(), "become dormant") {
		t.Fatalf("confirmation did not explain dormant data: %s", stdout.String())
	}

	stdout.Reset()
	if code := runProjectTagSet(home, featureDir, "sample", projectTagSetOptions{Yes: true}, strings.NewReader(""), &stdout); code != 0 {
		t.Fatalf("confirmed assignment returned %d: %s", code, stdout.String())
	}
	if _, err := os.Stat(dataPath); err != nil {
		t.Fatalf("source data was moved or deleted: %v", err)
	}
}

func TestRunProjectTagUnsetWarnsOnlyWhenNamespaceChanges(t *testing.T) {
	home := t.TempDir()
	mainDir := t.TempDir()
	featureDir := t.TempDir()
	mainTag, _, err := config.AssignProjectTag(home, mainDir, "sample")
	if err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := config.AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}

	var stdout bytes.Buffer
	if code := runProjectTagUnset(home, featureDir, "", false, strings.NewReader(""), &stdout); code != 0 {
		t.Fatalf("unset feature returned %d: %s", code, stdout.String())
	}
	featureNamespace := config.ProjectHashForPath(featureDir)
	for _, want := range []string{mainTag.Namespace, featureNamespace, "sessions already running", "must be restarted"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("unset transition warning missing %q: %s", want, stdout.String())
		}
	}

	stdout.Reset()
	if code := runProjectTagUnset(home, mainDir, "", false, strings.NewReader(""), &stdout); code != 0 {
		t.Fatalf("unset final member returned %d: %s", code, stdout.String())
	}
	if strings.Contains(stdout.String(), "sessions already running") {
		t.Fatalf("namespace-preserving final unset emitted a transition warning: %s", stdout.String())
	}
}

// An explicit path may detach a member of a different tag than the current
// directory's, so it must be reported and confirmed.
func TestRunProjectTagUnsetConfirmsExplicitPath(t *testing.T) {
	home := t.TempDir()
	dirs := makeProjectDirs(t, "main", "feature", "elsewhere")
	mainDir, featureDir, elsewhere := dirs[0], dirs[1], dirs[2]
	if _, _, err := config.AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := config.AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}

	var stdout bytes.Buffer
	if code := runProjectTagUnset(home, elsewhere, featureDir, false, strings.NewReader("n\n"), &stdout); code == 0 {
		t.Fatalf("expected declined removal to fail: %s", stdout.String())
	}
	for _, want := range []string{featureDir, "sample"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("removal prompt missing %q: %s", want, stdout.String())
		}
	}
	if tag, _, err := config.FindProjectTagMember(home, elsewhere, featureDir); err != nil || tag == nil {
		t.Fatalf("declined removal detached the member: tag=%v err=%v", tag, err)
	}

	stdout.Reset()
	if code := runProjectTagUnset(home, elsewhere, featureDir, false, strings.NewReader("y\n"), &stdout); code != 0 {
		t.Fatalf("confirmed removal returned %d: %s", code, stdout.String())
	}
	if tag, _, err := config.FindProjectTagMember(home, elsewhere, featureDir); err != nil || tag != nil {
		t.Fatalf("confirmed removal left the member tagged: tag=%v err=%v", tag, err)
	}

	if _, _, err := config.AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("retag feature: %v", err)
	}
	stdout.Reset()
	if code := runProjectTagUnset(home, elsewhere, featureDir, true, strings.NewReader(""), &stdout); code != 0 {
		t.Fatalf("--yes removal returned %d: %s", code, stdout.String())
	}
}

func TestRunProjectTagUnsetRemovesConfirmedMemberAfterSymlinkRetarget(t *testing.T) {
	home := t.TempDir()
	dirs := makeProjectDirs(t, "main", "confirmed", "retargeted", "elsewhere")
	mainDir, confirmedDir, retargetedDir, elsewhere := dirs[0], dirs[1], dirs[2], dirs[3]
	for _, dir := range []string{mainDir, confirmedDir, retargetedDir} {
		if _, _, err := config.AssignProjectTag(home, dir, "sample"); err != nil {
			t.Fatalf("tag %s: %v", dir, err)
		}
	}
	link := filepath.Join(t.TempDir(), "member")
	if err := os.Symlink(confirmedDir, link); err != nil {
		t.Skipf("create member symlink: %v", err)
	}

	var mutationErr error
	stdin := &callbackReader{
		beforeRead: func() {
			if err := os.Remove(link); err != nil {
				mutationErr = err
				return
			}
			mutationErr = os.Symlink(retargetedDir, link)
		},
		reader: strings.NewReader("y\n"),
	}
	var stdout bytes.Buffer
	if code := runProjectTagUnset(home, elsewhere, link, false, stdin, &stdout); code != 0 {
		t.Fatalf("confirmed removal returned %d: %s", code, stdout.String())
	}
	if mutationErr != nil {
		t.Fatalf("retarget member symlink: %v", mutationErr)
	}
	confirmed, _, err := config.FindProjectTagMember(home, elsewhere, confirmedDir)
	if err != nil {
		t.Fatalf("find confirmed member: %v", err)
	}
	if confirmed != nil {
		t.Fatalf("confirmed member remains tagged: %+v", confirmed)
	}
	retargeted, _, err := config.FindProjectTagMember(home, elsewhere, retargetedDir)
	if err != nil {
		t.Fatalf("find retargeted member: %v", err)
	}
	if retargeted == nil || retargeted.Name != "sample" {
		t.Fatalf("retargeted symlink removed the wrong member: %+v", retargeted)
	}
}

func TestRunProjectTagListHumanAndJSON(t *testing.T) {
	home := t.TempDir()

	var stdout bytes.Buffer
	if code := runProjectTagList(home, false, &stdout); code != 0 {
		t.Fatalf("empty list returned %d: %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "No project tags.") {
		t.Fatalf("empty list output: %s", stdout.String())
	}

	dirs := makeProjectDirs(t, "main", "feature")
	mainDir, featureDir := dirs[0], dirs[1]
	tag, _, err := config.AssignProjectTag(home, mainDir, "sample")
	if err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := config.AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}
	if err := os.Remove(featureDir); err != nil {
		t.Fatalf("remove feature: %v", err)
	}

	stdout.Reset()
	if code := runProjectTagList(home, false, &stdout); code != 0 {
		t.Fatalf("list returned %d: %s", code, stdout.String())
	}
	for _, want := range []string{"sample (namespace " + tag.Namespace + ")", mainDir, "(namespace origin)", "(missing)"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("list output missing %q: %s", want, stdout.String())
		}
	}

	stdout.Reset()
	if code := runProjectTagList(home, true, &stdout); code != 0 {
		t.Fatalf("list --json returned %d: %s", code, stdout.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &fields); err != nil {
		t.Fatalf("decode list json fields: %v", err)
	}
	if _, ok := fields["tags"]; !ok {
		t.Fatalf("missing JSON contract field \"tags\" in %s", stdout.String())
	}
	var output projectTagListJSON
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode list json: %v", err)
	}
	if len(output.Tags) != 1 || output.Tags[0].Name != "sample" || len(output.Tags[0].Members) != 2 {
		t.Fatalf("unexpected list output: %+v", output)
	}
	for _, member := range output.Tags[0].Members {
		wantOrigin := member.Path == mainDir
		if member.NamespaceOrigin != wantOrigin {
			t.Fatalf("member %q namespaceOrigin = %v, want %v", member.Path, member.NamespaceOrigin, wantOrigin)
		}
		if member.Path == featureDir && member.Exists {
			t.Fatalf("removed member reported as existing: %+v", member)
		}
	}
}

func TestNamespaceDataPathsIgnoresGlobalCaches(t *testing.T) {
	home := t.TempDir()
	const namespace = "abc123abc123"
	microVMPath := filepath.Join(config.HostCacheDir(home), "microvm", "codex", namespace)
	if err := os.MkdirAll(microVMPath, 0o700); err != nil {
		t.Fatalf("mkdir microvm bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(microVMPath, "vmlinuz"), []byte("bundle"), 0o600); err != nil {
		t.Fatalf("write microvm bundle: %v", err)
	}
	buildPath := filepath.Join(config.HostBuildDir(home), namespace)
	if err := os.MkdirAll(buildPath, 0o700); err != nil {
		t.Fatalf("mkdir build cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buildPath, "stamp"), []byte("build"), 0o600); err != nil {
		t.Fatalf("write build cache: %v", err)
	}
	if paths := namespaceDataPaths(home, namespace); len(paths) != 0 {
		t.Fatalf("global cache reported as project data: %v", paths)
	}

	projectCache := config.HostCacheToolProjectDir(home, "codex", namespace)
	if err := os.MkdirAll(projectCache, 0o700); err != nil {
		t.Fatalf("mkdir project cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectCache, "cache.db"), []byte("cache"), 0o600); err != nil {
		t.Fatalf("write project cache: %v", err)
	}
	paths := namespaceDataPaths(home, namespace)
	if len(paths) != 1 || paths[0] != projectCache {
		t.Fatalf("project cache paths = %v, want [%s]", paths, projectCache)
	}
}

func TestRunProjectShowJSONContract(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()
	if _, _, err := config.AssignProjectTag(home, projectDir, "sample"); err != nil {
		t.Fatalf("tag project: %v", err)
	}

	var stdout bytes.Buffer
	if code := runProjectShow(home, projectDir, true, &stdout); code != 0 {
		t.Fatalf("project show returned %d", code)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &fields); err != nil {
		t.Fatalf("decode show json fields: %v", err)
	}
	for _, key := range []string{"projectDirectory", "canonicalDirectory", "fallbackNamespace", "effectiveNamespace", "resolution", "tag"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing JSON contract field %q in %s", key, stdout.String())
		}
	}

	var output projectShowJSON
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode show json: %v", err)
	}
	if output.Resolution != "tag" || output.Tag == nil || output.Tag.Name != "sample" {
		t.Fatalf("unexpected show output: %+v", output)
	}
	if output.FallbackNamespace == "" || output.EffectiveNamespace == "" || len(output.Tag.Members) != 1 {
		t.Fatalf("incomplete show output: %+v", output)
	}
	if !output.Tag.Members[0].NamespaceOrigin {
		t.Fatalf("sole member not marked as namespace origin: %+v", output.Tag.Members[0])
	}
}

func TestRunProjectShowLeadsWithTag(t *testing.T) {
	home := t.TempDir()
	dirs := makeProjectDirs(t, "main", "feature")
	mainDir, featureDir := dirs[0], dirs[1]
	if _, _, err := config.AssignProjectTag(home, mainDir, "sample"); err != nil {
		t.Fatalf("tag main: %v", err)
	}
	if _, _, err := config.AssignProjectTag(home, featureDir, "sample"); err != nil {
		t.Fatalf("tag feature: %v", err)
	}

	var stdout bytes.Buffer
	if code := runProjectShow(home, featureDir, false, &stdout); code != 0 {
		t.Fatalf("project show returned %d: %s", code, stdout.String())
	}
	output := stdout.String()
	for _, want := range []string{"Tag:", "(namespace origin)", "(this directory)", "Fallback namespace:", "Effective namespace:"} {
		if !strings.Contains(output, want) {
			t.Fatalf("show output missing %q: %s", want, output)
		}
	}
	if strings.Index(output, "Tag:") > strings.Index(output, "Fallback namespace:") {
		t.Fatalf("show output does not lead with the tag: %s", output)
	}

	stdout.Reset()
	untagged := t.TempDir()
	if code := runProjectShow(home, untagged, false, &stdout); code != 0 {
		t.Fatalf("project show untagged returned %d: %s", code, stdout.String())
	}
	output = stdout.String()
	if !strings.Contains(output, "Tag:                  none") || !strings.Contains(output, "Namespace:") {
		t.Fatalf("untagged show output unexpected: %s", output)
	}
	if strings.Contains(output, "Fallback namespace:") {
		t.Fatalf("untagged show output repeats identical namespaces: %s", output)
	}
}
