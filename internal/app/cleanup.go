// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"enclave/internal/config"
	"enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/prompt"
	"enclave/internal/util"
)

// persistentConfigStoreKey is the config-store key for the persistent ("kept")
// store; every other key denotes an ephemeral session/worktree store. It
// mirrors defaultStoreKey in internal/backend/docker.
const persistentConfigStoreKey = "default"

// Cleanup dir kinds. Each maps to a --keep flag, except configStoreKind, which
// is a sub-kind of history: it is kept by --keep history like the rest, but
// session-scoped memory couples to it alone rather than to every history dir.
const (
	cacheKind       = "cache"
	historyKind     = "history"
	configStoreKind = "config-store"
	memoryKind      = "memory"
	authKind        = "auth"
	ephemeralKind   = "ephemeral"
)

// memoryScopeResolver answers each tool's declared memory scope for one cleanup
// run, caching the result so a full sweep over every project does not re-read
// the same specs once per project. A path-resolution failure is carried into
// every lookup, so it surfaces exactly where a plan consults the scope.
type memoryScopeResolver struct {
	paths    model.Paths
	pathsErr error
	scopes   map[string]string
}

func newMemoryScopeResolver(paths model.Paths, pathsErr error) *memoryScopeResolver {
	return &memoryScopeResolver{paths: paths, pathsErr: pathsErr, scopes: map[string]string{}}
}

// scopeFor returns tool's memory scope, never the empty string: a spec that
// declares none resolves to the default exactly like a tool with no installed
// spec at all. A missing spec is not an error because the host state tree keeps
// a directory per tool that ever ran in a project, including tools since removed
// from the extension tree, and cleanup must still be able to delete those. A
// spec that exists but does not load is reported; whether that aborts the run is
// the caller's decision, since only some plans can act on the answer.
func (r *memoryScopeResolver) scopeFor(tool string) (string, error) {
	if r.pathsErr != nil {
		return "", fmt.Errorf("resolve extension paths: %w", r.pathsErr)
	}
	if scope, ok := r.scopes[tool]; ok {
		return scope, nil
	}
	scope := model.MemoryScopeProject
	profile, err := config.LoadProfile(r.paths, tool)
	switch {
	case err == nil:
		// Load-time normalization only resolves the scope of tools that
		// declare memory; for the rest the undeclared scope is the default.
		scope = model.ResolveMemoryScope(profile.MemoryScope)
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("load %s: %w", tool, err)
	}
	r.scopes[tool] = scope
	return scope, nil
}

// memoryScopeAffectsPlan reports whether a tool's memory scope can change what
// the given cleanup removes. Only the flag combinations that couple memory to
// the config store depend on it; a full-tree cleanup removes both regardless.
func memoryScopeAffectsPlan(cleanup model.CleanupOptions) bool {
	if cleanup.CleanupEphemeral {
		return true
	}
	return !cleanup.CleanupAll && (cleanup.CleanupKeepHist || cleanup.CleanupKeepMemory)
}

func runCleanup(run model.RunOptions, cleanup model.CleanupOptions) int {
	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Errorf("Failed to resolve home directory: %v", err)
		return 1
	}
	if !config.IsWritableDir(home) {
		logx.Errorf("home directory is not writable: %s (set HOME to a writable path)", home)
		return 1
	}

	var project model.Project
	if !cleanup.CleanupAll {
		proj, err := config.ResolveProject()
		if err != nil {
			logx.Errorf("Failed to resolve project: %v", err)
			return 1
		}
		project = proj
	}
	// Session-scoped memory is removed together with its config store, so the
	// plan depends on each tool's declared scope. A path or spec problem is
	// carried by the resolver and handled where a plan consults the scope:
	// aborting before anything is deleted when the scope can change the plan,
	// and proceeding under the default otherwise. Cleanup is what a user
	// reaches for when the extension tree is broken or gone.
	paths, pathsErr := config.ResolvePaths()
	scopes := newMemoryScopeResolver(paths, pathsErr)

	if cleanup.CleanupEphemeral {
		containerNames, containersErr := resolveEphemeralContainers(run, cleanup, project)
		if containersErr != nil {
			logx.Errorf("Failed to list containers: %v", containersErr)
			return 1
		}
		// Ephemeral config stores are host directories keyed by a session or
		// worktree suffix.
		storeDirs, err := resolveEphemeralStoreDirs(run, cleanup, home, project, scopes)
		if err != nil {
			logx.Errorf("Failed to resolve memory cleanup policy: %v", err)
			return 1
		}
		if cleanup.CleanupDryRun {
			printEphemeralCleanupPlan(containerNames, storeDirs)
			cleanupBuildCache(cleanup)
			return 0
		}
		cleanupContainers(containerNames)
		cleanupDirs(storeDirs)
		cleanupBuildCache(cleanup)
		logx.Successf("Cleanup complete")
		return 0
	}

	memoryScope, err := scopes.scopeFor(run.Tool)
	if err != nil {
		if memoryScopeAffectsPlan(cleanup) {
			logx.Errorf("Failed to resolve memory cleanup policy: %v", err)
			return 1
		}
		// No --keep flag couples memory to the config store here, so the
		// unreadable scope cannot change what is removed. Refusing would strand
		// the state of exactly the broken extension the user is cleaning up.
		logx.Warnf("Ignoring unreadable memory cleanup policy: %v", err)
		memoryScope = model.MemoryScopeProject
	}
	dirPaths := cleanupDirsForRemoval(run, cleanup, home, project, memoryScope)

	if cleanup.CleanupDryRun {
		printCleanupPlan(dirPaths)
		cleanupBuildCache(cleanup)
		return 0
	}

	cleanupDirs(dirPaths)
	cleanupBuildCache(cleanup)

	logx.Successf("Cleanup complete")
	return 0
}

func resolveEphemeralContainers(run model.RunOptions, cleanup model.CleanupOptions, project model.Project) ([]string, error) {
	if err := checkDocker(); err != nil {
		return nil, err
	}

	containerFilters := docker.NewFilters()
	containerFilters.Add("status", "exited")
	listed, err := docker.ContainerList(context.Background(), docker.ListOptions{All: true, Filters: containerFilters})
	if err != nil {
		return nil, err
	}

	// Build a set of container names, excluding named sessions (which have a
	// LabelSession label and are not ephemeral).
	type containerInfo struct {
		name       string
		hasSession bool
	}
	infos := map[string]containerInfo{}
	for _, item := range listed {
		_, hasSession := item.Labels[model.LabelSession]
		_, hasEphemeral := item.Labels[model.LabelEphemeral]
		// Containers with a session label (but no ephemeral label) are named
		// sessions and should not be cleaned up as ephemeral.
		isNamedSession := hasSession && !hasEphemeral
		for _, name := range item.Names {
			name = strings.TrimPrefix(strings.TrimSpace(name), "/")
			if name == "" {
				continue
			}
			infos[name] = containerInfo{name: name, hasSession: isNamedSession}
		}
	}
	var containers []string
	if cleanup.CleanupAll {
		for _, info := range infos {
			if info.name == "" || info.hasSession {
				continue
			}
			if !isEphemeralContainer(info.name) {
				continue
			}
			containers = append(containers, info.name)
		}
		sort.Strings(containers)
		return containers, nil
	}

	if project.Hash == "" {
		return nil, fmt.Errorf("project hash is empty")
	}

	base := fmt.Sprintf("%s-%s-%s-", model.AppName, run.Tool, project.Hash)
	for _, info := range infos {
		if info.name == "" || info.hasSession {
			continue
		}
		if !strings.HasPrefix(info.name, base) {
			continue
		}
		if !isEphemeralContainer(info.name) {
			continue
		}
		containers = append(containers, info.name)
	}
	sort.Strings(containers)
	return containers, nil
}

// resolveEphemeralStoreDirs enumerates the host directories backing ephemeral
// config stores (every config-store key other than the persistent "default"
// key).
func resolveEphemeralStoreDirs(run model.RunOptions, cleanup model.CleanupOptions, home string, project model.Project, scopes *memoryScopeResolver) ([]cleanupDir, error) {
	var hashes []string
	if cleanup.CleanupAll {
		hashes = listSubdirs(config.HostProjectsDir(home))
	} else {
		if project.Hash == "" {
			return nil, nil
		}
		hashes = []string{project.Hash}
	}

	var dirs []cleanupDir
	for _, hash := range hashes {
		var tools []string
		if cleanup.CleanupAll {
			tools = listSubdirs(config.HostProjectDir(home, hash))
		} else {
			tools = []string{run.Tool}
		}
		for _, tool := range tools {
			storeRoot := config.HostStoreConfigRootDir(home, tool, hash)
			keys := listSubdirs(storeRoot)
			if len(keys) == 0 {
				continue
			}
			scope, err := scopes.scopeFor(tool)
			if err != nil {
				return nil, err
			}
			for _, key := range keys {
				if key == persistentConfigStoreKey {
					continue
				}
				memoryDir := ""
				if scope == model.MemoryScopeSession {
					if dir := config.HostProjectMemorySessionDir(home, hash, tool, key); util.PathExists(dir) {
						memoryDir = dir
					}
				}
				// Session memory and its config store are one unit here too, so
				// --keep memory retains the pair. Keys with nothing to keep are
				// still removed: every store of a tool without session memory,
				// and the timestamp-keyed stores of --ephemeral runs, which
				// never get a memory mount.
				if memoryDir != "" && cleanup.CleanupKeepMemory {
					continue
				}
				dirs = append(dirs, cleanupDir{Kind: ephemeralKind, Path: filepath.Join(storeRoot, key)})
				if memoryDir != "" {
					dirs = append(dirs, cleanupDir{Kind: memoryKind, Path: memoryDir})
				}
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Path < dirs[j].Path })
	return dirs, nil
}

// listSubdirs returns the immediate subdirectory names of dir, or nil when dir
// does not exist or cannot be read.
func listSubdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

type cleanupDir struct {
	Kind string
	Path string
}

func resolveCleanupDirs(run model.RunOptions, cleanup model.CleanupOptions, home string, project model.Project) []cleanupDir {
	if cleanup.CleanupAll {
		dirs := []cleanupDir{
			{Kind: cacheKind, Path: config.HostCacheDir(home)},
			// The state projects tree holds every project's config/env stores
			// and history, so a full cleanup removes them all at once.
			{Kind: historyKind, Path: config.HostProjectsDir(home)},
			// The image inbox is global (not project-scoped), so it is only
			// removed by a full cleanup. Held images are user-imported content.
			{Kind: "inbox", Path: config.HostImageInboxDir(home)},
		}
		// Shared tool/feature auth stores live outside the projects tree; they
		// are removed on a full cleanup unless `--keep auth` is set.
		return append(dirs, authStoreCleanupDirs(home)...)
	}

	projectDataDir := config.HostProjectToolDir(home, project.Hash, run.Tool)
	return []cleanupDir{
		{Kind: cacheKind, Path: config.HostCacheToolProjectDir(home, run.Tool, project.Hash)},
		{Kind: historyKind, Path: filepath.Join(projectDataDir, "history")},
		{Kind: historyKind, Path: config.HostProjectHomeConfigDir(home, project.Hash, run.Tool)},
		{Kind: historyKind, Path: config.HostProjectGeneratedConfigDir(home, project.Hash, run.Tool)},
		{Kind: historyKind, Path: filepath.Join(projectDataDir, model.GeneratedSkillsDirName)},
		{Kind: configStoreKind, Path: config.HostStoreConfigRootDir(home, run.Tool, project.Hash)},
		{Kind: historyKind, Path: config.HostStoreEnvDir(home, run.Tool, project.Hash)},
		{Kind: memoryKind, Path: config.HostProjectMemoryDir(home, project.Hash, run.Tool)},
	}
}

// authStoreCleanupDirs enumerates the shared tool and feature auth store
// directories on the host. They are project-independent, so they only
// participate in a full (--all) cleanup and are gated by `--keep auth`.
func authStoreCleanupDirs(home string) []cleanupDir {
	var dirs []cleanupDir
	for _, tool := range listSubdirs(config.HostStoreAuthRootDir(home)) {
		dirs = append(dirs, cleanupDir{Kind: authKind, Path: config.HostStoreAuthTreeDir(home, tool)})
	}
	for _, feature := range listSubdirs(config.HostStoreFeatureAuthRootDir(home)) {
		dirs = append(dirs, cleanupDir{Kind: authKind, Path: config.HostStoreFeatureAuthDir(home, feature)})
	}
	return dirs
}

// cleanupDirsForRemoval resolves the host directories to remove for the given
// cleanup options, applying keep-* flags and session memory/config coupling.
func cleanupDirsForRemoval(run model.RunOptions, cleanup model.CleanupOptions, home string, project model.Project, memoryScope string) []cleanupDir {
	dirs := resolveCleanupDirs(run, cleanup, home, project)
	if cleanup.CleanupKeepCache {
		dirs = filterDirs(dirs, cacheKind)
	}
	if cleanup.CleanupKeepHist {
		dirs = filterDirs(dirs, historyKind, configStoreKind)
	}
	if cleanup.CleanupKeepMemory {
		dirs = filterDirs(dirs, memoryKind)
	}
	if cleanup.CleanupKeepAuth {
		dirs = filterDirs(dirs, authKind)
	}
	if !cleanup.CleanupAll && memoryScope == model.MemoryScopeSession {
		// Session memory and the config store holding the database that indexes
		// it form one cleanup unit: keeping either keeps both. The remaining
		// history dirs (shell history, env store, generated config) are not
		// coupled and stay subject to --keep history alone.
		if cleanup.CleanupKeepHist {
			dirs = filterDirs(dirs, memoryKind)
		}
		if cleanup.CleanupKeepMemory {
			dirs = filterDirs(dirs, configStoreKind)
		}
	}
	return dirs
}

func filterDirs(dirs []cleanupDir, kinds ...string) []cleanupDir {
	var filtered []cleanupDir
	for _, dir := range dirs {
		if slices.Contains(kinds, dir.Kind) {
			continue
		}
		filtered = append(filtered, dir)
	}
	return filtered
}

func isEphemeralContainer(name string) bool {
	if !strings.HasPrefix(name, model.AppName+"-") {
		return false
	}
	if strings.HasSuffix(name, model.GatewayContainerSuffix) {
		return false
	}
	lastDash := strings.LastIndex(name, "-")
	if lastDash == -1 || lastDash == len(name)-1 {
		return false
	}
	prefix := name[:lastDash]
	prevDash := strings.LastIndex(prefix, "-")
	if prevDash == -1 || prevDash == len(prefix)-1 {
		return false
	}
	hash := prefix[prevDash+1:]
	return model.IsHashSegment(hash)
}

func printCleanupPlan(dirs []cleanupDir) {
	if len(dirs) == 0 {
		logx.Infof("Nothing to clean")
		return
	}
	for _, dir := range dirs {
		logx.Infof("Would remove %s: %s", dir.Kind, dir.Path)
	}
}

func printEphemeralCleanupPlan(containers []string, dirs []cleanupDir) {
	if len(containers) == 0 && len(dirs) == 0 {
		logx.Infof("Nothing to clean")
		return
	}
	for _, container := range containers {
		logx.Infof("Would remove container: %s", container)
	}
	for _, dir := range dirs {
		logx.Infof("Would remove %s: %s", dir.Kind, dir.Path)
	}
}

func cleanupContainers(containers []string) {
	for _, container := range containers {
		if err := docker.ContainerRemove(context.Background(), container, true, true); err != nil {
			logx.Warnf("Failed to remove container %s: %v", container, err)
		}
	}
}

func cleanupDirs(dirs []cleanupDir) {
	for _, dir := range dirs {
		if err := os.RemoveAll(dir.Path); err != nil {
			logx.Warnf("Failed to remove %s at %s: %v", dir.Kind, dir.Path, err)
		}
	}
}

func cleanupBuildCache(cleanup model.CleanupOptions) {
	if !cleanup.CleanupBuildCache {
		return
	}

	ctx := context.Background()
	total, reclaimable, err := docker.BuildCacheUsage(ctx)
	if err != nil {
		logx.Warnf("Failed to query build cache: %v", err)
		return
	}

	if cleanup.CleanupDryRun {
		logx.Infof("Build cache: %s total, %s reclaimable", util.FormatBytes(total), util.FormatBytes(reclaimable))
		return
	}

	if reclaimable == 0 {
		logx.Infof("Build cache: nothing to reclaim")
		return
	}

	logx.Infof("Build cache: %s total, %s reclaimable", util.FormatBytes(total), util.FormatBytes(reclaimable))
	confirmed, promptErr := prompt.Confirm(
		"Prune Docker build cache? This affects all Docker images, not just enclave.",
		os.Stdin, os.Stdout,
	)
	if promptErr != nil {
		logx.Warnf("Failed to read confirmation: %v", promptErr)
		return
	}
	if !confirmed {
		logx.Infof("Build cache prune skipped")
		return
	}

	report, pruneErr := docker.BuildCachePrune(ctx, true)
	if pruneErr != nil {
		logx.Warnf("Failed to prune build cache: %v", pruneErr)
		return
	}
	logx.Successf("Reclaimed %s of build cache", util.FormatBytes(report.SpaceReclaimed))
}
