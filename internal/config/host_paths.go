// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"path/filepath"
	"strings"

	"enclave/internal/model"
)

const (
	enclaveDirName         = "." + model.AppName
	configFilename         = "config.json"
	networkPolicyFilename  = "network.jsonc"
	recentProjectsFilename = "recent-projects.json"
)

func HostLocksDir(home string) string {
	return filepath.Join(hostStateRoot(home), "locks")
}

func HostLockPath(home string, name string) string {
	return filepath.Join(HostLocksDir(home), name)
}

func HostBuildDir(home string) string {
	return filepath.Join(hostCacheRoot(home), "build")
}

func HostSSHDir(home string) string {
	return filepath.Join(hostCacheRoot(home), "ssh")
}

func HostPatchesDir(home string) string {
	return filepath.Join(hostConfigRoot(home), "patches")
}

func HostToolPatchesDir(home string, tool string) string {
	return filepath.Join(HostPatchesDir(home), tool)
}

func HostGatewayAllowlistsDir(home string) string {
	return filepath.Join(hostConfigRoot(home), GatewayAllowlistsDirName)
}

func HostConfigDir(home string) string {
	return filepath.Join(hostConfigRoot(home), "tools")
}

func HostToolConfigDir(home string, tool string) string {
	return filepath.Join(HostConfigDir(home), tool)
}

// HostSessionsConfigPath is the file storing GUI session defaults (e.g. the
// default tool pre-selected in the New Session dialog).
func HostSessionsConfigPath(home string) string {
	return filepath.Join(HostConfigDir(home), "sessions.jsonc")
}

func HostExtensionsDir(home string) string {
	return filepath.Join(hostConfigRoot(home), "extensions")
}

func HostCommandsDir(home string) string {
	return filepath.Join(hostConfigRoot(home), "commands")
}

func HostCommandsHostDir(home string) string {
	return filepath.Join(HostCommandsDir(home), "host")
}

func HostCommandsSessionDir(home string) string {
	return filepath.Join(HostCommandsDir(home), "session")
}

func HostSkillsDir(home string) string {
	return filepath.Join(hostConfigRoot(home), model.SkillsDirName)
}

// HostProjectOverridesDir is the config-root home for per-project, user-edited
// overrides (config.json, network.jsonc, per-tool config/skills, patches, allowlists).
// It is keyed by project hash and lives outside the worktree so a project can
// no longer influence its own isolation from inside the checkout.
func HostProjectOverridesDir(home string, projectHash string) string {
	return filepath.Join(hostConfigRoot(home), "projects", projectHash)
}

func hostProjectOverridesToolDir(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectOverridesDir(home, projectHash), tool)
}

func HostProjectConfigJSONPath(home string, projectHash string) string {
	return filepath.Join(HostProjectOverridesDir(home, projectHash), configFilename)
}

func HostProjectNetworkPolicyPath(home string, projectHash string) string {
	return filepath.Join(HostProjectOverridesDir(home, projectHash), networkPolicyFilename)
}

func HostProjectPatchesDir(home string, projectHash string) string {
	return filepath.Join(HostProjectOverridesDir(home, projectHash), "patches")
}

func HostProjectToolPatchesDir(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectPatchesDir(home, projectHash), tool)
}

func HostProjectGatewayAllowlistsDir(home string, projectHash string) string {
	return filepath.Join(HostProjectOverridesDir(home, projectHash), GatewayAllowlistsDirName)
}

func HostProjectsDir(home string) string {
	return filepath.Join(hostStateRoot(home), "projects")
}

func HostProjectDir(home string, projectHash string) string {
	return filepath.Join(HostProjectsDir(home), projectHash)
}

func HostProjectToolDir(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectDir(home, projectHash), tool)
}

// HostImageInboxDir is the single global image inbox shared by every
// --image-inbox session on the host, mounted read-only at /mnt/host-images.
// It is deliberately not project-scoped: `enclave img import` writes here once
// and the image is visible to any running (and future) inbox-enabled session.
func HostImageInboxDir(home string) string {
	return filepath.Join(hostCacheRoot(home), "inbox")
}

func HostProjectHistoryDir(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectToolDir(home, projectHash, tool), "history")
}

func HostProjectMemoryDir(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectToolDir(home, projectHash, tool), "memory")
}

func HostProjectHomeConfigDir(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectToolDir(home, projectHash, tool), model.HomeConfigDirName)
}

func HostProjectConfigDir(home string, projectHash string, tool string) string {
	return filepath.Join(hostProjectOverridesToolDir(home, projectHash, tool), "config")
}

func HostProjectGeneratedConfigDir(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectToolDir(home, projectHash, tool), model.GeneratedConfigDirName)
}

// HostStoreConfigRootDir is the host directory holding every config store key
// for a tool within a project (the "default" store plus any ephemeral
// session/worktree keys).
func HostStoreConfigRootDir(home string, tool string, projectHash string) string {
	return filepath.Join(HostProjectToolDir(home, projectHash, tool), "config-store")
}

// HostStoreConfigDir is the host directory backing a tool's config store for a
// project. The key is either "default" or a session/worktree suffix, mirroring
// the config-generated/<key> convention.
func HostStoreConfigDir(home string, tool string, projectHash string, key string) string {
	return filepath.Join(HostStoreConfigRootDir(home, tool, projectHash), key)
}

// HostStoreEnvDir is the host directory backing a tool's persistent env store
// for a project.
func HostStoreEnvDir(home string, tool string, projectHash string) string {
	return filepath.Join(HostProjectToolDir(home, projectHash, tool), "env")
}

// HostStoreAuthRootDir is the host directory holding every tool's shared auth
// store. It is not project-scoped: credentials are reused across projects.
func HostStoreAuthRootDir(home string) string {
	return filepath.Join(hostStateRoot(home), "tools")
}

// HostStoreAuthTreeDir is the host directory holding all of one tool's shared
// auth identities (default and every --auth-name slug).
func HostStoreAuthTreeDir(home string, tool string) string {
	return filepath.Join(HostStoreAuthRootDir(home), tool, "auth")
}

// HostStoreAuthDir is the host directory backing a tool's shared auth store.
// It is not project-scoped: credentials are reused across projects. identity
// selects the named auth identity (--auth-name); empty selects "default",
// mirroring the config-store/<key> convention.
func HostStoreAuthDir(home string, tool string, identity string) string {
	if strings.TrimSpace(identity) == "" {
		identity = "default"
	}
	return filepath.Join(HostStoreAuthTreeDir(home, tool), identity)
}

// HostStoreFeatureAuthRootDir is the host directory holding every feature's
// auth store. Like the shared tool auth stores, it is not project-scoped.
func HostStoreFeatureAuthRootDir(home string) string {
	return filepath.Join(hostStateRoot(home), "features")
}

// HostStoreFeatureAuthDir is the host directory backing a feature's auth store.
// Like the shared tool auth store, it is not project-scoped.
func HostStoreFeatureAuthDir(home string, feature string) string {
	return filepath.Join(HostStoreFeatureAuthRootDir(home), feature, "auth")
}

func HostProjectSkillsDir(home string, projectHash string) string {
	return filepath.Join(HostProjectOverridesDir(home, projectHash), model.SkillsDirName)
}

func HostProjectLogsDir(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectToolDir(home, projectHash, tool), "logs")
}

func HostProjectNetworkLogPath(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectLogsDir(home, projectHash, tool), "network.log")
}

func HostProjectGatewayConfigDir(home string, projectHash string, tool string) string {
	return filepath.Join(HostProjectToolDir(home, projectHash, tool), "gateway-config")
}

func HostSecretsDir(home string) string {
	return filepath.Join(hostStateRoot(home), "secrets")
}

func HostSecretsProjectFile(home string, projectHash string, tool string) string {
	return filepath.Join(HostSecretsDir(home), "projects", projectHash, tool+".env")
}

func HostSecretsGlobalSharedFile(home string) string {
	return filepath.Join(HostSecretsDir(home), "global.env")
}

func HostSecretsGlobalFile(home string, tool string) string {
	return filepath.Join(HostSecretsDir(home), "global", tool+".env")
}

// HostTheiaLogsDir holds per-container Theia IDE launch logs. Logs are
// state per the XDG spec (persistent, machine-specific, not user-edited).
func HostTheiaLogsDir(home string) string {
	return filepath.Join(hostStateRoot(home), "logs", "theia")
}

func HostTLSDir(home string) string {
	return filepath.Join(hostStateRoot(home), "tls")
}

func HostTLSHostsDir(home string) string {
	return filepath.Join(HostTLSDir(home), "hosts")
}

func HostNetworkPolicyPath(home string) string {
	return filepath.Join(hostConfigRoot(home), networkPolicyFilename)
}

func HostCacheDir(home string) string {
	return hostCacheRoot(home)
}

func HostCacheToolProjectDir(home string, tool string, projectHash string) string {
	return filepath.Join(HostCacheDir(home), tool, projectHash)
}

func HostRecentProjectsPath(home string) string {
	return filepath.Join(hostConfigRoot(home), recentProjectsFilename)
}

// HostConfigRootDir returns the config root for enclave
// (<XDG_CONFIG_HOME|~/.config>/enclave on Linux, ~/Library/Application
// Support/org.eclipse.enclave/config on macOS). It is surfaced to user
// host commands via ENCLAVE_CONFIG_DIR.
func HostConfigRootDir(home string) string {
	return hostConfigRoot(home)
}

// HostStateRootDir returns the state root for enclave
// (<XDG_STATE_HOME|~/.local/state>/enclave on Linux, ~/Library/Application
// Support/org.eclipse.enclave/state on macOS).
func HostStateRootDir(home string) string {
	return hostStateRoot(home)
}

func GlobalConfigPath() (string, error) {
	home, err := ResolveHostHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(hostConfigRoot(home), configFilename), nil
}

// ProjectConfigJSONPath resolves the per-project override config path for a
// project directory: <config-root>/projects/<hash>/config.json. Returns an
// empty string when the host home or project hash cannot be resolved.
func ProjectConfigJSONPath(projectDir string) string {
	home, err := ResolveHostHome()
	if err != nil {
		return ""
	}
	project, err := ResolveProjectFromDir(projectDir)
	if err != nil {
		return ""
	}
	return HostProjectConfigJSONPath(home, project.Hash)
}

func HostProfilePath(home string, path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(home, path)
}

func HostProfileConfigDir(home string, profile model.Profile) string {
	if profile.HostConfigDir != "" {
		return HostProfilePath(home, profile.HostConfigDir)
	}
	if profile.ConfigDir != "" {
		return HostProfilePath(home, profile.ConfigDir)
	}
	return ""
}

func HostProfileCredentialsPath(home string, profile model.Profile) string {
	if profile.HostCredentialsFile == "" {
		return ""
	}
	if baseDir := HostProfileConfigDir(home, profile); baseDir != "" {
		return filepath.Join(baseDir, profile.HostCredentialsFile)
	}
	return HostProfilePath(home, profile.HostCredentialsFile)
}

func HostProfileOAuthJSONPath(home string, profile model.Profile) string {
	return HostProfilePath(home, profile.HostOAuthJSON)
}
