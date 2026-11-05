// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/a2a"
)

// Membership is membership.json (proposal 6, interfaces.md section 1).
type Membership struct {
	SchemaVersion   string             `json:"schemaVersion"`
	Group           string             `json:"group"`
	GroupGeneration Generation         `json:"groupGeneration"`
	Revision        Revision           `json:"revision"`
	HubPort         int                `json:"hubPort"`
	Members         []MembershipMember `json:"members"`
	HostPrincipal   HostPrincipalEntry `json:"hostPrincipal"`
}

// MembershipMember is one session member.
type MembershipMember struct {
	Name       string     `json:"name"`
	Role       a2a.Role   `json:"role"`
	Generation Generation `json:"generation"`
	// ProjectPath is host-canonical; MountMode is "rw" or "ro".
	ProjectPath string `json:"projectPath"`
	MountMode   string `json:"mountMode"`
	Network     string `json:"network"`
	Subnet      string `json:"subnet"`
	HubAddress  string `json:"hubAddress"`
	// GatewayAddress is both the hub INPUT source and, for workers, the adapter address.
	GatewayAddress string `json:"gatewayAddress"`
	// AdapterPort is 0 for the orchestrator.
	AdapterPort      int              `json:"adapterPort"`
	InboundTokenHash string           `json:"inboundTokenHash"`
	Description      string           `json:"description"`
	Tool             string           `json:"tool"`
	Skills           []a2a.AgentSkill `json:"skills"`
}

// HostPrincipalEntry is the host principal and its live invocation credentials.
type HostPrincipalEntry struct {
	Name        string           `json:"name"`
	Generation  Generation       `json:"generation"`
	Credentials []CredentialHash `json:"credentials"`
}

// CredentialHash is one host invocation credential as published to the proxy.
type CredentialHash struct {
	Hash      string `json:"hash"`
	ExpiresAt string `json:"expiresAt"`
}

// Secrets is secrets.json: outbound hub-to-worker tokens, readable by the proxy uid only.
type Secrets struct {
	SchemaVersion   string           `json:"schemaVersion"`
	Group           string           `json:"group"`
	GroupGeneration Generation       `json:"groupGeneration"`
	Revision        Revision         `json:"revision"`
	Outbound        []OutboundSecret `json:"outbound"`
}

// OutboundSecret is one worker's outbound token.
type OutboundSecret struct {
	Member     string     `json:"member"`
	Generation Generation `json:"generation"`
	Token      string     `json:"token"`
}

// Manifest is manifest.json, written last into a staged snapshot directory.
type Manifest struct {
	SchemaVersion   string     `json:"schemaVersion"`
	Group           string     `json:"group"`
	GroupGeneration Generation `json:"groupGeneration"`
	Revision        Revision   `json:"revision"`
	CreatedAt       string     `json:"createdAt"`
	// Files maps MembershipFile and SecretsFile to "sha256:<hex>" of their bytes.
	Files map[string]string `json:"files"`
}

// Snapshot is one verified revision as loaded by a reader.
type Snapshot struct {
	Manifest   Manifest
	Membership Membership
	// Secrets is nil for readers that do not load it (the supervisor).
	Secrets *Secrets
}

// SecretsOwner makes a staged secrets.json owned by HubProxyUID with mode
// 0400; the engine backend implements it because the host user may not chown.
type SecretsOwner func(ctx context.Context, path string) error

// SnapshotPublisher writes revisions under the group's snapshot root.
type SnapshotPublisher interface {
	// Publish stages membership and secrets with a new revision, writes the
	// manifest, renames the staging directory to the revision and swaps
	// current. It does not wait for acknowledgments; see StatusWaiter.
	Publish(ctx context.Context, group string, m Membership, s Secrets) (Revision, error)
	// Current returns the revision current points at, 0 when nothing is published.
	Current(group string) (Revision, error)
	// Prune removes every revision directory except committed, previous (the
	// last committed revision before it, skipped when 0) and the current
	// target, plus staging and temp-link leftovers.
	Prune(group string, committed, previous Revision) error
}

// NewSnapshotPublisher returns the filesystem publisher. A nil owner leaves
// secrets.json 0400 and owned by the host user (tests, or readers running as
// the host user).
func NewSnapshotPublisher(stateRoot string, owner SecretsOwner) SnapshotPublisher {
	return &fsPublisher{stateRoot: stateRoot, owner: owner}
}

// LocalSecretsOwner chowns to uid from this process. Only root (or
// CAP_CHOWN) can give a file away, so as a regular user it succeeds only
// when uid is the caller's own uid and otherwise returns the EPERM; engines
// that need another uid use an engine-side SecretsOwner instead.
func LocalSecretsOwner(uid int) SecretsOwner {
	return func(ctx context.Context, path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if uid != os.Geteuid() {
			if err := os.Lchown(path, uid, -1); err != nil {
				return fmt.Errorf("chown %s to uid %d: %w", path, uid, err)
			}
		}
		return os.Chmod(path, secretsFileMode)
	}
}

type fsPublisher struct {
	stateRoot string
	owner     SecretsOwner
}

func (p *fsPublisher) Publish(ctx context.Context, group string, m Membership, s Secrets) (Revision, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := ValidateGroupName(group); err != nil {
		return 0, err
	}
	if m.Group != group || s.Group != group {
		return 0, fmt.Errorf("snapshot for %s carries group %q / %q", group, m.Group, s.Group)
	}
	if !m.GroupGeneration.Valid() || s.GroupGeneration != m.GroupGeneration {
		return 0, fmt.Errorf("snapshot for %s: group generations %q / %q", group, m.GroupGeneration, s.GroupGeneration)
	}
	if err := checkSchema(&m.SchemaVersion); err != nil {
		return 0, err
	}
	if err := checkSchema(&s.SchemaVersion); err != nil {
		return 0, err
	}
	root := SnapshotsDir(p.stateRoot, group)
	if err := ensureDir(root, publicDirMode); err != nil {
		return 0, err
	}
	rev, err := nextRevision(root)
	if err != nil {
		return 0, err
	}
	m.Revision, s.Revision = rev, rev
	if m.HubPort == 0 {
		m.HubPort = HubPort
	}
	normalizeMembership(&m)
	if s.Outbound == nil {
		s.Outbound = []OutboundSecret{}
	}

	staging := filepath.Join(root, StagingPrefix+revisionName(rev))
	if err := os.RemoveAll(staging); err != nil {
		return 0, err
	}
	if err := os.Mkdir(staging, publicDirMode); err != nil {
		return 0, err
	}
	if err := os.Chmod(staging, publicDirMode); err != nil {
		return 0, err
	}
	membership, err := marshalJSON(m)
	if err != nil {
		return 0, err
	}
	secrets, err := marshalJSON(s)
	if err != nil {
		return 0, err
	}
	if err := writeFileSynced(filepath.Join(staging, MembershipFile), membership, publicFileMode); err != nil {
		return 0, err
	}
	secretsPath := filepath.Join(staging, SecretsFile)
	if err := writeFileSynced(secretsPath, secrets, secretsFileMode); err != nil {
		return 0, err
	}
	if p.owner != nil {
		if err := p.owner(ctx, secretsPath); err != nil {
			return 0, fmt.Errorf("hand %s to the hub proxy: %w", SecretsFile, err)
		}
	}
	manifest, err := marshalJSON(Manifest{
		SchemaVersion:   SchemaVersion,
		Group:           group,
		GroupGeneration: m.GroupGeneration,
		Revision:        rev,
		CreatedAt:       FormatTimestamp(time.Now()),
		Files:           map[string]string{MembershipFile: hashBytes(membership), SecretsFile: hashBytes(secrets)},
	})
	if err != nil {
		return 0, err
	}
	if err := writeFileSynced(filepath.Join(staging, ManifestFile), manifest, publicFileMode); err != nil {
		return 0, err
	}
	if err := syncDir(staging); err != nil {
		return 0, err
	}
	if err := os.Rename(staging, filepath.Join(root, revisionName(rev))); err != nil {
		return 0, err
	}
	if err := syncDir(root); err != nil {
		return 0, err
	}
	if err := swapCurrent(root, rev); err != nil {
		return 0, err
	}
	return rev, nil
}

func normalizeMembership(m *Membership) {
	if m.Members == nil {
		m.Members = []MembershipMember{}
	}
	for i := range m.Members {
		if m.Members[i].Skills == nil {
			m.Members[i].Skills = []a2a.AgentSkill{}
		}
	}
	if m.HostPrincipal.Credentials == nil {
		m.HostPrincipal.Credentials = []CredentialHash{}
	}
}

// swapCurrent replaces current with a relative symlink to rev in one rename,
// so a reader resolves either the old or the new revision, never neither.
func swapCurrent(root string, rev Revision) error {
	tmp := filepath.Join(root, currentTempPrefix+revisionName(rev))
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(revisionName(rev), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(root, CurrentLinkName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(root)
}

// nextRevision is one above every revision visible in root, including
// staging and temp-link leftovers, so a number is never reused.
func nextRevision(root string) (Revision, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	var highest Revision
	for _, e := range entries {
		name := e.Name()
		if after, ok := strings.CutPrefix(name, StagingPrefix); ok {
			name = after
		} else if after, ok := strings.CutPrefix(name, currentTempPrefix); ok {
			name = after
		}
		if rev, ok := parseRevisionName(name); ok && rev > highest {
			highest = rev
		}
	}
	cur, err := readCurrent(root)
	if err != nil {
		return 0, err
	}
	highest = max(highest, cur)
	if highest == math.MaxUint64 {
		return 0, errors.New("snapshot revisions exhausted")
	}
	return highest + 1, nil
}

// readCurrent returns 0 when current does not exist.
func readCurrent(root string) (Revision, error) {
	target, err := os.Readlink(filepath.Join(root, CurrentLinkName))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	rev, ok := parseRevisionName(target)
	if !ok {
		return 0, fmt.Errorf("snapshot link %s points at %q, not a revision", filepath.Join(root, CurrentLinkName), target)
	}
	return rev, nil
}

func (p *fsPublisher) Current(group string) (Revision, error) {
	if err := ValidateGroupName(group); err != nil {
		return 0, err
	}
	return readCurrent(SnapshotsDir(p.stateRoot, group))
}

func (p *fsPublisher) Prune(group string, committed, previous Revision) error {
	if err := ValidateGroupName(group); err != nil {
		return err
	}
	root := SnapshotsDir(p.stateRoot, group)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cur, err := readCurrent(root)
	if err != nil {
		return err
	}
	if previous >= committed && previous != 0 {
		return fmt.Errorf("prune %s: previous revision %d is not below committed %d", group, previous, committed)
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		remove := strings.HasPrefix(name, StagingPrefix) || strings.HasPrefix(name, currentTempPrefix)
		if rev, ok := parseRevisionName(name); ok {
			remove = rev != committed && rev != previous && rev != cur
		}
		if remove {
			if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := syncDir(root); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// LoadCurrent resolves current under snapshotRoot, verifies the manifest
// hashes and that every file carries the manifest revision and expected
// group generation, and loads secrets only when withSecrets is set. Hub
// readers use it; on error they keep their last revision and report it.
func LoadCurrent(snapshotRoot string, groupGeneration Generation, withSecrets bool) (*Snapshot, error) {
	if !groupGeneration.Valid() {
		return nil, fmt.Errorf("invalid expected group generation %q", groupGeneration)
	}
	rev, err := readCurrent(snapshotRoot)
	if err != nil {
		return nil, err
	}
	if rev == 0 {
		return nil, fmt.Errorf("no snapshot published under %s", snapshotRoot)
	}
	dir := filepath.Join(snapshotRoot, revisionName(rev))
	snap := &Snapshot{}
	if err := readJSON(filepath.Join(dir, ManifestFile), &snap.Manifest); err != nil {
		return nil, err
	}
	man := snap.Manifest
	if man.Revision != rev {
		return nil, fmt.Errorf("snapshot %d: manifest carries revision %d", rev, man.Revision)
	}
	if man.GroupGeneration != groupGeneration {
		return nil, fmt.Errorf("snapshot %d: group generation %s, expected %s", rev, man.GroupGeneration, groupGeneration)
	}
	if len(man.Files) != 2 || man.Files[MembershipFile] == "" || man.Files[SecretsFile] == "" {
		return nil, fmt.Errorf("snapshot %d: manifest must list exactly %s and %s", rev, MembershipFile, SecretsFile)
	}

	membership, err := readVerified(dir, MembershipFile, man.Files[MembershipFile])
	if err != nil {
		return nil, fmt.Errorf("snapshot %d: %w", rev, err)
	}
	if err := decodeJSON(membership, &snap.Membership); err != nil {
		return nil, fmt.Errorf("snapshot %d: %s: %w", rev, MembershipFile, err)
	}
	if err := checkFileHeader(man, snap.Membership.Group, snap.Membership.GroupGeneration, snap.Membership.Revision); err != nil {
		return nil, fmt.Errorf("snapshot %d: %s: %w", rev, MembershipFile, err)
	}

	secrets, err := readVerified(dir, SecretsFile, man.Files[SecretsFile])
	if err != nil {
		return nil, fmt.Errorf("snapshot %d: %w", rev, err)
	}
	if withSecrets {
		snap.Secrets = &Secrets{}
		if err := decodeJSON(secrets, snap.Secrets); err != nil {
			return nil, fmt.Errorf("snapshot %d: %s: %w", rev, SecretsFile, err)
		}
		if err := checkFileHeader(man, snap.Secrets.Group, snap.Secrets.GroupGeneration, snap.Secrets.Revision); err != nil {
			return nil, fmt.Errorf("snapshot %d: %s: %w", rev, SecretsFile, err)
		}
	}
	return snap, nil
}

func readVerified(dir, name, want string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- name is one of the fixed snapshot files.
	if err != nil {
		return nil, err
	}
	if got := hashBytes(data); got != want {
		return nil, fmt.Errorf("%s hashes to %s, manifest says %s", name, got, want)
	}
	return data, nil
}

func checkFileHeader(man Manifest, group string, gen Generation, rev Revision) error {
	if group != man.Group || gen != man.GroupGeneration || rev != man.Revision {
		return fmt.Errorf("carries group %q generation %s revision %d, manifest says %q %s %d",
			group, gen, rev, man.Group, man.GroupGeneration, man.Revision)
	}
	return nil
}
