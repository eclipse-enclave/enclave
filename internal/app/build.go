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
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/devcontainer"
	"enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

type mergedExtensionFile struct {
	RelativePath string
	SourcePath   string
	Mode         os.FileMode
}

type runtimeImageSelection struct {
	Tools    []string
	Features []string
}

const agentUpdateStampUnknown = "unknown"

type agentUpdatePlan struct {
	Tools                    []string
	Stamps                   map[string]string
	ForceTools               map[string]bool
	PendingWrites            map[string]string
	PendingFingerprintWrites map[string]string
	NeedsRebuild             bool
}

type automaticToolUpdateResult struct {
	Fingerprint string
	Known       bool
	Changed     bool
}

type automaticToolUpdateResolver func(tool string) automaticToolUpdateResult

type toolFingerprintProbe func(paths model.Paths, buildCfg buildConfig, tool string) (string, bool, error)

type runtimeImageBuildPlan struct {
	CombinedHash      string
	StructuralRebuild bool
	AgentUpdates      agentUpdatePlan
}

func (p runtimeImageBuildPlan) NeedsRebuild() bool {
	return p.StructuralRebuild || p.AgentUpdates.NeedsRebuild
}

var (
	dockerBuildImage       = docker.Build
	dockerImageExists      = docker.ImageExists
	dockerImagePrune       = docker.ImagePrune
	dockerFindImageByLabel = docker.FindImageByLabel
	dockerTagImage         = docker.Tag
)

func ensureExistingRuntimeImage(imageName string) error {
	return ensureExistingRuntimeImageWith(imageName, docker.ImageExists)
}

func ensureExistingRuntimeImageWith(imageName string, exists func(context.Context, string) (bool, error)) error {
	ok, err := exists(context.Background(), imageName)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return fmt.Errorf("runtime image %q does not exist locally; rerun without --no-rebuild or pass --rebuild", imageName)
}

type imageInfo struct {
	Exists bool
	Labels map[string]string
}

// inspectImageInfo reports whether the image exists locally and its
// informational labels.
func inspectImageInfo(imageName string) imageInfo {
	info := imageInfo{Labels: map[string]string{}}
	if strings.TrimSpace(imageName) == "" {
		return info
	}
	inspect, err := docker.ImageInspect(context.Background(), imageName)
	if err != nil {
		return info
	}
	info.Exists = true
	if inspect.Config != nil && inspect.Config.Labels != nil {
		for _, key := range []string{model.LabelHash, model.LabelBuilt, model.LabelVersion} {
			if value := strings.TrimSpace(inspect.Config.Labels[key]); value != "" {
				info.Labels[key] = value
			}
		}
	}
	return info
}

var dockerPing = docker.Ping

// checkDocker distinguishes the common connectivity failures so users are not
// sent chasing a stopped daemon when the CLI is missing or socket access is
// denied.
func checkDocker() error {
	err := dockerPing(context.Background())
	switch {
	case err == nil:
		return nil
	case docker.IsCLIUnavailable(err):
		return fmt.Errorf("docker CLI not found on PATH; install Docker and retry")
	case docker.IsSocketPermissionDenied(err):
		return fmt.Errorf("cannot access the Docker socket: permission denied. Grant this user access to Docker (commonly by adding it to the docker group and logging in again; see https://docs.docker.com/engine/install/linux-postinstall/), then retry")
	default:
		return fmt.Errorf("docker daemon is not reachable: %w", err)
	}
}

func requireDocker() int {
	if err := checkDocker(); err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	return 0
}

// resolveWritableHome resolves the host home directory and fails if it is not
// writable. The writability guard and its message are shared by every command
// that needs to persist state under the home directory.
func resolveWritableHome() (string, error) {
	home, err := config.ResolveHostHome()
	if err != nil {
		return "", err
	}
	if !config.IsWritableDir(home) {
		return "", fmt.Errorf("home directory is not writable: %s (set HOME to a writable path)", home)
	}
	return home, nil
}

func resolveHost() (model.Host, error) {
	home, err := resolveWritableHome()
	if err != nil {
		return model.Host{}, err
	}
	current, err := user.Current()
	if err != nil {
		return model.Host{}, err
	}
	return model.Host{Home: home, UID: current.Uid, GID: current.Gid}, nil
}

func prepareBuildContext(paths model.Paths, selection runtimeImageSelection) (contextDir string, cleanup func(), err error) {
	if paths.UserExtensionsDir == "" && selection.Tools == nil && selection.Features == nil {
		return paths.AppRoot, func() {}, nil
	}

	stagingDir, cleanup, err := newStagingDir("enclave-build-context-*")
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	rootEntries, err := os.ReadDir(paths.AppRoot)
	if err != nil {
		return "", nil, err
	}
	for _, entry := range rootEntries {
		if entry.Name() == "extensions" {
			continue
		}
		src := filepath.Join(paths.AppRoot, entry.Name())
		dst := filepath.Join(stagingDir, entry.Name())
		if copyErr := util.CopyTree(src, dst, linkOrCopyFile); copyErr != nil {
			return "", nil, copyErr
		}
	}

	files, err := mergedExtensionFiles(paths, selection)
	if err != nil {
		return "", nil, err
	}
	for _, file := range files {
		target := filepath.Join(stagingDir, filepath.FromSlash(file.RelativePath))
		if copyErr := linkOrCopyFile(file.SourcePath, target, file.Mode.Perm()); copyErr != nil {
			return "", nil, copyErr
		}
	}

	return stagingDir, cleanup, nil
}

func linkOrCopyFile(src string, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst, mode)
}

func mergedExtensionFiles(paths model.Paths, selection runtimeImageSelection) ([]mergedExtensionFile, error) {
	effective := map[string]mergedExtensionFile{}
	skipTop := map[string]struct{}{"tools": {}, "features": {}}
	if err := overlayFilesFromDir(effective, "extensions", paths.ExtensionsDir, skipTop); err != nil {
		return nil, err
	}
	if err := overlayFilesFromDir(effective, "extensions", paths.UserExtensionsDir, skipTop); err != nil {
		return nil, err
	}

	selectedTools, err := effectiveToolExtensionNames(paths, selection.Tools)
	if err != nil {
		return nil, err
	}
	if err := overlayNamedExtensionDirs(effective, "extensions/tools", paths.ToolsDir, paths.UserToolsDir, selectedTools); err != nil {
		return nil, err
	}

	selectedFeatures, err := effectiveFeatureExtensionNames(paths, selection.Features)
	if err != nil {
		return nil, err
	}
	if err := overlayNamedExtensionDirs(effective, "extensions/features", paths.FeaturesDir, paths.UserFeaturesDir, selectedFeatures); err != nil {
		return nil, err
	}

	files := make([]mergedExtensionFile, 0, len(effective))
	for _, file := range effective {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	return files, nil
}

func effectiveToolExtensionNames(paths model.Paths, selected []string) ([]string, error) {
	if selected != nil {
		return normalizeAndSortNames(selected), nil
	}
	names, err := config.ListTools(paths)
	if err != nil {
		return nil, err
	}
	return normalizeAndSortNames(names), nil
}

func effectiveFeatureExtensionNames(paths model.Paths, selected []string) ([]string, error) {
	if selected != nil {
		return normalizeAndSortNames(selected), nil
	}
	features, err := config.ListFeatures(paths)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(features))
	for _, feature := range features {
		names = append(names, feature.Name)
	}
	return normalizeAndSortNames(names), nil
}

// normalizeAndSortNames trims each value, drops empties, de-duplicates, and
// returns the result sorted alphabetically.
func normalizeAndSortNames(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		if name := strings.TrimSpace(raw); name != "" {
			normalized = append(normalized, name)
		}
	}
	normalized = util.Dedupe(normalized)
	sort.Strings(normalized)
	return normalized
}

func overlayNamedExtensionDirs(files map[string]mergedExtensionFile, relRoot string, builtinRoot string, userRoot string, names []string) error {
	for _, name := range names {
		relPrefix := filepath.ToSlash(filepath.Join(relRoot, name))
		if err := overlayFilesFromDir(files, relPrefix, filepath.Join(builtinRoot, name), nil); err != nil {
			return err
		}
		if err := overlayFilesFromDir(files, relPrefix, filepath.Join(userRoot, name), nil); err != nil {
			return err
		}
	}
	return nil
}

func overlayFilesFromDir(files map[string]mergedExtensionFile, relPrefix string, rootDir string, skipTop map[string]struct{}) error {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return nil
	}
	info, err := os.Stat(rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	return filepath.WalkDir(rootDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == rootDir {
			return nil
		}
		if !util.PathWithin(rootDir, path) {
			return fmt.Errorf("overlay path %s is outside root %s", path, rootDir)
		}
		rel, err := filepath.Rel(rootDir, path)
		if err != nil {
			return err
		}
		if skipTop != nil {
			top := rel
			if idx := strings.IndexRune(rel, filepath.Separator); idx >= 0 {
				top = rel[:idx]
			}
			if _, skip := skipTop[top]; skip {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		stat, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !stat.Mode().IsRegular() {
			return nil
		}
		relPath := filepath.ToSlash(filepath.Join(relPrefix, rel))
		files[relPath] = mergedExtensionFile{
			RelativePath: relPath,
			SourcePath:   path,
			Mode:         stat.Mode(),
		}
		return nil
	})
}

func hashMergedExtensionFiles(paths model.Paths, selection runtimeImageSelection) (string, error) {
	files, err := mergedExtensionFiles(paths, selection)
	if err != nil {
		return "", err
	}
	return hashRuntimeInputFiles(files)
}

func needsRebuildForSelection(paths model.Paths, buildCfg buildConfig, selection runtimeImageSelection) (bool, string, error) {
	// Use empty stamps for hash computation - structure only, not update timestamps.
	// Actual stamps are embedded during buildImage for per-tool cache invalidation.
	featureInstalls, err := resolveFeatureInstalls(paths, selection.Features, false)
	if err != nil {
		return false, "", err
	}
	dockerfileContent, err := renderDockerfile(paths.Dockerfile, selection.Tools, featureInstalls, nil, nil)
	if err != nil {
		return false, "", err
	}
	dockerfileHash := util.HashString(dockerfileContent)
	entrypointHash, err := util.HashFile(paths.Entrypoint)
	if err != nil {
		return false, "", err
	}
	extensionsHash, err := hashMergedExtensionFiles(paths, selection)
	if err != nil {
		return false, "", err
	}
	staticHash, err := hashRuntimeImageStaticFiles(paths)
	if err != nil {
		return false, "", err
	}
	combinedHash := dockerfileHash + "-" + entrypointHash + "-" + extensionsHash + "-" + staticHash
	if buildCfg.HashSuffix != "" {
		combinedHash += "-" + buildCfg.HashSuffix
	}

	inspect, err := docker.ImageInspect(context.Background(), buildCfg.ImageName)
	if err != nil {
		return true, combinedHash, nil
	}
	storedHash := ""
	if inspect.Config != nil && inspect.Config.Labels != nil {
		storedHash = inspect.Config.Labels[model.LabelHash]
	}
	if strings.TrimSpace(storedHash) != combinedHash {
		return true, combinedHash, nil
	}

	return false, combinedHash, nil
}

func resolveRuntimeImageBuildPlan(paths model.Paths, buildCfg buildConfig, opts model.BuildOptions, tool string, home string, forceAll bool, now time.Time) (runtimeImageBuildPlan, error) {
	selection, err := resolveRuntimeImageSelection(paths, opts, tool)
	if err != nil {
		return runtimeImageBuildPlan{}, err
	}
	structuralRebuild, combinedHash, err := needsRebuildForSelection(paths, buildCfg, selection)
	if err != nil {
		return runtimeImageBuildPlan{}, err
	}
	resolver := func(tool string) automaticToolUpdateResult {
		return resolveAutomaticToolUpdate(paths, buildCfg, home, tool, nil)
	}
	agentUpdates, err := planAgentUpdatesForTools(forceAll, selection.Tools, home, now, resolver)
	if err != nil {
		return runtimeImageBuildPlan{}, err
	}
	return runtimeImageBuildPlan{
		CombinedHash:      combinedHash,
		StructuralRebuild: structuralRebuild,
		AgentUpdates:      agentUpdates,
	}, nil
}

func buildImage(ctx context.Context, paths model.Paths, host model.Host, combinedHash string, buildCfg buildConfig, opts model.BuildOptions, tool string, updates agentUpdatePlan) error {
	if ctx == nil {
		ctx = context.Background()
	}
	logx.Infof("Building %s image (%s). This may take a few minutes on first run.", model.AppName, buildCfg.ImageName)
	if err := checkRuntimeImageBuildPreflight(ctx); err != nil {
		return err
	}
	warnAppRootModeIssues(paths.AppRoot)

	selection, err := resolveRuntimeImageSelection(paths, opts, tool)
	if err != nil {
		return err
	}
	contextDir, cleanupContext, err := prepareBuildContext(paths, selection)
	if err != nil {
		return err
	}
	defer cleanupContext()

	// This preflight only runs when a rebuild is already required; no-op runs
	// return earlier in runExecutionCommand after needsRebuild.
	if err := validateDevcontainerImageModeBase(ctx, buildCfg, opts); err != nil {
		return err
	}

	// Pass stamps to embed in Dockerfile for per-tool cache invalidation.
	// When a stamp changes, only that tool's RUN instruction is invalidated.
	featureInstalls, err := resolveFeatureInstalls(paths, selection.Features, true)
	if err != nil {
		return err
	}
	dockerfileContent, err := renderDockerfile(paths.Dockerfile, updates.Tools, featureInstalls, updates.Stamps, updates.ForceTools)
	if err != nil {
		return err
	}
	dockerfilePath := filepath.ToSlash(filepath.Join(".enclave", "Dockerfile.generated"))

	if buildCfg.Devcontainer != nil && buildCfg.Devcontainer.DockerfilePath != "" {
		if err := devcontainer.BuildImage(*buildCfg.Devcontainer); err != nil {
			return err
		}
	}

	buildTimestamp := time.Now().UTC().Format(time.RFC3339)

	agentTools := resolveAgentToolsArg(tool)
	features := resolveFeaturesArg(opts)
	logDevcontainerFeatureSelection(opts, features)

	cacheFrom := resolveCacheFrom(opts, buildCfg)
	username := model.ContainerUser
	if opts.UseRemoteUser && buildCfg.Devcontainer != nil {
		if ru := strings.TrimSpace(buildCfg.Devcontainer.RuntimeConfig.RemoteUser); ru != "" {
			username = ru
		}
	}
	buildUID, buildGID := effectiveBuildIdentity(host, opts)
	buildArgs := map[string]string{
		"USER_ID":     buildUID,
		"GROUP_ID":    buildGID,
		"USERNAME":    username,
		"AGENT_TOOLS": agentTools,
		"FEATURES":    features,
	}
	if docker.BuildkitEnabled() {
		buildArgs["BUILDKIT_INLINE_CACHE"] = "1"
	}
	if buildCfg.BaseImage != "" {
		buildArgs["BASE_IMAGE"] = buildCfg.BaseImage
	}
	if buildCfg.Devcontainer != nil {
		buildArgs["DEVCONTAINER_BASE_IMAGE"] = "1"
	}
	labels := map[string]string{
		model.LabelHash:    combinedHash,
		model.LabelVersion: model.Version,
		model.LabelBuilt:   buildTimestamp,
	}

	buildxCacheTo, err := resolveBuildxCacheTo(opts)
	if err != nil {
		return fmt.Errorf("prepare buildx cache directory: %w", err)
	}

	req := docker.BuildRequest{
		ContextDir:        contextDir,
		Dockerfile:        dockerfilePath,
		DockerfileContent: []byte(dockerfileContent),
		Tags:              []string{buildCfg.ImageName},
		Target:            dockerTarget(buildCfg.Target),
		BuildArgs:         buildArgs,
		Labels:            labels,
		CacheFrom:         cacheFrom,
		BuildxCacheFrom:   resolveBuildxCacheFrom(opts),
		BuildxCacheTo:     buildxCacheTo,
		Progress:          opts.Progress,
	}
	if err := dockerBuildImage(ctx, req, os.Stdout); err != nil {
		// Some Docker BuildKit setups fail DNS resolution in the default build
		// network during apt installs. Retry once with host build network.
		req.NetworkMode = "host"
		if retryErr := dockerBuildImage(ctx, req, os.Stdout); retryErr != nil {
			return fmt.Errorf("failed to build image: %w (retry with host build network failed: %v)", err, retryErr)
		}
		logx.Warnf("Image build failed on default build network; retry with host build network succeeded")
	}

	if err := backfillMissingAgentUpdateFingerprints(&updates, paths, buildCfg, host.Home, nil); err != nil {
		return err
	}
	if err := commitAgentUpdatePlan(updates, host.Home); err != nil {
		return err
	}

	logx.Successf("Image built successfully")
	_, _ = dockerImagePrune(ctx, model.LabelVersion)
	return nil
}

func validateDevcontainerImageModeBase(ctx context.Context, buildCfg buildConfig, opts model.BuildOptions) error {
	// Dockerfile-mode devcontainers define their own base and are validated by their build.
	if buildCfg.Devcontainer == nil || strings.TrimSpace(buildCfg.Devcontainer.Image) == "" {
		return nil
	}
	if opts.ForceBaseImage {
		return nil
	}

	image := strings.TrimSpace(buildCfg.BaseImage)
	if image == "" {
		return nil
	}
	if err := docker.EnsureImage(ctx, image); err != nil {
		return fmt.Errorf("failed to pull devcontainer image %q for compatibility check: %w", image, err)
	}

	_, err := docker.RunCapture(ctx, &docker.ContainerConfig{
		Image: image,
		Cmd:   []string{"sh", "-lc", "command -v apt-get >/dev/null 2>&1 || command -v apt >/dev/null 2>&1"},
	}, &docker.HostConfig{
		AutoRemove: true,
	}, "")
	if err == nil {
		return nil
	}

	return fmt.Errorf("devcontainer image %q is incompatible: apt-get was not found during preflight check. enclave requires a Debian/Ubuntu base image. Use a Debian-based variant (for example \"node:22-bookworm\") or use build.dockerfile to provide a compatible base. Use --force-base-image to bypass this check", image)
}

func resolveCacheFrom(opts model.BuildOptions, config buildConfig) []string {
	seen := map[string]struct{}{}
	var images []string
	for _, image := range opts.CacheFrom {
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		images = append(images, image)
	}

	// Images are per-tool, so the meaningful auto cache source is the current
	// image's own prior build.
	auto := []string{config.ImageName}
	for _, image := range auto {
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		if imageExists(image) {
			seen[image] = struct{}{}
			images = append(images, image)
		}
	}
	return images
}

func imageExists(image string) bool {
	if strings.TrimSpace(image) == "" {
		return false
	}
	ok, err := dockerImageExists(context.Background(), image)
	if err != nil {
		return false
	}
	return ok
}

// resolveAgentToolsArg returns the AGENT_TOOLS build arg. Images are always
// per-tool, so this is the single resolved tool.
func resolveAgentToolsArg(tool string) string {
	return strings.TrimSpace(tool)
}

func resolveFeaturesArg(opts model.BuildOptions) string {
	if opts.Slim {
		return "" // --slim means no features
	}
	if opts.Features != nil {
		normalized := normalizeAndSortNames(opts.Features)
		return strings.Join(normalized, " ")
	}
	if opts.Devcontainer {
		return "" // devcontainer provides its own environment
	}
	return model.SelectionDefault
}

func resolveRuntimeImageSelection(paths model.Paths, opts model.BuildOptions, tool string) (runtimeImageSelection, error) {
	selection := runtimeImageSelection{
		Tools:    []string{},
		Features: []string{},
	}

	// Images are always per-tool: the selection is exactly the resolved tool.
	if name := strings.TrimSpace(tool); name != "" {
		selection.Tools = []string{name}
	}

	if opts.Slim || (opts.Devcontainer && opts.Features == nil) {
		return selection, nil
	}

	availableFeatures, err := config.ListFeatures(paths)
	if err != nil {
		return runtimeImageSelection{}, err
	}
	if opts.Features != nil {
		selection.Features = resolveConfiguredFeatures(opts.Features, availableFeatures)
	} else {
		selection.Features = defaultEnabledFeatureNames(availableFeatures)
	}
	return selection, nil
}

// resolveFeatureInstalls turns the selected feature names into ordered
// per-feature install descriptors for Dockerfile generation. Metadata (priority,
// apt packages, root requirement) comes from each feature's manifest; HasScript
// reflects an executable install.sh resolved with user-override precedence.
func resolveFeatureInstalls(paths model.Paths, selected []string, warnConflicts bool) ([]featureInstall, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	available, err := config.ListFeatures(paths)
	if err != nil {
		return nil, err
	}
	meta := make(map[string]model.Extension, len(available))
	for _, ext := range available {
		meta[ext.Name] = ext
	}
	installs := make([]featureInstall, 0, len(selected))
	for _, raw := range selected {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		ext, ok := meta[name]
		if !ok {
			// Selected feature is unavailable; the build context omits it too.
			continue
		}
		hasScript := featureHasExecutableInstall(paths, name)
		hasInstallCmds := len(ext.InstallCommandUsers) > 0
		if hasInstallCmds && hasScript && warnConflicts {
			// install.sh is the escape hatch and wins; the declarative
			// commands.install steps are ignored to avoid double-installing.
			// Gated on warnConflicts so this fires once per build (from the
			// buildImage call site) rather than also from the rebuild-hash
			// probe (needsRebuildForSelection), which would double-log it.
			logx.Warnf("feature %q ships both install.sh and commands.install; install.sh wins, commands.install ignored", name)
		}
		installs = append(installs, featureInstall{
			Name:                    name,
			Priority:                ext.Priority,
			HasApt:                  len(ext.AptPackages) > 0,
			HasScript:               hasScript,
			NeedsRoot:               ext.NeedsRoot,
			HasInstallCommands:      hasInstallCmds && !hasScript,
			InstallCommandsNeedRoot: anyRootInstallUser(ext.InstallCommandUsers),
		})
	}
	return installs, nil
}

// anyRootInstallUser reports whether any commands.install entry targets root,
// which forces the feature's declarative install into the root build phase.
func anyRootInstallUser(users []string) bool {
	for _, u := range users {
		if u == "0" || u == "root" {
			return true
		}
	}
	return false
}

// featureHasExecutableInstall reports whether the feature has an install.sh
// that should be run. Built-in install scripts are made executable in the
// generated Dockerfile so a damaged installed app-root mode cannot silently
// skip them; user extension scripts still have to opt in with an execute bit.
func featureHasExecutableInstall(paths model.Paths, name string) bool {
	path, ok := config.ResolveFeatureFile(paths, name, model.InstallScriptFilename)
	if !ok {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if isBuiltinAppRootPath(paths, path) {
		return true
	}
	return info.Mode()&0o111 != 0
}

func isBuiltinAppRootPath(paths model.Paths, path string) bool {
	if strings.TrimSpace(paths.AppRoot) == "" {
		return false
	}
	builtinRoot := filepath.Join(paths.AppRoot, "extensions")
	return util.PathWithin(builtinRoot, path)
}

// reuseRuntimeImageByContentHash retags an existing local image that already
// carries the target content hash, avoiding a rebuild when the build inputs are
// byte-for-byte identical (for example the same selection on a different branch
// tag). It reports whether an image was reused.
func reuseRuntimeImageByContentHash(ctx context.Context, targetImage string, combinedHash string) (bool, error) {
	if strings.TrimSpace(combinedHash) == "" || strings.TrimSpace(targetImage) == "" {
		return false, nil
	}
	ref, found, err := dockerFindImageByLabel(ctx, model.LabelHash, combinedHash)
	if err != nil {
		return false, err
	}
	if !found || ref == targetImage {
		return false, nil
	}
	if err := dockerTagImage(ctx, ref, targetImage); err != nil {
		return false, err
	}
	logx.Successf("Reused existing image with identical build inputs (tagged %s from %s); skipping rebuild.", targetImage, ref)
	return true, nil
}

func logDevcontainerFeatureSelection(opts model.BuildOptions, features string) {
	if !opts.Devcontainer {
		return
	}
	logx.Infof("%s", devcontainerFeatureSelectionMessage(features))
}

func devcontainerFeatureSelectionMessage(features string) string {
	if strings.TrimSpace(features) == "" {
		return "Devcontainer mode: no enclave features enabled (use --features to add)."
	}
	return fmt.Sprintf("Devcontainer mode: features enabled: %s", strings.ReplaceAll(features, " ", ", "))
}

// planAgentUpdatesForTools decides, per tool, whether the agent CLI install
// must be refreshed. With forceAll set (the `update` command), every tool is
// force-updated; otherwise each tool is refreshed only when its update interval
// has elapsed and an online probe reports a changed upstream fingerprint.
func planAgentUpdatesForTools(forceAll bool, tools []string, home string, now time.Time, resolveAutomatic automaticToolUpdateResolver) (agentUpdatePlan, error) {
	interval := resolveAgentUpdateInterval()
	stampDir := config.HostBuildDir(home)
	plan := agentUpdatePlan{
		Stamps:                   make(map[string]string, len(tools)),
		ForceTools:               make(map[string]bool),
		PendingWrites:            make(map[string]string),
		PendingFingerprintWrites: make(map[string]string),
	}

	for _, tool := range tools {
		name := strings.TrimSpace(strings.ToLower(tool))
		if name == "" {
			continue
		}
		plan.Tools = append(plan.Tools, name)
		if forceAll {
			queueForcedAgentUpdate(&plan, name, now)
			continue
		}
		stampFile := agentUpdateStampFile(stampDir, name)
		state, err := resolveAgentUpdateStateForTool(now, interval, stampFile)
		if err != nil {
			return agentUpdatePlan{}, err
		}
		plan.Stamps[name] = state.CurrentStamp
		if !state.Due {
			continue
		}
		if resolveAutomatic == nil {
			continue
		}
		update := resolveAutomatic(name)
		if update.Known && update.Changed {
			stamp := formatAgentUpdateStamp(now)
			// Force the install so it fetches online metadata. A bare
			// rebuild reinstalls with npm_config_prefer_offline=true and
			// would resolve @latest from the stale cache, never applying
			// the update the hook just detected.
			queueAgentUpdate(&plan, name, stamp, true)
			plan.PendingFingerprintWrites[name] = update.Fingerprint
		}
	}
	return plan, nil
}

func resolveAgentUpdateInterval() int {
	interval := 24
	if raw := os.Getenv(model.EnvAgentUpdateIntervalHours); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			logx.Warnf("Invalid %s: %s. Defaulting to 24.", model.EnvAgentUpdateIntervalHours, raw)
		} else {
			interval = parsed
		}
	}
	return interval
}

type agentUpdateState struct {
	CurrentStamp string
	Due          bool
}

func resolveAgentUpdateStateForTool(now time.Time, interval int, stampFile string) (agentUpdateState, error) {
	currentStamp, ok, err := readAgentUpdateStateValue(stampFile)
	if err != nil {
		return agentUpdateState{}, err
	}
	if !ok {
		currentStamp = agentUpdateStampUnknown
	}
	if interval == 0 {
		return agentUpdateState{CurrentStamp: currentStamp, Due: true}, nil
	}

	info, err := os.Stat(stampFile)
	if err != nil {
		if os.IsNotExist(err) {
			return agentUpdateState{CurrentStamp: currentStamp, Due: true}, nil
		}
		return agentUpdateState{}, err
	}
	due := now.Sub(info.ModTime()) >= time.Duration(interval)*time.Hour
	return agentUpdateState{CurrentStamp: currentStamp, Due: due}, nil
}

func commitAgentUpdatePlan(plan agentUpdatePlan, home string) error {
	if len(plan.PendingWrites) == 0 && len(plan.PendingFingerprintWrites) == 0 {
		return nil
	}
	stampDir := config.HostBuildDir(home)
	tools := make([]string, 0, len(plan.PendingWrites)+len(plan.PendingFingerprintWrites))
	seen := map[string]struct{}{}
	for tool := range plan.PendingWrites {
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		tools = append(tools, tool)
	}
	for tool := range plan.PendingFingerprintWrites {
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	for _, tool := range tools {
		if stamp, ok := plan.PendingWrites[tool]; ok {
			if err := writeAgentUpdateStateValue(stampDir, agentUpdateStampFile(stampDir, tool), stamp); err != nil {
				return err
			}
		}
		if fingerprint, ok := plan.PendingFingerprintWrites[tool]; ok {
			if err := writeAgentUpdateStateValue(stampDir, agentUpdateFingerprintFile(stampDir, tool), fingerprint); err != nil {
				return err
			}
		}
	}
	return nil
}

func queueForcedAgentUpdate(plan *agentUpdatePlan, tool string, now time.Time) {
	queueAgentUpdate(plan, tool, formatAgentUpdateStamp(now), true)
}

func queueAgentUpdate(plan *agentUpdatePlan, tool string, stamp string, force bool) {
	plan.Stamps[tool] = stamp
	if force {
		plan.ForceTools[tool] = true
	}
	plan.PendingWrites[tool] = stamp
	plan.NeedsRebuild = true
}

func resolveAutomaticToolUpdate(paths model.Paths, buildCfg buildConfig, home string, tool string, probe toolFingerprintProbe) automaticToolUpdateResult {
	if _, found := config.ResolveToolFile(paths, tool, model.CheckUpdateScriptFilename); !found {
		return automaticToolUpdateResult{}
	}
	if probe == nil {
		probe = probeToolUpdateFingerprint
	}
	fingerprint, known, err := probe(paths, buildCfg, tool)
	if err != nil {
		logx.Debugf("check-update probe failed for %s: %v", tool, err)
		return automaticToolUpdateResult{}
	}
	if !known {
		return automaticToolUpdateResult{}
	}
	existing, ok, err := readAgentUpdateStateValue(agentUpdateFingerprintFile(config.HostBuildDir(home), tool))
	if err != nil {
		logx.Debugf("failed to read stored update fingerprint for %s: %v", tool, err)
		return automaticToolUpdateResult{}
	}
	return automaticToolUpdateResult{
		Fingerprint: fingerprint,
		Known:       true,
		Changed:     !ok || existing != fingerprint,
	}
}

func backfillMissingAgentUpdateFingerprints(plan *agentUpdatePlan, paths model.Paths, buildCfg buildConfig, home string, probe toolFingerprintProbe) error {
	if plan == nil {
		return nil
	}
	if probe == nil {
		probe = probeToolUpdateFingerprint
	}
	if plan.PendingFingerprintWrites == nil {
		plan.PendingFingerprintWrites = make(map[string]string)
	}
	stampDir := config.HostBuildDir(home)
	for _, tool := range plan.Tools {
		if _, ok := plan.PendingFingerprintWrites[tool]; ok {
			continue
		}
		if _, found := config.ResolveToolFile(paths, tool, model.CheckUpdateScriptFilename); !found {
			continue
		}
		if _, ok, err := readAgentUpdateStateValue(agentUpdateFingerprintFile(stampDir, tool)); err != nil {
			return err
		} else if ok {
			continue
		}
		fingerprint, known, err := probe(paths, buildCfg, tool)
		if err != nil {
			logx.Debugf("post-build check-update probe failed for %s: %v", tool, err)
			continue
		}
		if !known {
			continue
		}
		plan.PendingFingerprintWrites[tool] = fingerprint
	}
	return nil
}

func probeToolUpdateFingerprint(paths model.Paths, buildCfg buildConfig, tool string) (string, bool, error) {
	toolDir, cleanup, err := prepareToolProbeDir(paths, tool)
	if err != nil {
		return "", false, err
	}
	defer cleanup()

	containerToolDir := filepath.ToSlash(filepath.Join("/opt/enclave/extensions/tools", tool))
	mounts := []docker.Mount{{
		Type:     docker.MountTypeBind,
		Source:   toolDir,
		Target:   containerToolDir,
		ReadOnly: true,
	}}
	binds, remaining := docker.SplitMountsForSELinux(mounts)
	output, err := docker.RunCapture(context.Background(), &docker.ContainerConfig{
		Image:      buildCfg.ImageName,
		User:       model.ContainerUser,
		WorkingDir: containerToolDir,
		Env: []string{
			"HOME=" + model.ContainerHome,
			"ENCLAVE_EXTENSIONS_ROOT=/opt/enclave/extensions",
			"ENCLAVE_AGENT_NODE_DIR=/opt/enclave/node",
			"ENCLAVE_AGENT_NODE_BIN=/opt/enclave/node/bin/node",
			"ENCLAVE_AGENT_NPM_BIN=/opt/enclave/node/bin/npm",
			"PATH=/opt/enclave/node/bin:" + model.ContainerHome + "/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		},
		Cmd: []string{"bash", "-lc", "bash ./" + model.CheckUpdateScriptFilename},
	}, &docker.HostConfig{
		AutoRemove: true,
		Binds:      binds,
		Mounts:     remaining,
	}, "")
	if err != nil {
		return "", false, err
	}
	fingerprint := strings.TrimSpace(output)
	if fingerprint == "" {
		return "", false, nil
	}
	return fingerprint, true, nil
}

func prepareToolProbeDir(paths model.Paths, tool string) (dir string, cleanup func(), err error) {
	builtinDir, userDir := config.ResolveToolDirs(paths, tool)
	switch {
	case builtinDir != "" && userDir == "":
		return builtinDir, func() {}, nil
	case builtinDir == "" && userDir != "":
		return userDir, func() {}, nil
	case builtinDir == "" && userDir == "":
		return "", nil, fmt.Errorf("tool extension not found: %s", tool)
	}

	stagingDir, cleanup, err := newStagingDir("enclave-tool-probe-*")
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if err != nil && cleanup != nil {
			cleanup()
		}
	}()

	if copyErr := util.CopyTree(builtinDir, stagingDir, linkOrCopyFile); copyErr != nil {
		return "", nil, copyErr
	}
	if copyErr := util.CopyTree(userDir, stagingDir, linkOrCopyFile); copyErr != nil {
		return "", nil, copyErr
	}
	return stagingDir, cleanup, nil
}

func newStagingDir(prefix string) (string, func(), error) {
	stagingDir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(stagingDir) }
	return stagingDir, cleanup, nil
}

func agentUpdateStampFile(stampDir string, tool string) string {
	return filepath.Join(stampDir, "agent-update-"+tool)
}

func agentUpdateFingerprintFile(stampDir string, tool string) string {
	return filepath.Join(stampDir, "agent-update-fingerprint-"+tool)
}

func formatAgentUpdateStamp(now time.Time) string {
	return now.UTC().Format(time.RFC3339)
}

func readAgentUpdateStateValue(stateFile string) (string, bool, error) {
	// #nosec G304 -- stateFile is under enclave-managed state directories.
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", false, nil
	}
	return value, true, nil
}

func writeAgentUpdateStateValue(stateDir string, stateFile string, value string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(stateFile, []byte(value+"\n"), 0o600); err != nil {
		return err
	}
	return nil
}
