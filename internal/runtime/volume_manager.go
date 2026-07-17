// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// storeSet is the neutral record of which persistent stores this execution
// uses. It replaces the volume-name-carrying model.VolumeState: the runtime
// only tracks store identity; the backend resolves names.
type storeSet struct {
	Config   *backend.StoreRef
	Auth     *backend.StoreRef
	Env      *backend.StoreRef
	Features map[string]backend.StoreRef

	PersistedEnvAvailable bool
}

// AuthStorage is the store where session credentials live: the shared auth
// store when present, the config store otherwise.
func (s storeSet) AuthStorage() *backend.StoreRef {
	if s.Auth != nil {
		return s.Auth
	}
	return s.Config
}

type volumeManager struct {
	*Runtime
	authFiles []string
}

func newVolumeManager(r *Runtime) volumeManager {
	return volumeManager{
		Runtime:   r,
		authFiles: r.profile.ProviderAuthFiles(),
	}
}

// BuildPrep translates the runtime's policy decisions (which stores exist,
// reset flags, layout, overlay source) into backend-neutral store preparation
// intent plus the resulting store set. The backend owns the mechanics.
func (m volumeManager) BuildPrep(volumeSuffix string) (backend.StorePrep, storeSet) {
	prep := backend.StorePrep{}
	stores := storeSet{}

	configKey := backend.StoreKey{Owner: m.profile.Name, ProjectHash: m.project.Hash, Suffix: strings.TrimSpace(volumeSuffix)}
	prep.Config = &backend.ConfigStorePrep{
		Key:        configKey,
		Recreate:   !m.run.Persist,
		LayoutDirs: m.configVolumeRelativeDirs(),
	}
	if m.configSourceDir != "" {
		prep.Config.Overlay = &backend.ConfigOverlaySpec{
			SourceDir:     m.configSourceDir,
			PreservePaths: m.configSourcePreservePaths(),
		}
	}
	stores.Config = &backend.StoreRef{Kind: backend.StoreKindConfig, Key: configKey}

	if m.shouldCreateSharedAuthVolume() {
		// Suffix selects the named auth identity; an empty suffix selects default.
		key := backend.StoreKey{Owner: m.profile.Name, Suffix: m.auth.AuthName}
		prep.Auth = &backend.StorePrepEntry{Key: key}
		stores.Auth = &backend.StoreRef{Kind: backend.StoreKindAuth, Key: key}
	}

	if m.shouldCreateEnvVolume() {
		key := m.envStoreKey()
		prep.Env = &backend.EnvStorePrep{Key: key, Reset: m.auth.ResetAuth}
		stores.Env = &backend.StoreRef{Kind: backend.StoreKindEnv, Key: key}
	}

	for _, feat := range m.features {
		if !m.shouldCreateFeatureAuthVolume(feat) {
			continue
		}
		authFiles, err := backend.ValidateAuthFilePaths(feat.AuthFiles)
		if err != nil || len(authFiles) == 0 {
			continue
		}
		key := backend.StoreKey{Owner: feat.Name}
		prep.Features = append(prep.Features, backend.StorePrepEntry{Key: key})
		if stores.Features == nil {
			stores.Features = map[string]backend.StoreRef{}
		}
		stores.Features[feat.Name] = backend.StoreRef{Kind: backend.StoreKindFeatureAuth, Key: key}
	}

	if m.shouldResetAuthFiles() {
		authFiles, err := backend.ValidateAuthFilePaths(m.authFiles)
		if err != nil {
			logx.Warnf("Failed to reset %s auth files in store: %v", util.TitleCase(m.profile.Name), err)
		} else {
			prep.ResetAuthFiles = authFiles
		}
	}

	return prep, stores
}

// authSyncSpec declares the post-run credential sync for this execution:
// which auth files reconcile to the shared auth store and which feature
// credentials copy to feature auth stores. Returns nil when nothing syncs.
func (m volumeManager) authSyncSpec(stores storeSet) *backend.AuthSyncSpec {
	if stores.Config == nil {
		return nil
	}
	spec := &backend.AuthSyncSpec{}
	if m.shouldCreateSharedAuthVolume() && stores.Auth != nil {
		if authFiles, err := backend.ValidateAuthFilePaths(m.authFiles); err == nil {
			spec.AuthFiles = authFiles
		}
	}
	for _, feat := range m.features {
		if _, ok := stores.Features[feat.Name]; !ok {
			continue
		}
		authFiles, err := backend.ValidateAuthFilePaths(feat.AuthFiles)
		if err != nil || len(authFiles) == 0 {
			continue
		}
		spec.Features = append(spec.Features, backend.FeatureAuthSync{
			Feature:   feat.Name,
			ConfigDir: feat.ConfigDir,
			AuthFiles: authFiles,
		})
	}
	if len(spec.AuthFiles) == 0 && len(spec.Features) == 0 {
		return nil
	}
	return spec
}

func (m volumeManager) configVolumeRelativeDirs() []string {
	dirs := map[string]struct{}{}
	addDirForFile := func(relPath string) {
		relPath = filepath.Clean(strings.TrimSpace(relPath))
		if relPath == "" || relPath == "." {
			return
		}
		dir := filepath.Clean(filepath.Dir(relPath))
		if dir == "" || dir == "." {
			return
		}
		dirs[dir] = struct{}{}
	}

	if settingsPath, err := m.settingsRelativePath(); err == nil {
		addDirForFile(settingsPath)
	}
	if authFiles, err := backend.ValidateAuthFilePaths(m.profile.RuntimeAuthFiles()); err == nil {
		for _, authFile := range authFiles {
			addDirForFile(authFile)
		}
	}

	result := make([]string, 0, len(dirs))
	for dir := range dirs {
		result = append(result, dir)
	}
	sort.Strings(result)
	return result
}

func (m volumeManager) shouldCreateSharedAuthVolume() bool {
	if m.profile.ConfigDir == "" || len(m.authFiles) == 0 {
		return false
	}
	if !m.run.Persist || m.auth.AuthScope != model.AuthScopeShared {
		return false
	}
	return true
}

func (m volumeManager) shouldCreateFeatureAuthVolume(ext model.Extension) bool {
	if ext.ConfigDir == "" || len(ext.AuthFiles) == 0 {
		return false
	}
	if !m.run.Persist || m.auth.AuthScope != model.AuthScopeShared {
		return false
	}
	return true
}

func (m volumeManager) shouldCreateEnvVolume() bool {
	return m.run.Persist
}

func (m volumeManager) envStoreKey() backend.StoreKey {
	return envStoreKey(m.profile.Name, m.project.Hash)
}

func envStoreKey(owner string, projectHash string) backend.StoreKey {
	return backend.StoreKey{Owner: owner, ProjectHash: projectHash}
}

func (m volumeManager) shouldResetAuthFiles() bool {
	if !m.run.Persist {
		return false
	}
	if !m.auth.ResetAuth || len(m.authFiles) == 0 {
		return false
	}
	return true
}
