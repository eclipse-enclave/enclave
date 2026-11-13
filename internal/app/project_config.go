// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/gitworktree"
	"enclave/internal/model"
)

var (
	errInvalidWorktreeMetadata = errors.New("invalid worktree metadata")
	errConfigAnchorUnavailable = errors.New("config anchor unavailable")
)

// resolveProjectConfig never reads candidate configuration before verification.
// Requested inheritance either selects a verified anchor or fails; only checkouts
// that are not linked worktrees keep their own layer.
func resolveProjectConfig(project model.Project, metadata gitworktree.Result) (model.Project, error) {
	switch metadata.Status {
	case gitworktree.Invalid:
		return project, fmt.Errorf("%w for %s: %s; run 'git worktree repair' from the main repository (passing the moved worktree path if needed) before retrying", errInvalidWorktreeMetadata, project.RealDir, metadata.Reason)
	case gitworktree.VerifiedUnanchored:
		return project, fmt.Errorf("%w for linked worktree %s: %s; pass --worktree-inheritance=false or set it in this checkout's project config to use checkout-local configuration", errConfigAnchorUnavailable, project.RealDir, metadata.Reason)
	case gitworktree.Verified:
		project.ConfigDir = metadata.Anchor
		project.ConfigHash = config.ProjectHashForPath(metadata.Anchor)
	}
	return project, nil
}

// anchorWorktreeInheritance reads only the selector from the repository's
// anchor. Without a supported anchor, a candidate that opted in still counts as
// a request, so the start fails instead of silently using local inputs.
func anchorWorktreeInheritance(home string, metadata gitworktree.Result) (*bool, []config.Warning, error) {
	switch metadata.Status {
	case gitworktree.Verified:
		return config.LoadWorktreeInheritanceSelector(home, config.ProjectHashForPath(metadata.Anchor))
	case gitworktree.VerifiedUnanchored:
		candidates := []string{metadata.CommonDir}
		if filepath.Base(metadata.CommonDir) == ".git" {
			candidates = append(candidates, filepath.Dir(metadata.CommonDir))
		}
		var warnings []config.Warning
		for _, candidate := range candidates {
			value, more, err := config.LoadWorktreeInheritanceSelector(home, config.ProjectHashForPath(candidate))
			warnings = append(warnings, more...)
			if err != nil {
				return nil, warnings, err
			}
			if value != nil && *value {
				return value, warnings, nil
			}
		}
		return nil, warnings, nil
	}
	return nil, nil, nil
}

// worktreeInheritanceSelection is the bootstrap decision made before a project
// layer is loaded. projectValue is the deciding project-level setting, if any.
type worktreeInheritanceSelection struct {
	inherit        bool
	projectValue   *bool
	localWarnings  []config.Warning
	anchorWarnings []config.Warning
}

// selectWorktreeInheritance applies CLI > checkout-local project > anchor
// project > global > default.
func selectWorktreeInheritance(home string, parsed cli.Result, global config.Defaults, project model.Project, metadata gitworktree.Result) (worktreeInheritanceSelection, error) {
	selection := worktreeInheritanceSelection{inherit: config.DefaultOptions().WorktreeInheritance}
	fromCLI := parsed.Sources.WorktreeInheritance == model.SourceCLI
	var err error
	selection.projectValue, selection.localWarnings, err = config.LoadWorktreeInheritanceSelector(home, project.Hash)
	if err != nil {
		return selection, err
	}
	if selection.projectValue == nil && !fromCLI {
		if selection.projectValue, selection.anchorWarnings, err = anchorWorktreeInheritance(home, metadata); err != nil {
			return selection, err
		}
	}
	if global.WorktreeInheritance != nil {
		selection.inherit = *global.WorktreeInheritance
	}
	if selection.projectValue != nil {
		selection.inherit = *selection.projectValue
	}
	if fromCLI {
		selection.inherit = parsed.Options.WorktreeInheritance
	}
	return selection, nil
}

// ignoredWorktreeInputs warns about populated checkout-local inputs that an
// inherited layer replaces.
func ignoredWorktreeInputs(home string, project model.Project) []config.Warning {
	own := config.HostProjectOverridesDir(home, project.Hash)
	secrets := config.HostSecretsProjectDir(home, project.Hash)
	var ignored []string
	path := own
	if config.HasUserOverrides(own) {
		ignored = append(ignored, "project inputs in "+own)
	}
	if config.HasUserOverrides(secrets) {
		if len(ignored) == 0 {
			path = secrets
		}
		ignored = append(ignored, "project secrets in "+secrets)
	}
	if len(ignored) == 0 {
		return nil
	}
	return []config.Warning{{Code: config.WarningWorktreeOverridesIgnored, Path: path, Message: fmt.Sprintf("Ignoring worktree %s; this worktree inherits the project layer from %s (set worktree_inheritance to false in this checkout's config to keep them)", strings.Join(ignored, " and "), project.ConfigDir)}}
}

// loadDefaultsForAction selects the project layer before normal option merging.
// Only actions that need the config source require a valid relationship;
// inspection can fall back.
func loadDefaultsForAction(projectDir string, parsed cli.Result) (global, project config.Defaults, resolved model.Project, warnings []config.Warning, err error) {
	if parsed.Action == "cleanup" && parsed.Options.CleanupAll {
		global, warnings, err = config.LoadGlobalDefaults()
		return
	}
	resolved, err = config.ResolveProjectFromDir(projectDir)
	if err != nil {
		return
	}
	home, err := config.ResolveHostHome()
	if err != nil {
		return
	}
	global, warnings, err = config.LoadGlobalDefaultsFromHome(home)
	if err != nil {
		return
	}
	metadata := gitworktree.Resolve(resolved.RealDir)
	selection, err := selectWorktreeInheritance(home, parsed, global, resolved, metadata)
	if err != nil {
		return
	}
	if selection.inherit {
		resolved, err = resolveProjectConfig(resolved, metadata)
		if !actionRequiresConfigSource(parsed) {
			code := ""
			switch {
			case errors.Is(err, errInvalidWorktreeMetadata):
				code = config.WarningInvalidWorktreeMetadata
			case errors.Is(err, errConfigAnchorUnavailable):
				code = config.WarningConfigAnchorUnavailable
			}
			if code != "" {
				warnings = append(warnings, config.Warning{Code: code, Path: filepath.Join(resolved.RealDir, ".git"), Message: fmt.Sprintf("%v; using checkout-local configuration for %s, a session start would fail", err, parsed.Action)})
				err = nil
			}
		}
		if err != nil {
			return
		}
	}
	inherited := resolved.ConfigSourceHash() != resolved.Hash
	// The full read of the selected layer repeats its bootstrap warnings, so
	// keep only those of the file that is not loaded.
	if inherited {
		warnings = append(warnings, selection.localWarnings...)
	} else {
		warnings = append(warnings, selection.anchorWarnings...)
	}
	var more []config.Warning
	project, more, err = config.LoadProjectDefaults(home, resolved)
	project.WorktreeInheritance = selection.projectValue
	warnings = append(warnings, more...)
	if err != nil {
		return
	}
	if inherited {
		warnings = append(warnings, ignoredWorktreeInputs(home, resolved)...)
	}
	return
}

// validatePinnedConfigSource checks recorded identity without requiring the
// source to remain on disk. ps reports the historical pin; lifecycle readers
// additionally require its directory to be available and unchanged.
func validatePinnedConfigSource(dir, hash string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || config.ProjectHashForPath(dir) != hash {
		return errors.New("invalid pinned config source")
	}
	return nil
}

// resolvePinnedProjectConfig validates a recorded source without following the
// checkout's current Git metadata or accepting a new anchor through a symlink.
func resolvePinnedProjectConfig(project model.Project, dir, hash string) (model.Project, error) {
	if err := validatePinnedConfigSource(dir, hash); err != nil {
		return project, fmt.Errorf("%w; restart the session", err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real != dir {
		return project, fmt.Errorf("pinned config anchor %s is unavailable or changed; restart the session", dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return project, fmt.Errorf("pinned config anchor %s is not a directory; restart the session", dir)
	}
	project.ConfigDir, project.ConfigHash = dir, hash
	return project, nil
}
