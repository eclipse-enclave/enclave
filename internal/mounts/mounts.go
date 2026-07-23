// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mounts

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

const maxWorktreeMetadataBytes = 4 * 1024

var errSymlinkFile = errors.New("symlink file")

func validateDirPath(dir string, validated *[]string, projectReal string, home string) (realPath string, skip bool, err error) {
	if home != "" {
		dir = util.ExpandTilde(dir, home)
	}

	// Convert relative paths to absolute
	if !filepath.IsAbs(dir) {
		dir, err = filepath.Abs(dir)
		if err != nil {
			err = fmt.Errorf("cannot resolve path: %s", dir)
			return
		}
	}
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		err = fmt.Errorf("directory not found: %s", dir)
		return
	}

	realPath, err = filepath.EvalSymlinks(dir)
	if err != nil {
		err = fmt.Errorf("cannot resolve path: %s", dir)
		return
	}

	if util.IsCriticalSystemDir(realPath) {
		err = fmt.Errorf("mounting critical system directory not allowed: %s", realPath)
		return
	}
	if isDisallowedHomeMount(realPath, home) {
		err = fmt.Errorf("mounting other users' home directories not allowed: %s", realPath)
		return
	}
	if realPath == projectReal {
		logx.Warnf("Skipping duplicate: %s (already mounted as project directory)", dir)
		skip = true
		return
	}
	if containsDir(*validated, realPath) {
		logx.Warnf("Skipping duplicate directory: %s", dir)
		skip = true
		return
	}
	if util.IsSystemDir(realPath) {
		logx.Warnf("Mounting system directory: %s (proceed with caution)", realPath)
	}

	*validated = append(*validated, realPath)
	return
}

func isDisallowedHomeMount(realPath string, home string) bool {
	if home == "" {
		return false
	}
	if !util.HasPathPrefix(realPath, "/home") {
		return false
	}
	return !util.HasPathPrefix(realPath, home)
}

func ValidateExtraDirs(extraDirs []string, projectReal string, home string) ([]string, error) {
	return validateExtraDirs(extraDirs, nil, projectReal, home)
}

func ValidateExtraDirsWithExisting(extraDirs []string, existing []string, projectReal string, home string) ([]string, error) {
	validated := append([]string{}, existing...)
	return validateExtraDirs(extraDirs, &validated, projectReal, home)
}

func validateExtraDirs(extraDirs []string, seen *[]string, projectReal string, home string) ([]string, error) {
	if seen == nil {
		seen = &[]string{}
	}

	var validated []string
	var errorsFound []string

	for _, dir := range extraDirs {
		realPath, skip, err := validateDirPath(dir, seen, projectReal, home)
		if err != nil {
			errorsFound = append(errorsFound, err.Error())
			continue
		}
		if skip {
			continue
		}
		validated = append(validated, realPath)
	}

	if len(errorsFound) > 0 {
		return nil, errors.New(strings.Join(errorsFound, "\n"))
	}

	return validated, nil
}

func containsDir(dirs []string, target string) bool {
	for _, dir := range dirs {
		if dir == target {
			return true
		}
	}
	return false
}

func AddAdditional(mounts *[]backend.Mount, dirs []string, readOnly bool) {
	for _, dir := range dirs {
		*mounts = append(*mounts, backend.Mount{
			Type:          backend.MountTypeBind,
			Source:        dir,
			ContainerPath: dir,
			ReadOnly:      readOnly,
		})
		if readOnly {
			logx.Infof("Mounting additional read-only directory: %s", dir)
			continue
		}
		logx.Infof("Mounting additional directory: %s", dir)
	}
}

// ApplyProjectMountMode enforces project_mount=readonly on bind mounts.
func ApplyProjectMountMode(mount *backend.Mount, projectMount string) {
	if mount == nil || mount.Type != backend.MountTypeBind || !model.ProjectMountIsReadonly(projectMount) {
		return
	}
	mount.ReadOnly = true
}

func AddWorktree(mounts *[]backend.Mount, project model.Project, validatedDirs *[]string, readOnly bool) {
	gitFile := filepath.Join(project.Dir, ".git")
	data, err := readRegularFileInDir(project.Dir, ".git")
	if errors.Is(err, errSymlinkFile) {
		logx.Warnf("Ignoring .git symlink: %s", gitFile)
		return
	}
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return
	}
	line := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(line, "gitdir:") {
		return
	}

	gitdirPath := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if gitdirPath == "" {
		return
	}
	if !filepath.IsAbs(gitdirPath) {
		gitdirPath = filepath.Join(project.Dir, gitdirPath)
	}
	gitdirPath, err = filepath.EvalSymlinks(gitdirPath)
	if err != nil {
		return
	}

	commondirPath, hasCommondir := resolveWorktreeCommondir(gitdirPath)
	allowExternal := hasCommondir && isVerifiedExternalWorktree(project, gitdirPath, commondirPath)

	if realPath, ok := validateWorktreeMountPath(gitdirPath, validatedDirs, project.RealDir, "gitdir", allowExternal); ok {
		*mounts = append(*mounts, backend.Mount{
			Type:          backend.MountTypeBind,
			Source:        realPath,
			ContainerPath: realPath,
			ReadOnly:      readOnly,
		})
		logx.Infof("Mounted worktree gitdir: %s", realPath)
	}

	if !hasCommondir {
		return
	}

	if realPath, ok := validateWorktreeMountPath(commondirPath, validatedDirs, project.RealDir, "commondir", allowExternal); ok {
		*mounts = append(*mounts, backend.Mount{
			Type:          backend.MountTypeBind,
			Source:        realPath,
			ContainerPath: realPath,
			ReadOnly:      readOnly,
		})
		logx.Infof("Mounted worktree commondir: %s", realPath)
	}
}

func resolveWorktreeCommondir(gitdirPath string) (string, bool) {
	commondirData, err := readRegularFileInDir(gitdirPath, "commondir")
	if err != nil {
		return "", false
	}
	commondirPath := strings.TrimSpace(string(commondirData))
	if commondirPath == "" {
		return "", false
	}
	if !filepath.IsAbs(commondirPath) {
		commondirPath = filepath.Join(gitdirPath, commondirPath)
	}
	commondirPath, err = filepath.EvalSymlinks(commondirPath)
	if err != nil {
		return "", false
	}
	return commondirPath, true
}

func isVerifiedExternalWorktree(project model.Project, gitdirPath string, commondirPath string) bool {
	if util.PathWithin(project.RealDir, gitdirPath) {
		return false
	}
	if util.IsSystemDir(gitdirPath) || util.IsSystemDir(commondirPath) {
		return false
	}
	if !util.PathStrictlyWithin(filepath.Join(commondirPath, "worktrees"), gitdirPath) {
		return false
	}

	backPointerPath, err := readWorktreeBackPointer(gitdirPath)
	if err != nil {
		return false
	}
	projectGitFile, err := resolveExistingPath(projectGitFilePath(project))
	if err != nil {
		return false
	}
	return backPointerPath == projectGitFile
}

func projectGitFilePath(project model.Project) string {
	projectDir := strings.TrimSpace(project.RealDir)
	if projectDir == "" {
		projectDir = project.Dir
	}
	return filepath.Join(projectDir, ".git")
}

func readWorktreeBackPointer(gitdirPath string) (string, error) {
	backPointerData, err := readRegularFileInDir(gitdirPath, "gitdir")
	if err != nil {
		return "", err
	}
	backPointerPath := strings.TrimSpace(string(backPointerData))
	if backPointerPath == "" {
		return "", fmt.Errorf("empty gitdir back pointer")
	}
	if !filepath.IsAbs(backPointerPath) {
		backPointerPath = filepath.Join(gitdirPath, backPointerPath)
	}
	return resolveExistingPath(backPointerPath)
}

func readRegularFileInDir(dir string, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errSymlinkFile
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", name)
	}

	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("file changed while opening: %s", name)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxWorktreeMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxWorktreeMetadataBytes {
		return nil, fmt.Errorf("metadata file too large: %s", name)
	}
	return data, nil
}

func resolveExistingPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absPath)
}

// validateWorktreeMountPath enforces the policy for paths derived from a
// repo's `.git` pointer. The path must resolve to inside project.RealDir unless
// it belongs to a verified external linked-worktree relationship. System
// directories are hard-blocked (unlike --add-dir, where IsSystemDir is only
// warned). Returns the resolved path and true on success.
func validateWorktreeMountPath(path string, validatedDirs *[]string, projectReal string, label string, allowExternal bool) (string, bool) {
	if !allowExternal && !util.PathWithin(projectReal, path) {
		logx.Warnf("Ignoring .git %s pointer outside project dir: %s", label, path)
		return "", false
	}
	if util.IsSystemDir(path) {
		logx.Warnf("Refusing to mount worktree %s in system directory: %s", label, path)
		return "", false
	}
	realPath, skip, err := validateDirPath(path, validatedDirs, projectReal, "")
	if err != nil {
		logx.Warnf("Skipping worktree %s: %v", label, err)
		return "", false
	}
	if skip {
		return "", false
	}
	return realPath, true
}

// ResolvePorts converts Docker-style publish specs into neutral port
// mappings. Bindings that omit an explicit host-IP default to loopback
// (127.0.0.1); binding to another interface is an explicit opt-in via the
// "ip:host:container" form (e.g. "0.0.0.0:3000:3000"). A host port of "0"
// (e.g. "0:3000") requests an OS-assigned host port at runtime.
func ResolvePorts(ports []string) []backend.PortMapping {
	var mappings []backend.PortMapping
	for _, port := range ports {
		hostIP, hostPort, containerPort, ok := util.ParsePortSpec(port)
		if !ok {
			if strings.TrimSpace(port) != "" {
				logx.Warnf("Invalid port format: %s (use '3000', '3000:8080', '0:3000' for an auto-assigned host port, or '127.0.0.1:3000:8080')", port)
			}
			continue
		}
		if hostIP == "" {
			hostIP = "127.0.0.1"
		}
		mappings = append(mappings, backend.PortMapping{HostIP: hostIP, HostPort: hostPort, ContainerPort: containerPort, Protocol: "tcp"})
	}
	return mappings
}
