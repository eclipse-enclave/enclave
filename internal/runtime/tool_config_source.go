// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

func (r *Runtime) prepareToolConfigSource() error {
	if strings.TrimSpace(r.profile.ConfigDir) == "" {
		patchDirs := []string{
			config.HostToolPatchesDir(r.host.Home, r.profile.Name),
			config.HostProjectToolPatchesDir(r.host.Home, r.project.Hash, r.profile.Name),
		}
		for _, patchDir := range patchDirs {
			if hasConfigPatchFiles(patchDir) {
				return fmt.Errorf("config patches in %q require config_dir for %s", patchDir, r.profile.Name)
			}
		}
		return nil
	}
	if !r.shouldMountToolConfigSource() {
		return nil
	}

	globalConfigDir := config.HostToolConfigDir(r.host.Home, r.profile.Name)
	projectConfigDir := config.HostProjectConfigDir(r.host.Home, r.project.Hash, r.profile.Name)
	projectToolDir := config.HostProjectToolDir(r.host.Home, r.project.Hash, r.profile.Name)
	generatedConfigDir := r.generatedConfigSourceDir()
	for _, dir := range []string{globalConfigDir, projectConfigDir, generatedConfigDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create config directory %q: %w", dir, err)
		}
	}

	if err := r.withToolDataLock(projectToolDir, "config-source", func() error {
		return r.composeGeneratedConfigDir(generatedConfigDir, globalConfigDir, projectConfigDir)
	}); err != nil {
		return fmt.Errorf("prepare config source for %s: %w", r.profile.Name, err)
	}

	r.configSourceDir = generatedConfigDir
	logx.Infof("%s config source prepared", util.TitleCase(r.profile.Name))
	return nil
}

func (r *Runtime) generatedConfigSourceDir() string {
	root := config.HostProjectGeneratedConfigDir(r.host.Home, r.project.Hash, r.profile.Name)
	key := defaultConfigKey
	if r.configVolReady && strings.TrimSpace(r.configVolSuffix) != "" {
		key = strings.TrimSpace(r.configVolSuffix)
	}
	return filepath.Join(root, key)
}

// configSourcePreservePaths lists the store-relative paths that must survive
// the config overlay (session state such as auth files). The overlay itself is
// backend-owned; this is the policy input it receives.
func (r *Runtime) configSourcePreservePaths() []string {
	return ConfigSourcePreservePaths(r.profile)
}

// ConfigSourcePreservePaths is the preserve-path policy for the config-source
// overlay of the given profile. Exported so backend tests can exercise the
// overlay mechanics against the real policy.
func ConfigSourcePreservePaths(profile model.Profile) []string {
	raw := append([]string{".enclave-devcontainer/"}, config.HostConfigPassthroughDeniedPaths(profile)...)
	seen := make(map[string]struct{}, len(raw))
	result := make([]string, 0, len(raw))
	for _, entry := range raw {
		normalized := normalizeConfigSourcePreservePath(entry)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}

func normalizeConfigSourcePreservePath(raw string) string {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if value == "" {
		return ""
	}
	isDir := strings.HasSuffix(value, "/")
	value = strings.TrimPrefix(value, "/")
	value = path.Clean(value)
	if value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return ""
	}
	if isDir && !strings.HasSuffix(value, "/") {
		value += "/"
	}
	return value
}

func configSourceChownSpec(uid string, gid string) string {
	return util.ChownSpec(uid, gid)
}

func (r *Runtime) shouldMountToolConfigSource() bool {
	if strings.TrimSpace(r.profile.ConfigDir) == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.run.HostConfig), model.HostConfigPassthrough) {
		return true
	}
	if util.PathExists(filepath.Join(r.paths.ToolsDir, r.profile.Name, "config-base")) {
		return true
	}
	if util.DirHasFiles(config.HostToolConfigDir(r.host.Home, r.profile.Name)) {
		return true
	}
	if util.DirHasFiles(config.HostProjectConfigDir(r.host.Home, r.project.Hash, r.profile.Name)) {
		return true
	}
	if hasConfigPatchFiles(config.HostToolPatchesDir(r.host.Home, r.profile.Name)) {
		return true
	}
	return hasConfigPatchFiles(config.HostProjectToolPatchesDir(r.host.Home, r.project.Hash, r.profile.Name))
}

func (r *Runtime) toolConfigSourceHandlesSkills() bool {
	return r.configSourceDir != "" && strings.TrimSpace(r.profile.SkillsDir) != ""
}

func (r *Runtime) composeGeneratedConfigDir(targetDir string, globalConfigDir string, projectConfigDir string) error {
	if err := clearDirectory(targetDir); err != nil {
		return fmt.Errorf("clear generated config directory %q: %w", targetDir, err)
	}
	if err := r.applyBuiltInConfigLayer(targetDir); err != nil {
		return err
	}
	if err := r.applyHostConfigLayer(targetDir); err != nil {
		return err
	}
	if err := r.applySharedSkillsLayer(targetDir, false); err != nil {
		return err
	}
	if err := r.applyConfigOverrideScope(targetDir, globalConfigDir, false); err != nil {
		return err
	}
	if err := r.applySharedSkillsLayer(targetDir, true); err != nil {
		return err
	}
	return r.applyConfigOverrideScope(targetDir, projectConfigDir, true)
}

func (r *Runtime) applyBuiltInConfigLayer(targetDir string) error {
	baseDir := filepath.Join(r.paths.ToolsDir, r.profile.Name, "config-base")
	if util.PathExists(baseDir) {
		if err := overlayConfigSource(targetDir, baseDir, nil, nil, nil); err != nil {
			return fmt.Errorf("overlay built-in config base from %q: %w", baseDir, err)
		}
	}

	relativeSettingsPath, err := r.settingsRelativePath()
	if err != nil {
		return fmt.Errorf("resolve settings target: %w", err)
	}
	if relativeSettingsPath != "" {
		sourcePath, err := r.builtInSettingsTemplatePath()
		if err != nil {
			return err
		}
		if err := copyConfigFile(sourcePath, filepath.Join(targetDir, relativeSettingsPath)); err != nil {
			return fmt.Errorf("copy built-in settings template to %q: %w", relativeSettingsPath, err)
		}
	}
	if err := r.applyBuiltInSkillsLayer(targetDir); err != nil {
		return err
	}
	return nil
}

func (r *Runtime) builtInSettingsTemplatePath() (string, error) {
	prefix := r.profile.Name + "-"
	if !strings.HasPrefix(r.profile.SettingsFile, prefix) {
		return "", fmt.Errorf("settings_file %q must start with %q", r.profile.SettingsFile, prefix)
	}
	templateName := strings.TrimPrefix(r.profile.SettingsFile, prefix)
	hostTemplatePath := filepath.Join(r.paths.ToolsDir, r.profile.Name, model.TemplatesDir, templateName)
	if !util.PathExists(hostTemplatePath) {
		return "", fmt.Errorf("built-in settings template missing at %s", hostTemplatePath)
	}
	return hostTemplatePath, nil
}

func (r *Runtime) applyBuiltInSkillsLayer(targetDir string) error {
	if strings.TrimSpace(r.profile.SkillsDir) == "" {
		return nil
	}

	sourceDirs := r.extensionSkillSourceDirs()
	if len(sourceDirs) == 0 {
		return nil
	}

	relativeSkillsDir, err := r.relativePathWithinConfig(r.profile.SkillsDir)
	if err != nil {
		return fmt.Errorf("resolve skills path: %w", err)
	}
	if relativeSkillsDir == "." || relativeSkillsDir == "" {
		return nil
	}

	skillsTargetDir := filepath.Join(targetDir, relativeSkillsDir)
	if err := os.MkdirAll(skillsTargetDir, 0o700); err != nil {
		return fmt.Errorf("create generated skills directory %q: %w", skillsTargetDir, err)
	}
	for _, sourceDir := range sourceDirs {
		if err := overlaySkillSource(skillsTargetDir, sourceDir); err != nil {
			return fmt.Errorf("overlay extension skills from %q: %w", sourceDir, err)
		}
	}
	return nil
}

func (r *Runtime) applyHostConfigLayer(targetDir string) error {
	if !strings.EqualFold(strings.TrimSpace(r.run.HostConfig), model.HostConfigPassthrough) {
		return nil
	}
	hostConfigDir := config.HostProfileConfigDir(r.host.Home, r.profile)
	if hostConfigDir == "" || !util.PathExists(hostConfigDir) {
		return nil
	}
	allowedPaths := config.ResolveHostConfigPaths(r.profile, r.run.HostConfigPaths)
	if allowedPaths == nil {
		return fmt.Errorf("resolve host config paths for %s: nil allow-list", r.profile.Name)
	}
	if len(allowedPaths) == 0 {
		return nil
	}
	logx.Infof("%s host config passthrough from %s: %s", util.TitleCase(r.profile.Name), hostConfigDir, strings.Join(allowedPaths, ", "))
	deniedPaths := config.HostConfigPassthroughDeniedPaths(r.profile)
	deniedAbsolute := config.HostConfigPassthroughDeniedAbsolutePaths(r.host.Home, r.profile)
	if err := r.overlayToolConfigLayer(targetDir, hostConfigDir, allowedPaths, deniedPaths, deniedAbsolute); err != nil {
		return fmt.Errorf("overlay host config from %q: %w", hostConfigDir, err)
	}
	return nil
}

func (r *Runtime) applyConfigOverrideScope(targetDir string, sourceDir string, projectScope bool) error {
	scope := "global"
	patchDir := config.HostToolPatchesDir(r.host.Home, r.profile.Name)
	if projectScope {
		scope = "project"
		patchDir = config.HostProjectToolPatchesDir(r.host.Home, r.project.Hash, r.profile.Name)
	}

	patches, err := findConfigPatches(patchDir)
	if err != nil {
		return fmt.Errorf("resolve %s config patches: %w", scope, err)
	}
	for _, patch := range patches {
		fullPath := filepath.Join(sourceDir, patch.relativePath)
		if util.PathExists(fullPath) {
			return fmt.Errorf("%s config override for %q is ambiguous: full file %q and patch %q cannot both be set", scope, patch.relativePath, fullPath, patch.sourcePath)
		}
	}
	if err := r.overlayToolConfigLayer(targetDir, sourceDir, nil, nil, nil); err != nil {
		return fmt.Errorf("overlay %s config from %q: %w", scope, sourceDir, err)
	}
	for _, patch := range patches {
		targetPath := filepath.Join(targetDir, patch.relativePath)
		info, err := os.Stat(targetPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%s config patch %q has no lower-precedence target %q", scope, patch.sourcePath, patch.relativePath)
			}
			return fmt.Errorf("stat %s config patch target %q: %w", scope, targetPath, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s config patch target %q is not a regular file", scope, targetPath)
		}
		if err := mergeConfigPatch(targetPath, patch.sourcePath); err != nil {
			return fmt.Errorf("apply %s config patch for %q: %w", scope, patch.relativePath, err)
		}
	}
	if len(patches) > 0 {
		label := "patches"
		if len(patches) == 1 {
			label = "patch"
		}
		logx.Infof("Applied %d %s %s config %s", len(patches), scope, r.profile.Name, label)
	}
	return nil
}

func (r *Runtime) overlayToolConfigLayer(targetDir string, sourceDir string, allowedPaths []string, deniedPaths []string, deniedAbsolutePaths []string) error {
	if strings.TrimSpace(r.profile.SkillsDir) != "" {
		relativeSkillsDir, err := r.relativePathWithinConfig(r.profile.SkillsDir)
		if err != nil {
			return fmt.Errorf("resolve skills path: %w", err)
		}
		if relativeSkillsDir != "" && relativeSkillsDir != "." {
			if err := removeOverriddenSkillDirs(
				filepath.Join(targetDir, relativeSkillsDir),
				filepath.Join(sourceDir, relativeSkillsDir),
				filepath.ToSlash(relativeSkillsDir),
				allowedPaths,
				deniedPaths,
			); err != nil {
				return err
			}
		}
	}
	return overlayConfigSource(targetDir, sourceDir, allowedPaths, deniedPaths, deniedAbsolutePaths)
}

func removeOverriddenSkillDirs(targetSkillsDir string, sourceSkillsDir string, relativeSkillsDir string, allowedPaths []string, deniedPaths []string) error {
	entries, err := os.ReadDir(sourceSkillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read tool-specific skills directory %q: %w", sourceSkillsDir, err)
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(sourceSkillsDir, entry.Name())
		info, err := os.Stat(sourcePath)
		if err != nil || !info.IsDir() {
			continue
		}
		relativePath := path.Join(relativeSkillsDir, entry.Name())
		if shouldDenyConfigPath(relativePath, deniedPaths) || shouldSkipConfigPath(relativePath, true, allowedPaths) {
			continue
		}
		targetPath := filepath.Join(targetSkillsDir, entry.Name())
		if err := os.RemoveAll(targetPath); err != nil {
			return fmt.Errorf("replace tool-specific skill %q: %w", targetPath, err)
		}
	}
	return nil
}

func (r *Runtime) applySharedSkillsLayer(targetDir string, projectScope bool) error {
	if strings.TrimSpace(r.profile.SkillsDir) == "" {
		return nil
	}

	relativeSkillsDir, err := r.relativePathWithinConfig(r.profile.SkillsDir)
	if err != nil {
		return fmt.Errorf("resolve skills path: %w", err)
	}
	if relativeSkillsDir == "." || relativeSkillsDir == "" {
		return nil
	}

	sourceDir := config.HostSkillsDir(r.host.Home)
	if projectScope {
		sourceDir = config.HostProjectSkillsDir(r.host.Home, r.project.Hash)
	}
	if !util.PathExists(sourceDir) {
		return nil
	}

	skillsTargetDir := filepath.Join(targetDir, relativeSkillsDir)
	if err := os.MkdirAll(skillsTargetDir, 0o700); err != nil {
		return fmt.Errorf("create generated skills directory %q: %w", skillsTargetDir, err)
	}
	if err := overlayPortableSkillSource(skillsTargetDir, sourceDir); err != nil {
		scope := "global"
		if projectScope {
			scope = "project"
		}
		return fmt.Errorf("overlay %s shared skills from %q: %w", scope, sourceDir, err)
	}
	return nil
}

func (r *Runtime) settingsRelativePath() (string, error) {
	if strings.TrimSpace(r.profile.SettingsFile) == "" {
		return "", nil
	}
	rel, err := r.relativePathWithinConfig(r.profile.SettingsTarget)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == "" {
		return "", nil
	}
	return rel, nil
}

func (r *Runtime) relativePathWithinConfig(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	basePath := resolveContainerProfilePath(r.containerHome, r.profile.ConfigDir)
	targetPath := resolveContainerProfilePath(r.containerHome, path)
	if !util.PathWithin(basePath, targetPath) {
		return "", fmt.Errorf("%q is outside config dir %q", targetPath, basePath)
	}
	relPath, err := filepath.Rel(basePath, targetPath)
	if err != nil {
		return "", fmt.Errorf("compute relative path from %q to %q: %w", basePath, targetPath, err)
	}
	if relPath == "." {
		return ".", nil
	}
	return relPath, nil
}

func resolveContainerProfilePath(containerHome string, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(containerHome, path))
}

// overlayConfigSource copies sourceDir onto targetDir, honoring the allow/deny
// filters. Unlike filepath.WalkDir it follows symlinks and copies the resolved
// contents, so host configs managed as symlinks (home-manager/Nix, GNU stow,
// chezmoi) pass through instead of being silently dropped. The allow/deny lists,
// matched on the path relative to sourceDir, remain the security boundary; the
// inode type does not.
//
// Following symlinks deliberately widens passthrough versus the old
// skip-all-symlinks behavior: an allow-listed name (or directory) symlinked to
// an arbitrary location copies that target's contents in, even from outside the
// config dir. That is the point — dotfile managers symlink configs in from a
// store directory. The guards are the reviewed allow-list (gating by name) and
// deniedAbsolute (the tool's own auth/OAuth files). The relative backstop
// deny-list is anchored at the config root, so it does not re-match
// denied-looking names at nested paths reached through a symlinked directory
// (e.g. "sessions/" does not deny "commands/sessions"). This is accepted because
// the config dir and its symlinks are user-controlled.
func overlayConfigSource(targetDir string, sourceDir string, allowedPaths []string, deniedPaths []string, deniedAbsolutePaths []string) error {
	rootDir, err := filepath.EvalSymlinks(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("resolve config source %q: %w", sourceDir, err)
	}
	o := &configOverlay{
		targetDir:      targetDir,
		rootDir:        rootDir,
		allowedPaths:   allowedPaths,
		deniedPaths:    deniedPaths,
		deniedAbsolute: canonicalPathSet(deniedAbsolutePaths),
		ancestors:      map[string]struct{}{rootDir: {}},
	}
	return o.overlayDir(sourceDir, "")
}

// canonicalPathSet resolves each path through symlinks (falling back to a clean
// form) so membership tests compare fully resolved paths.
func canonicalPathSet(paths []string) map[string]struct{} {
	set := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		canonical, err := filepath.EvalSymlinks(p)
		if err != nil {
			canonical = filepath.Clean(p)
		}
		set[canonical] = struct{}{}
	}
	return set
}

// configOverlay carries the invariant context for one overlayConfigSource walk:
// the destination, the resolved source root, the filters, and the set of
// resolved directories on the current recursion stack (so symlinked cycles
// terminate instead of recursing forever).
type configOverlay struct {
	targetDir      string
	rootDir        string
	allowedPaths   []string
	deniedPaths    []string
	deniedAbsolute map[string]struct{}
	ancestors      map[string]struct{}
}

// overlayDir overlays the directory at currentDir, whose path relative to the
// overlay root is relBase, into the destination.
func (o *configOverlay) overlayDir(currentDir string, relBase string) error {
	entries, err := os.ReadDir(currentDir)
	if err != nil {
		return fmt.Errorf("read config directory %q: %w", currentDir, err)
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(currentDir, entry.Name())
		relativePath := entry.Name()
		if relBase != "" {
			relativePath = relBase + "/" + entry.Name()
		}

		// Deny on the visible path first, so denied entries are dropped before
		// the symlink is ever followed.
		if shouldDenyConfigPath(relativePath, o.deniedPaths) {
			continue
		}

		// Resolve through symlinks so the entry is classified by its target.
		info, err := os.Stat(sourcePath)
		if err != nil {
			logx.Debugf("Skipping unreadable host config entry while preparing tool config source: %s", sourcePath)
			continue
		}
		resolved, err := filepath.EvalSymlinks(sourcePath)
		if err != nil {
			logx.Debugf("Skipping unresolvable host config entry while preparing tool config source: %s", sourcePath)
			continue
		}

		// Reject aliases to known auth/OAuth files, including ones outside the
		// config dir (e.g. settings.json -> ~/.claude.json) that the relative
		// deny-list below cannot express.
		if _, denied := o.deniedAbsolute[resolved]; denied {
			logx.Debugf("Skipping host config symlink that aliases a denied auth path: %s", sourcePath)
			continue
		}

		// A symlink must not alias a denied path: when it resolves to a location
		// inside the config dir, apply the deny-list to that target path too.
		// Otherwise an allowed name (e.g. settings.json -> config.json) would
		// copy a denied auth file under the allowed name.
		if targetRel, inside := rootRelative(o.rootDir, resolved); inside && shouldDenyConfigPath(targetRel, o.deniedPaths) {
			logx.Debugf("Skipping host config symlink that aliases denied path %q: %s", targetRel, sourcePath)
			continue
		}

		if shouldSkipConfigPath(relativePath, info.IsDir(), o.allowedPaths) {
			continue
		}

		targetPath := filepath.Join(o.targetDir, filepath.FromSlash(relativePath))
		switch {
		case info.IsDir():
			if _, looping := o.ancestors[resolved]; looping {
				logx.Debugf("Skipping symlinked directory cycle while preparing tool config source: %s", sourcePath)
				continue
			}
			if err := ensureConfigDirectory(targetPath, info.Mode().Perm()); err != nil {
				return fmt.Errorf("create directory %q: %w", targetPath, err)
			}
			o.ancestors[resolved] = struct{}{}
			err = o.overlayDir(sourcePath, relativePath)
			delete(o.ancestors, resolved)
			if err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyConfigFile(sourcePath, targetPath); err != nil {
				return fmt.Errorf("copy file %q to %q: %w", sourcePath, targetPath, err)
			}
		default:
			logx.Debugf("Skipping non-regular host config entry while preparing tool config source: %s", sourcePath)
		}
	}
	return nil
}

// rootRelative reports target's slash path relative to root and whether target
// lies within root.
func rootRelative(root string, target string) (string, bool) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// ensureConfigDirectory creates targetPath as a directory with the given mode,
// replacing any non-directory already occupying the path.
func ensureConfigDirectory(targetPath string, mode os.FileMode) error {
	if info, err := os.Lstat(targetPath); err == nil && !info.IsDir() {
		if err := os.RemoveAll(targetPath); err != nil {
			return fmt.Errorf("remove non-directory target %q: %w", targetPath, err)
		}
	}
	if mode == 0 {
		mode = 0o755
	}
	if err := os.MkdirAll(targetPath, mode); err != nil {
		return fmt.Errorf("create directory %q: %w", targetPath, err)
	}
	return nil
}

func copyConfigFile(sourcePath string, targetPath string) error {
	if info, err := os.Lstat(targetPath); err == nil && info.IsDir() {
		if err := os.RemoveAll(targetPath); err != nil {
			return fmt.Errorf("remove directory target %q: %w", targetPath, err)
		}
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("stat source file %q: %w", sourcePath, err)
	}
	return copySkillFile(sourcePath, targetPath, sourceInfo.Mode().Perm())
}

func shouldSkipConfigPath(relativePath string, isDir bool, allowedPaths []string) bool {
	if allowedPaths == nil {
		return false
	}
	for _, allowedPath := range allowedPaths {
		if config.HostConfigPathMatches(relativePath, allowedPath) {
			return false
		}
		if isDir && strings.HasPrefix(allowedPath, relativePath+"/") {
			return false
		}
	}
	return true
}

func shouldDenyConfigPath(relativePath string, deniedPaths []string) bool {
	for _, deniedPath := range deniedPaths {
		if config.HostConfigPathMatches(relativePath, deniedPath) {
			return true
		}
	}
	return false
}
