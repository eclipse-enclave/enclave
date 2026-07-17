// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/config"
	"enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/prompt"
)

// persistentConfigStoreKey is the config-store key for the persistent ("kept")
// store; every other key denotes an ephemeral session/worktree store. It
// mirrors defaultStoreKey in internal/backend/docker.
const persistentConfigStoreKey = "default"

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

	if cleanup.CleanupEphemeral {
		containerNames, containersErr := resolveEphemeralContainers(run, cleanup, project)
		if containersErr != nil {
			logx.Errorf("Failed to list containers: %v", containersErr)
			return 1
		}
		// Ephemeral config stores are host directories keyed by a session or
		// worktree suffix.
		storeDirs := resolveEphemeralStoreDirs(run, cleanup, home, project)
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

	dirPaths := cleanupDirsForRemoval(run, cleanup, home, project)

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
func resolveEphemeralStoreDirs(run model.RunOptions, cleanup model.CleanupOptions, home string, project model.Project) []cleanupDir {
	var hashes []string
	if cleanup.CleanupAll {
		hashes = listSubdirs(config.HostProjectsDir(home))
	} else {
		if project.Hash == "" {
			return nil
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
			for _, key := range listSubdirs(storeRoot) {
				if key == persistentConfigStoreKey {
					continue
				}
				dirs = append(dirs, cleanupDir{Kind: "ephemeral", Path: filepath.Join(storeRoot, key)})
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Path < dirs[j].Path })
	return dirs
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
			{Kind: "cache", Path: config.HostCacheDir(home)},
			// The state projects tree holds every project's config/env stores
			// and history, so a full cleanup removes them all at once.
			{Kind: "history", Path: config.HostProjectsDir(home)},
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
		{Kind: "cache", Path: config.HostCacheToolProjectDir(home, run.Tool, project.Hash)},
		{Kind: "history", Path: filepath.Join(projectDataDir, "history")},
		{Kind: "history", Path: config.HostProjectHomeConfigDir(home, project.Hash, run.Tool)},
		{Kind: "history", Path: config.HostProjectGeneratedConfigDir(home, project.Hash, run.Tool)},
		{Kind: "history", Path: filepath.Join(projectDataDir, model.GeneratedSkillsDirName)},
		{Kind: "history", Path: config.HostStoreConfigRootDir(home, run.Tool, project.Hash)},
		{Kind: "history", Path: config.HostStoreEnvDir(home, run.Tool, project.Hash)},
		{Kind: "memory", Path: config.HostProjectMemoryDir(home, project.Hash, run.Tool)},
	}
}

// authStoreCleanupDirs enumerates the shared tool and feature auth store
// directories on the host. They are project-independent, so they only
// participate in a full (--all) cleanup and are gated by `--keep auth`.
func authStoreCleanupDirs(home string) []cleanupDir {
	var dirs []cleanupDir
	for _, tool := range listSubdirs(config.HostStoreAuthRootDir(home)) {
		dirs = append(dirs, cleanupDir{Kind: "auth", Path: config.HostStoreAuthTreeDir(home, tool)})
	}
	for _, feature := range listSubdirs(config.HostStoreFeatureAuthRootDir(home)) {
		dirs = append(dirs, cleanupDir{Kind: "auth", Path: config.HostStoreFeatureAuthDir(home, feature)})
	}
	return dirs
}

// cleanupDirsForRemoval resolves the host directories to remove for the given
// cleanup options, applying the keep-* gating. Each host-dir kind is preserved
// when its matching --keep-* flag is set.
func cleanupDirsForRemoval(run model.RunOptions, cleanup model.CleanupOptions, home string, project model.Project) []cleanupDir {
	dirs := resolveCleanupDirs(run, cleanup, home, project)
	if cleanup.CleanupKeepCache {
		dirs = filterDirs(dirs, "cache")
	}
	if cleanup.CleanupKeepHist {
		dirs = filterDirs(dirs, "history")
	}
	if cleanup.CleanupKeepMemory {
		dirs = filterDirs(dirs, "memory")
	}
	if cleanup.CleanupKeepAuth {
		dirs = filterDirs(dirs, "auth")
	}
	return dirs
}

func filterDirs(dirs []cleanupDir, kind string) []cleanupDir {
	var filtered []cleanupDir
	for _, dir := range dirs {
		if dir.Kind == kind {
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
		logx.Infof("Build cache: %s total, %s reclaimable", formatBytes(total), formatBytes(reclaimable))
		return
	}

	if reclaimable == 0 {
		logx.Infof("Build cache: nothing to reclaim")
		return
	}

	logx.Infof("Build cache: %s total, %s reclaimable", formatBytes(total), formatBytes(reclaimable))
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
	logx.Successf("Reclaimed %s of build cache", formatBytes(report.SpaceReclaimed))
}

func formatBytes(b uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
