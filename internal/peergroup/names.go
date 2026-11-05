// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package peergroup owns the host-side state of A2A peer groups: group
// records, revisioned snapshots, status acknowledgments, cleanup inventory,
// tokens, the worktree guard and the gateway overlay file.
package peergroup

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// SchemaVersion is the "schemaVersion" of every file this package defines.
const SchemaVersion = "1"

// GroupNamePattern validates group names; they appear in paths, labels and
// container names, so invalid names are rejected, never sanitized.
var GroupNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// HostMemberName is the host principal's member name; no sanitized session
// name can take this form.
const HostMemberName = "_host"

// Generation is an opaque member or group generation: 16 lowercase hex
// characters from crypto/rand.
type Generation string

// Revision numbers snapshots; monotonic per group generation, starting at 1.
// Numbers are never reused, a rollback publishes a new revision.
type Revision uint64

// Fixed v1 ports, carried through env, snapshot and overlay so they can change.
const (
	HubPort     = 41820
	AdapterPort = 41821
)

// HubProxyUID is the unprivileged proxy uid inside the hub image; the host
// makes secrets.json readable by this uid only.
const HubProxyUID = 65532

// Hub container paths of the group's host directories.
const (
	HubSnapshotMountPath = "/run/enclave-a2a/snapshots"
	HubStatusMountPath   = "/run/enclave-a2a/status"
	HubAuditMountPath    = "/var/log/enclave-a2a"
)

// HubContainerPrefix prefixes hub container names: "enclave-a2a-hub-<group>".
const HubContainerPrefix = "enclave-a2a-hub-"

var (
	ErrGroupNotFound   = errors.New("peer group not found")
	ErrInvalidGroup    = errors.New("invalid peer group name")
	ErrNotCommitted    = errors.New("snapshot revision not committed")
	ErrRevisionRefused = errors.New("snapshot revision refused by a hub reader")
	ErrWorktreeClash   = errors.New("two writable members on one project path")
)

// NewGeneration returns a fresh random Generation.
func NewGeneration() (Generation, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate generation: %w", err)
	}
	return Generation(hex.EncodeToString(b[:])), nil
}

// ValidateGroupName reports ErrInvalidGroup for names outside GroupNamePattern.
func ValidateGroupName(name string) error {
	if !GroupNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidGroup, name)
	}
	return nil
}

var generationPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Valid reports whether g has the form of 16 lowercase hex characters.
func (g Generation) Valid() bool { return generationPattern.MatchString(string(g)) }

// FormatTimestamp renders t as RFC 3339 in UTC with second precision.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// ParseTimestamp parses a timestamp written by FormatTimestamp.
func ParseTimestamp(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }
