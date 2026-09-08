// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/prompt"
	"enclave/internal/util"
)

type projectTagMemberJSON struct {
	Path            string `json:"path"`
	Exists          bool   `json:"exists"`
	NamespaceOrigin bool   `json:"namespaceOrigin"`
}

type projectTagJSON struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace"`
	Members   []projectTagMemberJSON `json:"members"`
}

type projectShowJSON struct {
	ProjectDirectory   string          `json:"projectDirectory"`
	CanonicalDirectory string          `json:"canonicalDirectory"`
	FallbackNamespace  string          `json:"fallbackNamespace"`
	EffectiveNamespace string          `json:"effectiveNamespace"`
	Resolution         string          `json:"resolution"`
	Tag                *projectTagJSON `json:"tag"`
}

type projectTagListJSON struct {
	Tags []projectTagJSON `json:"tags"`
}

// projectCommandRequest carries the parsed `enclave project` subcommand
// arguments from the CLI layer.
type projectCommandRequest struct {
	Action   string
	Dir      string
	Tag      string
	Path     string
	JSON     bool
	Yes      bool
	New      bool
	Existing bool
}

// projectTagSetOptions selects how `project tag set` confirms its outcome:
// Yes skips the prompt, New refuses to assign an existing tag, and Existing
// refuses to create a new one.
type projectTagSetOptions struct {
	Yes      bool
	New      bool
	Existing bool
}

func runProjectCommand(req projectCommandRequest) int {
	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Errorf("resolve host home: %v", err)
		return 1
	}

	switch req.Action {
	case "project-show":
		return runProjectShow(home, req.Dir, req.JSON, os.Stdout)
	case "project-tag-set":
		opts := projectTagSetOptions{Yes: req.Yes, New: req.New, Existing: req.Existing}
		return runProjectTagSet(home, req.Dir, req.Tag, opts, os.Stdin, os.Stdout)
	case "project-tag-unset":
		return runProjectTagUnset(home, req.Dir, req.Path, req.Yes, os.Stdin, os.Stdout)
	case "project-tag-list":
		return runProjectTagList(home, req.JSON, os.Stdout)
	default:
		logx.Errorf("unsupported project action %q", req.Action)
		return 1
	}
}

func runProjectShow(home string, projectDir string, jsonOutput bool, stdout io.Writer) int {
	description, err := config.DescribeProjectFromDir(home, projectDir)
	if err != nil {
		logx.Errorf("resolve project tags: %v", err)
		logx.Infof("Move %s aside to recover from an invalid registry.", config.HostProjectTagsPath(home))
		return 1
	}

	output := buildProjectShowJSON(description)
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			logx.Errorf("encode project tag output: %v", err)
			return 1
		}
		return 0
	}

	_, _ = fmt.Fprintf(stdout, "Project directory:    %s\n", output.ProjectDirectory)
	if output.CanonicalDirectory != output.ProjectDirectory {
		_, _ = fmt.Fprintf(stdout, "Canonical directory:  %s\n", output.CanonicalDirectory)
	}
	if output.Tag == nil {
		_, _ = fmt.Fprintln(stdout, "Tag:                  none")
		_, _ = fmt.Fprintf(stdout, "Namespace:            %s\n", output.EffectiveNamespace)
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "Tag:                  %s\n", output.Tag.Name)
	_, _ = fmt.Fprintln(stdout, "Members:")
	writeProjectTagMembers(stdout, *description.Tag, description.Project.RealDir)
	_, _ = fmt.Fprintf(stdout, "Fallback namespace:   %s\n", output.FallbackNamespace)
	_, _ = fmt.Fprintf(stdout, "Effective namespace:  %s\n", output.EffectiveNamespace)
	return 0
}

func buildProjectShowJSON(description config.ProjectDescription) projectShowJSON {
	output := projectShowJSON{
		ProjectDirectory:   description.Project.Dir,
		CanonicalDirectory: description.Project.RealDir,
		FallbackNamespace:  description.FallbackNamespace,
		EffectiveNamespace: description.Project.Hash,
		Resolution:         "path",
	}
	if description.Tag == nil {
		return output
	}
	output.Resolution = "tag"
	tag := buildProjectTagJSON(*description.Tag)
	output.Tag = &tag
	return output
}

func buildProjectTagJSON(tag config.ProjectTag) projectTagJSON {
	origin := config.ProjectTagNamespaceOrigin(tag)
	output := projectTagJSON{
		Name:      tag.Name,
		Namespace: tag.Namespace,
		Members:   make([]projectTagMemberJSON, 0, len(tag.Members)),
	}
	for _, path := range tag.Members {
		output.Members = append(output.Members, projectTagMemberJSON{
			Path:            path,
			Exists:          projectTagMemberExists(path),
			NamespaceOrigin: path == origin,
		})
	}
	return output
}

func projectTagMemberExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// writeProjectTagMembers lists tag members with their roles: the namespace
// origin, the caller's directory (when currentDir is non-empty), and missing
// paths.
func writeProjectTagMembers(stdout io.Writer, tag config.ProjectTag, currentDir string) {
	origin := config.ProjectTagNamespaceOrigin(tag)
	notes := make([]string, len(tag.Members))
	width := 0
	for i, member := range tag.Members {
		var labels []string
		if member == origin {
			labels = append(labels, "namespace origin")
		}
		if currentDir != "" && member == currentDir {
			labels = append(labels, "this directory")
		}
		if !projectTagMemberExists(member) {
			labels = append(labels, "missing")
		}
		notes[i] = strings.Join(labels, ", ")
		if notes[i] != "" && len(member) > width {
			width = len(member)
		}
	}
	for i, member := range tag.Members {
		if notes[i] == "" {
			_, _ = fmt.Fprintf(stdout, "  %s\n", member)
			continue
		}
		_, _ = fmt.Fprintf(stdout, "  %-*s  (%s)\n", width, member, notes[i])
	}
}

func runProjectTagSet(home string, projectDir string, name string, opts projectTagSetOptions, stdin io.Reader, stdout io.Writer) int {
	description, err := config.DescribeProjectFromDir(home, projectDir)
	if err != nil {
		logx.Errorf("resolve project: %v", err)
		return 1
	}
	if description.Tag != nil {
		if description.Tag.Name == name {
			_, _ = fmt.Fprintf(stdout, "Project is already tagged %s.\n", name)
			return 0
		}
		logx.Errorf("project is already tagged %q; run 'enclave project tag unset' before assigning %q", description.Tag.Name, name)
		return 1
	}

	registry, err := config.LoadProjectTags(home)
	if err != nil {
		logx.Errorf("load project tags: %v", err)
		return 1
	}
	target := config.ProjectTagByName(registry, name)
	if target == nil && opts.Existing {
		logx.Errorf("project tag %q does not exist; run 'enclave project tag list' to inspect known tags, or drop --existing to create it", name)
		return 1
	}
	if target != nil && opts.New {
		logx.Errorf("project tag %q already exists; drop --new to assign it, or choose another name", name)
		return 1
	}

	var question string
	if target == nil {
		question = fmt.Sprintf("Create project tag %q?", name)
		_, _ = fmt.Fprintf(stdout, "Project tag %q does not exist yet and will be created.\n", name)
		_, _ = fmt.Fprintf(stdout, "This directory keeps its current namespace %s and becomes the tag's namespace origin:\n", description.FallbackNamespace)
		_, _ = fmt.Fprintf(stdout, "  %s\n", description.Project.RealDir)
	} else {
		question = fmt.Sprintf("Assign existing project tag %q?", name)
		_, _ = fmt.Fprintf(stdout, "Project tag %q exists. This directory will share its complete Enclave project scope with:\n", name)
		writeProjectTagMembers(stdout, *target, "")
		_, _ = fmt.Fprintln(stdout, "Tagged projects share project config, secrets, project-scoped auth, persisted environment, generated config, skills, patches, history, memory, caches, network state, lifecycle behavior, and cleanup scope.")
		_, _ = fmt.Fprintln(stdout, "The effective namespace changes:")
		_, _ = fmt.Fprintf(stdout, "  current: %s\n", description.FallbackNamespace)
		_, _ = fmt.Fprintf(stdout, "  tagged:  %s\n", target.Namespace)
		if paths := namespaceDataPaths(home, description.FallbackNamespace); len(paths) > 0 {
			_, _ = fmt.Fprintln(stdout, "Existing data under the current namespace will remain on disk but become dormant:")
			for _, path := range paths {
				_, _ = fmt.Fprintf(stdout, "  %s\n", path)
			}
		}
	}
	if !opts.Yes {
		confirmed, err := prompt.Confirm(question, stdin, stdout)
		if err != nil {
			logx.Errorf("read confirmation: %v", err)
			return 1
		}
		if !confirmed {
			_, _ = fmt.Fprintln(stdout, "Project tag unchanged.")
			return 1
		}
	}

	expectedNamespace := description.FallbackNamespace
	var tag config.ProjectTag
	var changed bool
	if target != nil {
		tag, changed, err = config.AssignProjectTagToSnapshot(home, projectDir, name, *target)
	} else {
		tag, changed, err = config.AssignProjectTagToNamespace(home, projectDir, name, expectedNamespace)
	}
	if err != nil {
		logx.Errorf("assign project tag: %v", err)
		return 1
	}
	if !changed {
		_, _ = fmt.Fprintf(stdout, "Project is already tagged %s.\n", tag.Name)
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "Tagged %s as %s (namespace %s).\n", description.Project.RealDir, tag.Name, tag.Namespace)
	writeProjectNamespaceTransitionWarning(stdout, description.Project.RealDir, description.FallbackNamespace, tag.Namespace)
	return 0
}

func runProjectTagUnset(home string, projectDir string, memberPath string, yes bool, stdin io.Reader, stdout io.Writer) int {
	expectedTag := ""
	expectedMember := ""
	explicitPath := strings.TrimSpace(memberPath) != ""
	if explicitPath {
		// An explicit path can detach a member of a different tag than the
		// current directory's, so report the match and confirm before removal.
		tag, stored, err := config.FindProjectTagMember(home, projectDir, memberPath)
		if err != nil {
			logx.Errorf("resolve project tag member: %v", err)
			return 1
		}
		if tag == nil {
			_, _ = fmt.Fprintf(stdout, "No project tag member matches %s.\n", stored)
			return 0
		}
		expectedTag = tag.Name
		expectedMember = stored
		if !yes {
			confirmed, err := prompt.Confirm(fmt.Sprintf("Remove %s from project tag %q?", stored, tag.Name), stdin, stdout)
			if err != nil {
				logx.Errorf("read confirmation: %v", err)
				return 1
			}
			if !confirmed {
				_, _ = fmt.Fprintln(stdout, "Project tag unchanged.")
				return 1
			}
		}
	}

	tag, member, changed, err := config.RemoveProjectTag(home, projectDir, memberPath, expectedTag, expectedMember)
	if err != nil {
		logx.Errorf("remove project tag: %v", err)
		return 1
	}
	if !changed {
		if explicitPath {
			_, _ = fmt.Fprintf(stdout, "No project tag member matches %s.\n", member)
		} else {
			_, _ = fmt.Fprintln(stdout, "Current project has no tag.")
		}
		return 0
	}
	fallbackNamespace := config.ProjectHashForPath(member)
	_, _ = fmt.Fprintf(stdout, "Removed %s from tag %s. Future invocations at that path use namespace %s.\n", member, tag.Name, fallbackNamespace)
	writeProjectNamespaceTransitionWarning(stdout, member, tag.Namespace, fallbackNamespace)
	return 0
}

func runProjectTagList(home string, jsonOutput bool, stdout io.Writer) int {
	registry, err := config.LoadProjectTags(home)
	if err != nil {
		logx.Errorf("load project tags: %v", err)
		logx.Infof("Move %s aside to recover from an invalid registry.", config.HostProjectTagsPath(home))
		return 1
	}
	// Writers keep the registry sorted; sort again so hand-edited but valid
	// files list deterministically too.
	sort.Slice(registry.Tags, func(i, j int) bool {
		return registry.Tags[i].Name < registry.Tags[j].Name
	})

	if jsonOutput {
		output := projectTagListJSON{Tags: make([]projectTagJSON, 0, len(registry.Tags))}
		for _, tag := range registry.Tags {
			output.Tags = append(output.Tags, buildProjectTagJSON(tag))
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			logx.Errorf("encode project tag list: %v", err)
			return 1
		}
		return 0
	}

	if len(registry.Tags) == 0 {
		_, _ = fmt.Fprintln(stdout, "No project tags.")
		return 0
	}
	for i, tag := range registry.Tags {
		if i > 0 {
			_, _ = fmt.Fprintln(stdout)
		}
		_, _ = fmt.Fprintf(stdout, "%s (namespace %s)\n", tag.Name, tag.Namespace)
		writeProjectTagMembers(stdout, tag, "")
	}
	return 0
}

func writeProjectNamespaceTransitionWarning(stdout io.Writer, member string, previousNamespace string, nextNamespace string) {
	if previousNamespace == nextNamespace {
		return
	}
	_, _ = fmt.Fprintf(stdout, "Warning: any sessions already running for %s retain namespace %s and must be restarted to use namespace %s.\n", member, previousNamespace, nextNamespace)
	_, _ = fmt.Fprintln(stdout, "Until then, use 'enclave ps' to find them, 'enclave status --all' or 'enclave network apply --all-running' for widened scope, and explicit container names with 'enclave attach' or 'enclave stop'.")
}

func namespaceDataPaths(home string, namespace string) []string {
	candidates := []string{
		config.HostProjectOverridesDir(home, namespace),
		config.HostProjectDir(home, namespace),
		filepath.Join(config.HostSecretsDir(home), "projects", namespace),
	}

	cacheRoot := config.HostCacheDir(home)
	globalCacheDirs := map[string]struct{}{
		filepath.Base(config.HostBuildDir(home)):      {},
		filepath.Base(config.HostSSHDir(home)):        {},
		filepath.Base(config.HostImageInboxDir(home)): {},
		"microvm": {},
	}
	if tools, err := os.ReadDir(cacheRoot); err == nil {
		for _, tool := range tools {
			if _, global := globalCacheDirs[tool.Name()]; !tool.IsDir() || global {
				continue
			}
			candidates = append(candidates, filepath.Join(cacheRoot, tool.Name(), namespace))
		}
	}
	paths := make([]string, 0, len(candidates))
	for _, path := range candidates {
		if namespacePathHasData(path) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return util.Dedupe(paths)
}

func namespacePathHasData(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if entry.Name() != "project.json" {
			return true
		}
	}
	return false
}
