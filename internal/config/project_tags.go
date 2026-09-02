// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	projectTagsVersion      = 1
	projectTagsMaxBytes     = 1 << 20
	projectTagsReadAttempts = 3
	projectTagsLockName     = "project-tags.lock"
)

var (
	projectTagNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	errProjectTagsChanged  = errors.New("project tag registry changed while opening")
	errProjectTagsNoChange = errors.New("project tags unchanged")
)

// ProjectTag assigns one effective project namespace to exact canonical host
// directories. Name is for display; Namespace remains the filesystem key.
type ProjectTag struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Members   []string `json:"members"`
}

// ProjectTags is the versioned host-owned project tag registry.
type ProjectTags struct {
	Version int          `json:"version"`
	Tags    []ProjectTag `json:"tags"`
}

// ProjectDescription reports both the path-derived fallback and the effective
// namespace selected by the host tag registry.
type ProjectDescription struct {
	Project           model.Project
	FallbackNamespace string
	Tag               *ProjectTag
}

// LoadProjectTags reads and validates the host-owned project tag registry.
// A missing registry is equivalent to an empty version-1 registry.
func LoadProjectTags(home string) (ProjectTags, error) {
	path := HostProjectTagsPath(home)
	var lastErr error
	for range projectTagsReadAttempts {
		registry, err := loadProjectTagsFile(path)
		if !errors.Is(err, errProjectTagsChanged) {
			return registry, err
		}
		lastErr = err
	}
	return ProjectTags{}, lastErr
}

func loadProjectTagsFile(path string) (ProjectTags, error) {
	empty := ProjectTags{Version: projectTagsVersion, Tags: []ProjectTag{}}
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return ProjectTags{}, fmt.Errorf("inspect project tag registry %s: %w", path, err)
	}
	if err := validateProjectTagsFile(path, before); err != nil {
		return ProjectTags{}, err
	}

	file, err := os.Open(path) // #nosec G304 -- path is the fixed host project-tag registry.
	if err != nil {
		return ProjectTags{}, fmt.Errorf("open project tag registry %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	opened, err := file.Stat()
	if err != nil {
		return ProjectTags{}, fmt.Errorf("stat open project tag registry %s: %w", path, err)
	}
	if !os.SameFile(before, opened) {
		return ProjectTags{}, fmt.Errorf("%w: %s", errProjectTagsChanged, path)
	}
	if err := validateProjectTagsFile(path, opened); err != nil {
		return ProjectTags{}, err
	}

	data, err := io.ReadAll(io.LimitReader(file, projectTagsMaxBytes+1))
	if err != nil {
		return ProjectTags{}, fmt.Errorf("read project tag registry %s: %w", path, err)
	}
	if len(data) > projectTagsMaxBytes {
		return ProjectTags{}, fmt.Errorf("project tag registry exceeds %d bytes: %s", projectTagsMaxBytes, path)
	}

	var registry ProjectTags
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return ProjectTags{}, fmt.Errorf("parse project tag registry %s: %w", path, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ProjectTags{}, fmt.Errorf("parse project tag registry %s: %w", path, err)
	}
	if err := validateProjectTags(registry); err != nil {
		return ProjectTags{}, fmt.Errorf("validate project tag registry %s: %w", path, err)
	}
	if registry.Tags == nil {
		registry.Tags = []ProjectTag{}
	}
	return registry, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unexpected data after registry object")
}

func validateProjectTagsFile(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("project tag registry must not be a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("project tag registry must be a regular file: %s", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("project tag registry must not be group- or world-writable: %s", path)
	}
	if err := validateProjectTagsFileOwner(info); err != nil {
		return fmt.Errorf("project tag registry %s: %w", path, err)
	}
	return nil
}

func validateProjectTags(registry ProjectTags) error {
	if registry.Version != projectTagsVersion {
		return fmt.Errorf("unsupported version %d (expected %d)", registry.Version, projectTagsVersion)
	}

	names := make(map[string]struct{}, len(registry.Tags))
	namespaces := make(map[string]string, len(registry.Tags))
	members := make(map[string]string)
	for _, tag := range registry.Tags {
		if !projectTagNamePattern.MatchString(tag.Name) {
			return fmt.Errorf("invalid tag name %q", tag.Name)
		}
		if _, exists := names[tag.Name]; exists {
			return fmt.Errorf("duplicate tag name %q", tag.Name)
		}
		names[tag.Name] = struct{}{}

		if !model.IsHashSegment(tag.Namespace) {
			return fmt.Errorf("tag %q has invalid namespace %q", tag.Name, tag.Namespace)
		}
		if existing, exists := namespaces[tag.Namespace]; exists {
			return fmt.Errorf("tags %q and %q use the same namespace %q", existing, tag.Name, tag.Namespace)
		}
		namespaces[tag.Namespace] = tag.Name

		if len(tag.Members) == 0 {
			return fmt.Errorf("tag %q has no members", tag.Name)
		}
		hasNamespaceOrigin := false
		for _, member := range tag.Members {
			if !filepath.IsAbs(member) || filepath.Clean(member) != member {
				return fmt.Errorf("tag %q has non-canonical member path %q", tag.Name, member)
			}
			if existing, exists := members[member]; exists {
				return fmt.Errorf("member %q belongs to both %q and %q", member, existing, tag.Name)
			}
			members[member] = tag.Name
			if ProjectHashForPath(member) == tag.Namespace {
				hasNamespaceOrigin = true
			}
		}
		if !hasNamespaceOrigin {
			return fmt.Errorf("tag %q namespace %q is not derived from any member", tag.Name, tag.Namespace)
		}
	}
	return nil
}

// updateProjectTags serializes and atomically persists one registry mutation.
func updateProjectTags(home string, update func(*ProjectTags) error) error {
	if strings.TrimSpace(home) == "" {
		return fmt.Errorf("host home is empty")
	}
	path := HostProjectTagsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create project tag registry directory: %w", err)
	}
	lockPath := HostLockPath(home, projectTagsLockName)
	return util.WithFileLock(lockPath, func() error {
		registry, err := loadProjectTagsFile(path)
		if err != nil {
			return err
		}
		if err := update(&registry); err != nil {
			if errors.Is(err, errProjectTagsNoChange) {
				return nil
			}
			return err
		}
		normalizeProjectTags(&registry)
		if err := validateProjectTags(registry); err != nil {
			return fmt.Errorf("validate updated project tags: %w", err)
		}
		data, err := json.MarshalIndent(registry, "", "  ")
		if err != nil {
			return fmt.Errorf("encode project tag registry: %w", err)
		}
		data = append(data, '\n')
		if err := util.WriteFileAtomic(path, data, 0o600); err != nil {
			return fmt.Errorf("write project tag registry %s: %w", path, err)
		}
		if err := util.SyncDir(filepath.Dir(path)); err != nil {
			return fmt.Errorf("sync project tag registry directory: %w", err)
		}
		return nil
	})
}

func normalizeProjectTags(registry *ProjectTags) {
	registry.Version = projectTagsVersion
	if registry.Tags == nil {
		registry.Tags = []ProjectTag{}
	}
	for i := range registry.Tags {
		sort.Strings(registry.Tags[i].Members)
	}
	sort.Slice(registry.Tags, func(i, j int) bool {
		return registry.Tags[i].Name < registry.Tags[j].Name
	})
}

// ProjectTagForDirectory returns the tag assigned to canonicalDir. Membership
// is exact; parent and child directories do not inherit tags. Existing paths
// that identify the same directory also match, which handles case aliases on
// case-insensitive filesystems.
func ProjectTagForDirectory(registry ProjectTags, canonicalDir string) (*ProjectTag, error) {
	var matched *ProjectTag
	for i := range registry.Tags {
		for _, member := range registry.Tags[i].Members {
			if !sameProjectTagDirectory(member, canonicalDir) {
				continue
			}
			if matched != nil && matched.Name != registry.Tags[i].Name {
				return nil, fmt.Errorf("project directory %s matches multiple tags", canonicalDir)
			}
			matched = cloneProjectTag(registry.Tags[i])
		}
	}
	return matched, nil
}

func cloneProjectTag(tag ProjectTag) *ProjectTag {
	cloned := tag
	cloned.Members = append([]string(nil), tag.Members...)
	return &cloned
}

func sameProjectTagDirectory(member string, canonicalDir string) bool {
	if member == canonicalDir {
		return true
	}
	memberInfo, err := os.Lstat(member)
	if err != nil || memberInfo.Mode()&os.ModeSymlink != 0 || !memberInfo.IsDir() {
		return false
	}
	currentInfo, err := os.Stat(canonicalDir)
	return err == nil && currentInfo.IsDir() && os.SameFile(memberInfo, currentInfo)
}

// ProjectTagByName returns a copy of the named tag, or nil when absent.
func ProjectTagByName(registry ProjectTags, name string) *ProjectTag {
	for i := range registry.Tags {
		if registry.Tags[i].Name == name {
			return cloneProjectTag(registry.Tags[i])
		}
	}
	return nil
}

// ProjectTagByNamespace returns a copy of the tag using namespace.
func ProjectTagByNamespace(registry ProjectTags, namespace string) *ProjectTag {
	for i := range registry.Tags {
		if registry.Tags[i].Namespace == namespace {
			return cloneProjectTag(registry.Tags[i])
		}
	}
	return nil
}

// DescribeProjectFromDir resolves a project and records how its namespace was
// selected. It is used by both the resolver and project diagnostics.
func DescribeProjectFromDir(home string, dir string) (ProjectDescription, error) {
	project, err := resolveCanonicalProject(dir)
	if err != nil {
		return ProjectDescription{}, err
	}
	fallback := project.Hash
	registry, err := LoadProjectTags(home)
	if err != nil {
		return ProjectDescription{}, err
	}
	tag, err := ProjectTagForDirectory(registry, project.RealDir)
	if err != nil {
		return ProjectDescription{}, err
	}
	if tag != nil {
		project.Hash = tag.Namespace
	}
	return ProjectDescription{Project: project, FallbackNamespace: fallback, Tag: tag}, nil
}

// AssignProjectTag assigns the exact canonical directory to name. It returns
// the resulting tag and whether the registry changed.
func AssignProjectTag(home string, dir string, name string) (ProjectTag, bool, error) {
	return AssignProjectTagToNamespace(home, dir, name, "")
}

// AssignProjectTagToNamespace assigns a tag only if its namespace still
// matches expectedNamespace. This prevents a tag created concurrently after a
// confirmation from redirecting the project to an unconfirmed namespace.
func AssignProjectTagToNamespace(home string, dir string, name string, expectedNamespace string) (ProjectTag, bool, error) {
	return assignProjectTag(home, dir, name, expectedNamespace, nil)
}

// AssignProjectTagToSnapshot assigns an existing tag only if its namespace and
// members still match the snapshot shown before confirmation.
func AssignProjectTagToSnapshot(home string, dir string, name string, expected ProjectTag) (ProjectTag, bool, error) {
	return assignProjectTag(home, dir, name, expected.Namespace, expected.Members)
}

func assignProjectTag(home string, dir string, name string, expectedNamespace string, expectedMembers []string) (ProjectTag, bool, error) {
	if !projectTagNamePattern.MatchString(name) {
		return ProjectTag{}, false, fmt.Errorf("invalid project tag %q (use lowercase letters, digits, '.', '_' or '-')", name)
	}
	project, err := resolveCanonicalProject(dir)
	if err != nil {
		return ProjectTag{}, false, err
	}

	var result ProjectTag
	changed := false
	err = updateProjectTags(home, func(registry *ProjectTags) error {
		if expectedMembers != nil {
			target := ProjectTagByName(*registry, name)
			if target == nil {
				return fmt.Errorf("project tag %q changed while assigning it; inspect the tag and retry", name)
			}
			if target.Namespace != expectedNamespace {
				return fmt.Errorf("project tag %q changed namespace while assigning it; inspect the tag and retry", name)
			}
			if !sameProjectTagMembers(target.Members, expectedMembers) {
				return fmt.Errorf("project tag %q changed members while assigning it; inspect the tag and retry", name)
			}
		}

		current, err := ProjectTagForDirectory(*registry, project.RealDir)
		if err != nil {
			return err
		}
		if current != nil {
			if current.Name != name {
				return fmt.Errorf("project is already tagged %q; run 'enclave project tag unset' before assigning %q", current.Name, name)
			}
			result = *current
			return errProjectTagsNoChange
		}

		for i := range registry.Tags {
			if registry.Tags[i].Name != name {
				continue
			}
			if expectedNamespace != "" && registry.Tags[i].Namespace != expectedNamespace {
				return fmt.Errorf("project tag %q changed namespace while assigning it; inspect the tag and retry", name)
			}
			registry.Tags[i].Members = append(registry.Tags[i].Members, project.RealDir)
			result = registry.Tags[i]
			changed = true
			return nil
		}

		if expectedNamespace != "" && project.Hash != expectedNamespace {
			return fmt.Errorf("project namespace changed while assigning tag %q; inspect the project and retry", name)
		}
		result = ProjectTag{Name: name, Namespace: project.Hash, Members: []string{project.RealDir}}
		registry.Tags = append(registry.Tags, result)
		changed = true
		return nil
	})
	return result, changed, err
}

func sameProjectTagMembers(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// RemoveProjectTag removes an exact member. memberPath may name a missing
// stored path so stale declarations can be removed. Non-empty expectedTag and
// expectedMember values bind a prior confirmation to the exact stored member
// and tag, without resolving the mutable path argument again.
func RemoveProjectTag(home string, currentDir string, memberPath string, expectedTag string, expectedMember string) (ProjectTag, string, bool, error) {
	member := expectedMember
	resolvedMember := ""
	if expectedMember == "" {
		var err error
		member, resolvedMember, err = resolveProjectTagMemberPath(currentDir, memberPath)
		if err != nil {
			return ProjectTag{}, "", false, err
		}
	}

	var removedTag ProjectTag
	changed := false
	err := updateProjectTags(home, func(registry *ProjectTags) error {
		var tagIndex, memberIndex int
		var stored string
		if expectedMember != "" {
			tagIndex, memberIndex = findExactProjectTagMember(*registry, expectedMember)
			stored = expectedMember
		} else {
			tagIndex, memberIndex, stored = findProjectTagMember(*registry, member, resolvedMember)
		}
		if tagIndex < 0 {
			if expectedMember != "" {
				return fmt.Errorf("confirmed project tag member %s changed while removing it; inspect 'enclave project tag list' and retry", expectedMember)
			}
			return errProjectTagsNoChange
		}
		member = stored

		tag := registry.Tags[tagIndex]
		if expectedTag != "" && tag.Name != expectedTag {
			return fmt.Errorf("project tag member %s moved from tag %q to %q while removing it; inspect 'enclave project tag list' and retry", member, expectedTag, tag.Name)
		}
		if len(tag.Members) > 1 && ProjectHashForPath(member) == tag.Namespace {
			return fmt.Errorf("cannot unset namespace-origin member %s while tag %q has other members; unset the other members first", member, tag.Name)
		}
		removedTag = tag
		removedTag.Members = append([]string(nil), tag.Members...)
		registry.Tags[tagIndex].Members = append(tag.Members[:memberIndex], tag.Members[memberIndex+1:]...)
		if len(registry.Tags[tagIndex].Members) == 0 {
			registry.Tags = append(registry.Tags[:tagIndex], registry.Tags[tagIndex+1:]...)
		}
		changed = true
		return nil
	})
	return removedTag, member, changed, err
}

func findExactProjectTagMember(registry ProjectTags, member string) (int, int) {
	for tagIndex := range registry.Tags {
		for memberIndex, stored := range registry.Tags[tagIndex].Members {
			if stored == member {
				return tagIndex, memberIndex
			}
		}
	}
	return -1, -1
}

// FindProjectTagMember reports which tag currently contains memberPath (or the
// current directory when memberPath is empty) and the stored member spelling,
// without mutating the registry. A nil tag means no member matched.
func FindProjectTagMember(home string, currentDir string, memberPath string) (*ProjectTag, string, error) {
	member, resolvedMember, err := resolveProjectTagMemberPath(currentDir, memberPath)
	if err != nil {
		return nil, "", err
	}
	registry, err := LoadProjectTags(home)
	if err != nil {
		return nil, "", err
	}
	tagIndex, _, stored := findProjectTagMember(registry, member, resolvedMember)
	if tagIndex < 0 {
		return nil, member, nil
	}
	return cloneProjectTag(registry.Tags[tagIndex]), stored, nil
}

// findProjectTagMember locates the tag and member index matching member (an
// exact stored spelling or the same directory) or its symlink-resolved form.
// It returns (-1, -1, "") when nothing matches.
func findProjectTagMember(registry ProjectTags, member string, resolvedMember string) (int, int, string) {
	for tagIndex := range registry.Tags {
		for memberIndex, stored := range registry.Tags[tagIndex].Members {
			matchesResolved := resolvedMember != "" && (stored == resolvedMember || sameProjectTagDirectory(stored, resolvedMember))
			if stored == member || sameProjectTagDirectory(stored, member) || matchesResolved {
				return tagIndex, memberIndex, stored
			}
		}
	}
	return -1, -1, ""
}

// ProjectTagNamespaceOrigin returns the member whose path-derived hash is the
// tag's namespace. It is empty only for an invalid in-memory tag.
func ProjectTagNamespaceOrigin(tag ProjectTag) string {
	for _, member := range tag.Members {
		if ProjectHashForPath(member) == tag.Namespace {
			return member
		}
	}
	return ""
}

func resolveProjectTagMemberPath(currentDir string, memberPath string) (string, string, error) {
	if strings.TrimSpace(memberPath) == "" {
		project, err := resolveCanonicalProject(currentDir)
		if err != nil {
			return "", "", err
		}
		return project.RealDir, project.RealDir, nil
	}
	candidate := memberPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(currentDir, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve project tag member %s: %w", memberPath, err)
	}
	member := filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return member, resolved, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("resolve project tag member %s: %w", memberPath, err)
	}
	return member, "", nil
}
