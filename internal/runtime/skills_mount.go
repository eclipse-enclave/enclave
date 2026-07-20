// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"

	"sigs.k8s.io/yaml"
)

var portableSkillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type skillSource struct {
	dir      string
	portable bool
}

func (r *Runtime) addSkillMounts(mounts *mountAccumulator) error {
	skillsDir := strings.TrimSpace(r.profile.SkillsDir)
	if skillsDir == "" {
		return nil
	}
	containerSkillsDir := resolveContainerProfilePath(r.containerHome, skillsDir)

	globalSkillsDir := config.HostSkillsDir(r.host.Home)
	projectSkillsDir := config.HostProjectSkillsDir(r.host.Home, r.project.Hash)
	projectToolDir := config.HostProjectToolDir(r.host.Home, r.project.Hash, r.profile.Name)
	generatedSkillsDir := filepath.Join(projectToolDir, model.GeneratedSkillsDirName)

	if err := os.MkdirAll(globalSkillsDir, 0o700); err != nil {
		return fmt.Errorf("create global shared skills directory %q: %w", globalSkillsDir, err)
	}
	if err := os.MkdirAll(projectSkillsDir, 0o700); err != nil {
		return fmt.Errorf("create project shared skills directory %q: %w", projectSkillsDir, err)
	}
	if err := os.MkdirAll(generatedSkillsDir, 0o700); err != nil {
		return fmt.Errorf("create generated skills directory %q: %w", generatedSkillsDir, err)
	}

	// Overlay order (later wins): built-in tool, user-extension, enabled
	// features, global shared, project shared.
	sources := make([]skillSource, 0, 4)
	for _, sourceDir := range r.extensionSkillSourceDirs() {
		sources = append(sources, skillSource{dir: sourceDir})
	}
	for _, sourceDir := range r.featureSkillSourceDirs() {
		sources = append(sources, skillSource{dir: sourceDir})
	}
	sources = append(sources,
		skillSource{dir: globalSkillsDir, portable: true},
		skillSource{dir: projectSkillsDir, portable: true},
	)

	if err := r.withToolDataLock(projectToolDir, "skills", func() error {
		return mergeSkillSources(generatedSkillsDir, sources)
	}); err != nil {
		return fmt.Errorf("prepare skills mount for %s: %w", r.profile.Name, err)
	}

	mounts.AddMount(bindMount(generatedSkillsDir, containerSkillsDir, true))
	mounts.AddEnv(model.EnvToolSkillsDir, containerSkillsDir)
	logx.Infof("%s skills mounted", util.TitleCase(r.profile.Name))
	return nil
}

func (r *Runtime) extensionSkillSourceDirs() []string {
	sourceDirs := make([]string, 0, 2)
	for _, toolsRoot := range []string{r.paths.ToolsDir, r.paths.UserToolsDir} {
		if toolsRoot == "" {
			continue
		}
		skillsDir := filepath.Join(toolsRoot, r.profile.Name, model.SkillsDirName)
		if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
			sourceDirs = append(sourceDirs, skillsDir)
		}
	}
	return sourceDirs
}

// featureSkillSourceDirs returns skill directories shipped by enabled features
// (built-in and user extension trees). Only enabled features contribute, so a
// feature's skills are absent from the mounted skills directory unless the
// session opted into the feature (e.g. `--features playwright`). Composing here
// (rather than writing at container start) is required because the tool skills
// directory is mounted read-only.
func (r *Runtime) featureSkillSourceDirs() []string {
	var dirs []string
	for _, feature := range r.features {
		for _, root := range []string{r.paths.FeaturesDir, r.paths.UserFeaturesDir} {
			if root == "" {
				continue
			}
			skillsDir := filepath.Join(root, feature.Name, model.SkillsDirName)
			if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
				dirs = append(dirs, skillsDir)
			}
		}
	}
	return dirs
}

func (r *Runtime) withToolDataLock(projectToolDir string, prefix string, fn func() error) error {
	lockName := prefix + "-" + util.HashString(projectToolDir) + ".lock"
	lockPath := config.HostLockPath(r.host.Home, lockName)
	return util.WithFileLock(lockPath, fn)
}

func mergeSkillSources(targetDir string, sources []skillSource) error {
	if err := clearDirectory(targetDir); err != nil {
		return fmt.Errorf("clear generated skills directory %q: %w", targetDir, err)
	}
	for _, source := range sources {
		overlay := overlaySkillSource
		if source.portable {
			overlay = overlayPortableSkillSource
		}
		if err := overlay(targetDir, source.dir); err != nil {
			return fmt.Errorf("overlay skills from %q: %w", source.dir, err)
		}
	}
	return nil
}

func clearDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read directory %q: %w", dir, err)
	}
	for _, entry := range entries {
		entryPath := filepath.Join(dir, entry.Name())
		if err := os.RemoveAll(entryPath); err != nil {
			return fmt.Errorf("remove %q: %w", entryPath, err)
		}
	}
	return nil
}

func overlaySkillSource(targetDir string, sourceDir string) error {
	return overlayValidatedSkillSource(targetDir, sourceDir, nil)
}

func overlayPortableSkillSource(targetDir string, sourceDir string) error {
	return overlayValidatedSkillSource(targetDir, sourceDir, validatePortableSkill)
}

func overlayValidatedSkillSource(targetDir string, sourceDir string, validate func(string, string) error) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("read source directory %q: %w", sourceDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sourcePath := filepath.Join(sourceDir, entry.Name())
		if validate != nil {
			if err := validate(sourcePath, entry.Name()); err != nil {
				logx.Warnf("Skipping shared skill %q from %s: %v", entry.Name(), sourceDir, err)
				continue
			}
		}
		targetPath := filepath.Join(targetDir, entry.Name())
		if err := os.RemoveAll(targetPath); err != nil {
			return fmt.Errorf("remove existing target %q: %w", targetPath, err)
		}
		if err := copySkillDirectory(sourcePath, targetPath); err != nil {
			return fmt.Errorf("copy skill directory %q to %q: %w", sourcePath, targetPath, err)
		}
	}
	return nil
}

type portableSkillMetadata struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	License       string         `json:"license,omitempty"`
	Compatibility string         `json:"compatibility,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

func validatePortableSkill(skillDir string, directoryName string) error {
	skillPath := filepath.Join(skillDir, "SKILL.md")
	info, err := os.Lstat(skillPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("missing SKILL.md")
		}
		return fmt.Errorf("inspect SKILL.md: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("SKILL.md must be a regular file")
	}
	// #nosec G304 -- skillPath is rooted in a user-managed shared skill directory.
	content, err := os.ReadFile(skillPath)
	if err != nil {
		return fmt.Errorf("read SKILL.md: %w", err)
	}
	frontmatter, err := extractSkillFrontmatter(content)
	if err != nil {
		return err
	}
	var metadata portableSkillMetadata
	if err := yaml.UnmarshalStrict(frontmatter, &metadata); err != nil {
		return fmt.Errorf("frontmatter must use only portable fields (name, description, license, compatibility, metadata): %w", err)
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	metadata.Description = strings.TrimSpace(metadata.Description)
	if metadata.Name == "" {
		return fmt.Errorf("frontmatter name is required")
	}
	if metadata.Name != directoryName {
		return fmt.Errorf("frontmatter name %q must match directory name %q", metadata.Name, directoryName)
	}
	if len(metadata.Name) > 64 || !portableSkillNamePattern.MatchString(metadata.Name) {
		return fmt.Errorf("frontmatter name must be at most 64 lowercase letters, numbers, or hyphen-separated segments")
	}
	if metadata.Description == "" {
		return fmt.Errorf("frontmatter description is required")
	}
	if utf8.RuneCountInString(metadata.Description) > 1024 {
		return fmt.Errorf("frontmatter description exceeds 1024 characters")
	}
	if utf8.RuneCountInString(metadata.Compatibility) > 500 {
		return fmt.Errorf("frontmatter compatibility exceeds 500 characters")
	}
	return nil
}

func extractSkillFrontmatter(content []byte) ([]byte, error) {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return nil, fmt.Errorf("SKILL.md must start with YAML frontmatter")
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return []byte(strings.Join(lines[1:i], "\n")), nil
		}
	}
	return nil, fmt.Errorf("SKILL.md frontmatter is not terminated with ---")
}

func copySkillDirectory(sourceDir string, targetDir string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %q: %w", path, walkErr)
		}

		if !util.PathWithin(sourceDir, path) {
			return fmt.Errorf("skill path %q is outside source dir %q", path, sourceDir)
		}
		relativePath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return fmt.Errorf("resolve relative path for %q: %w", path, err)
		}
		targetPath := filepath.Join(targetDir, relativePath)

		if entry.IsDir() {
			if err := mkdirFromEntry(entry, targetPath); err != nil {
				return fmt.Errorf("create directory %q: %w", targetPath, err)
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			logx.Debugf("Skipping symlink while preparing managed skills: %s", path)
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("read metadata for %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := copySkillFile(path, targetPath, info.Mode().Perm()); err != nil {
			return fmt.Errorf("copy file %q to %q: %w", path, targetPath, err)
		}
		return nil
	})
}

func mkdirFromEntry(entry os.DirEntry, targetPath string) error {
	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("read directory metadata for %q: %w", targetPath, err)
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o755
	}
	if err := os.MkdirAll(targetPath, mode); err != nil {
		return fmt.Errorf("create directory %q: %w", targetPath, err)
	}
	return nil
}

func copySkillFile(sourcePath string, targetPath string, mode os.FileMode) (err error) {
	// #nosec G304 -- sourcePath is produced by filepath.WalkDir rooted in a
	// trusted managed skills directory.
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source file %q: %w", sourcePath, err)
	}
	defer func() {
		err = errors.Join(err, sourceFile.Close())
	}()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return fmt.Errorf("create parent directory %q: %w", filepath.Dir(targetPath), err)
	}
	// #nosec G304 -- targetPath is derived from filepath.Rel over a trusted
	// source root, then joined into the managed generated skills directory.
	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("open target file %q: %w", targetPath, err)
	}
	defer func() {
		err = errors.Join(err, targetFile.Close())
	}()

	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return fmt.Errorf("copy contents: %w", err)
	}

	return nil
}
