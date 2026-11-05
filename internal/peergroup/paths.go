// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"path/filepath"
	"strconv"
)

// Host layout (interfaces.md sections 1 to 5). stateRoot is
// config.HostStateRootDir(home); projectToolDir is
// config.HostProjectToolDir(home, hash, tool); configStoreDir is
// config.HostStoreConfigDir(home, tool, hash, key).

// File and directory names.
const (
	GroupsDirName       = "a2a"
	GroupRecordFile     = "group.json"
	SnapshotsDirName    = "snapshots"
	CurrentLinkName     = "current"
	StagingPrefix       = ".staging-"
	MembershipFile      = "membership.json"
	SecretsFile         = "secrets.json"
	ManifestFile        = "manifest.json"
	StatusDirName       = "status"
	SupervisorStatus    = "supervisor.json"
	ProxyStatus         = "proxy.json"
	InventoryDirName    = "inventory"
	AuditDirName        = "audit"
	AuditLogFile        = "audit.jsonl"
	HostSecretsDirName  = "host-secrets"
	OutboundTokensFile  = "outbound.json"
	OverlayDirName      = "a2a-overlay"
	OverlayFile         = "overlay.json"
	ContextTableDirName = "a2a"
	GroupLockPrefix     = "a2a-"
	GroupLockSuffix     = ".lock"
)

// currentTempPrefix names the temporary symlink renamed over current.
const currentTempPrefix = ".current-"

// GroupsDir is <state>/a2a.
func GroupsDir(stateRoot string) string {
	return filepath.Join(stateRoot, GroupsDirName)
}

// GroupDir is <state>/a2a/<group>.
func GroupDir(stateRoot, group string) string {
	return filepath.Join(GroupsDir(stateRoot), group)
}

// GroupRecordPath is <state>/a2a/<group>/group.json.
func GroupRecordPath(stateRoot, group string) string {
	return filepath.Join(GroupDir(stateRoot, group), GroupRecordFile)
}

// SnapshotsDir is <state>/a2a/<group>/snapshots, mounted read-only into the hub.
func SnapshotsDir(stateRoot, group string) string {
	return filepath.Join(GroupDir(stateRoot, group), SnapshotsDirName)
}

// SnapshotDir is <state>/a2a/<group>/snapshots/<revision>.
func SnapshotDir(stateRoot, group string, rev Revision) string {
	return filepath.Join(SnapshotsDir(stateRoot, group), revisionName(rev))
}

// CurrentLinkPath is <state>/a2a/<group>/snapshots/current.
func CurrentLinkPath(stateRoot, group string) string {
	return filepath.Join(SnapshotsDir(stateRoot, group), CurrentLinkName)
}

// StatusDir is <state>/a2a/<group>/status, mounted read-write into the hub.
func StatusDir(stateRoot, group string) string {
	return filepath.Join(GroupDir(stateRoot, group), StatusDirName)
}

// InventoryPath is <state>/a2a/<group>/inventory/<groupGeneration>.json.
func InventoryPath(stateRoot, group string, groupGeneration Generation) string {
	return filepath.Join(inventoryDir(stateRoot, group), string(groupGeneration)+".json")
}

func inventoryDir(stateRoot, group string) string {
	return filepath.Join(GroupDir(stateRoot, group), InventoryDirName)
}

// AuditDir is <state>/a2a/<group>/audit, mounted read-write into the hub.
func AuditDir(stateRoot, group string) string {
	return filepath.Join(GroupDir(stateRoot, group), AuditDirName)
}

// OutboundTokensPath is <state>/a2a/<group>/host-secrets/outbound.json (0600, host user).
func OutboundTokensPath(stateRoot, group string) string {
	return filepath.Join(GroupDir(stateRoot, group), HostSecretsDirName, OutboundTokensFile)
}

// GroupLockName is the HostLockPath name of a group's lock: "a2a-<group>.lock".
func GroupLockName(group string) string {
	return GroupLockPrefix + group + GroupLockSuffix
}

// OverlayDir is <projectToolDir>/a2a-overlay/<session>, mounted read-only into the gateway.
func OverlayDir(projectToolDir, sessionContainer string) string {
	return filepath.Join(projectToolDir, OverlayDirName, sessionContainer)
}

// OverlayPath is <projectToolDir>/a2a-overlay/<session>/overlay.json.
func OverlayPath(projectToolDir, sessionContainer string) string {
	return filepath.Join(OverlayDir(projectToolDir, sessionContainer), OverlayFile)
}

// ContextTableDir is <configStoreDir>/a2a/<memberGeneration>; one table per
// member generation, so inventory deletion never touches another generation.
func ContextTableDir(configStoreDir string, memberGeneration Generation) string {
	return filepath.Join(configStoreDir, ContextTableDirName, string(memberGeneration))
}

func revisionName(rev Revision) string {
	return strconv.FormatUint(uint64(rev), 10)
}

// parseRevisionName accepts only canonical decimal names, so "07" or "+7" never alias 7.
func parseRevisionName(name string) (Revision, bool) {
	v, err := strconv.ParseUint(name, 10, 64)
	if err != nil || v == 0 || strconv.FormatUint(v, 10) != name {
		return 0, false
	}
	return Revision(v), true
}
