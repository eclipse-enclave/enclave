// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"context"
	"time"

	"enclave/internal/a2a"
)

// Member states in GroupMember.State.
const (
	MemberJoining = "joining"
	MemberActive  = "active"
	MemberLeaving = "leaving"
)

// Departure kinds and states.
const (
	DepartureSuspected     = "suspected"
	DepartureAuthoritative = "authoritative"

	DeparturePending  = "pending"
	DepartureComplete = "complete"
	DepartureDegraded = "degraded"
)

// Obligation kinds in Obligation.Kind.
const (
	ObligationSnapshotCommit    = "snapshot-commit"
	ObligationDepartureAck      = "departure-ack"
	ObligationQuarantineRelease = "quarantine-release"
	ObligationEngineDisconnect  = "engine-disconnect"
)

// GroupRecord is group.json (proposal 16, interfaces.md section 3).
type GroupRecord struct {
	SchemaVersion   string     `json:"schemaVersion"`
	Group           string     `json:"group"`
	GroupGeneration Generation `json:"groupGeneration"`
	CreatedAt       string     `json:"createdAt"`
	HubContainer    string     `json:"hubContainer"`
	// Engine is nil in records written before it existed: the engine is then unknown.
	Engine        *Engine            `json:"engine,omitempty"`
	HostPrincipal HostPrincipal      `json:"hostPrincipal"`
	Committed     Revision           `json:"committedRevision"`
	Pending       *PendingRevision   `json:"pendingRevision"`
	Members       []GroupMember      `json:"members"`
	Departures    []Departure        `json:"departures"`
	Obligations   []Obligation       `json:"obligations"`
	Releases      []Release          `json:"releases"`
	Credentials   []InvocationRecord `json:"credentials"`
}

// Engine identifies the container engine that runs a group's hub and members.
type Engine struct {
	// Name is "docker" or "podman".
	Name string `json:"name"`
	// Root is the engine's storage root from its info; it tells a rootless
	// daemon from a rootful one of the same engine. Empty when unknown.
	Root string `json:"root,omitempty"`
}

// String renders the engine for messages.
func (e Engine) String() string {
	if e.Root == "" {
		return e.Name
	}
	return e.Name + " (" + e.Root + ")"
}

// SameDaemon reports whether e and other name the same engine; an empty root
// on either side matches any root.
func (e Engine) SameDaemon(other Engine) bool {
	if e.Name != other.Name {
		return false
	}
	return e.Root == "" || other.Root == "" || e.Root == other.Root
}

// HostPrincipal is the group's stable host principal (proposal 7).
type HostPrincipal struct {
	Name       string     `json:"name"`
	Generation Generation `json:"generation"`
}

// PendingRevision is a published revision not yet committed or rolled back.
type PendingRevision struct {
	Revision Revision `json:"revision"`
	// Purpose is "join", "leave", "credential" or "rollback".
	Purpose   string `json:"purpose"`
	Member    string `json:"member,omitempty"`
	StartedAt string `json:"startedAt"`
}

// GroupMember is one session member as the host recorded it at join.
type GroupMember struct {
	Name             string     `json:"name"`
	Role             a2a.Role   `json:"role"`
	Generation       Generation `json:"generation"`
	State            string     `json:"state"`
	SessionContainer string     `json:"sessionContainer"`
	Tool             string     `json:"tool"`
	ProjectHash      string     `json:"projectHash"`
	ConfigStoreKey   string     `json:"configStoreKey"`
	ProjectPath      string     `json:"projectPath"`
	MountMode        string     `json:"mountMode"`
	Network          string     `json:"network"`
	Subnet           string     `json:"subnet"`
	HubAddress       string     `json:"hubAddress"`
	GatewayAddress   string     `json:"gatewayAddress"`
	AdapterPort      int        `json:"adapterPort"`
	InboundTokenHash string     `json:"inboundTokenHash"`
	Description      string     `json:"description"`
	JoinedAt         string     `json:"joinedAt"`
	LeaveIntentAt    string     `json:"leaveIntentAt,omitempty"`
}

// Departure is a suspected or authoritative departure of one principal.
type Departure struct {
	Member     string             `json:"member"`
	Generation Generation         `json:"generation"`
	Kind       string             `json:"kind"`
	State      string             `json:"state"`
	ObservedAt string             `json:"observedAt"`
	UpdatedAt  string             `json:"updatedAt"`
	Acks       []a2a.DepartureAck `json:"acks"`
}

// Obligation is a durable reconciliation obligation; reconcile retries it.
type Obligation struct {
	Kind          string     `json:"kind"`
	Member        string     `json:"member,omitempty"`
	Generation    Generation `json:"generation,omitempty"`
	Revision      Revision   `json:"revision,omitempty"`
	Since         string     `json:"since"`
	LastAttemptAt string     `json:"lastAttemptAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
}

// Release is a host-confirmed release of a quarantine or admission stop.
type Release struct {
	// Kind is "quarantine", "admission-stop" or "departure-cleared".
	Kind       string     `json:"kind"`
	Worker     string     `json:"worker,omitempty"`
	Member     string     `json:"member"`
	Generation Generation `json:"generation"`
	ReleasedAt string     `json:"releasedAt"`
}

// InvocationRecord tracks a host invocation credential until its hash is
// unpublished; the credential itself is never stored.
type InvocationRecord struct {
	Hash      string `json:"hash"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
}

// GroupStore persists group records under the group lock.
type GroupStore interface {
	// Lock takes the cross-process group lock; every mutation runs under it.
	Lock(ctx context.Context, group string) (unlock func(), err error)
	// Load returns ErrGroupNotFound when no record exists.
	Load(group string) (*GroupRecord, error)
	// Create writes a record with a new group generation and host principal.
	Create(group string) (*GroupRecord, error)
	// CreateOn is Create with the engine recorded in the first write.
	CreateOn(group string, engine Engine) (*GroupRecord, error)
	// Save atomically replaces the record.
	Save(rec *GroupRecord) error
	// Delete removes the record only if it still has groupGeneration.
	Delete(group string, groupGeneration Generation) error
	// List returns the names of groups with a record or pending inventory.
	List() ([]string, error)
}

// NewGroupStore returns the filesystem store; locksDir is config.HostLocksDir(home).
func NewGroupStore(stateRoot, locksDir string) GroupStore {
	return &fsGroupStore{stateRoot: stateRoot, locksDir: locksDir}
}

// AddCredential records an issued invocation credential by hash only.
func (r *GroupRecord) AddCredential(c InvocationCredential) {
	r.Credentials = append(r.Credentials, InvocationRecord{
		Hash:      c.Hash,
		IssuedAt:  FormatTimestamp(c.IssuedAt),
		ExpiresAt: FormatTimestamp(c.ExpiresAt),
	})
}

// RevokeCredential drops hash and reports whether it was present; the
// caller publishes a revision to make the revocation effective.
func (r *GroupRecord) RevokeCredential(hash string) bool {
	for i, c := range r.Credentials {
		if c.Hash == hash {
			r.Credentials = append(r.Credentials[:i:i], r.Credentials[i+1:]...)
			return true
		}
	}
	return false
}

// ExpireCredentials drops credentials expired at now (or unparsable) and
// returns them; a non-empty result needs a new revision.
func (r *GroupRecord) ExpireCredentials(now time.Time) []InvocationRecord {
	var live, expired []InvocationRecord
	for _, c := range r.Credentials {
		exp, err := ParseTimestamp(c.ExpiresAt)
		if err != nil || !now.Before(exp) {
			expired = append(expired, c)
			continue
		}
		live = append(live, c)
	}
	r.Credentials = live
	if r.Credentials == nil {
		r.Credentials = []InvocationRecord{}
	}
	return expired
}

// CredentialHashes returns the credentials still live at now, as published
// in HostPrincipalEntry.Credentials.
func (r *GroupRecord) CredentialHashes(now time.Time) []CredentialHash {
	out := []CredentialHash{}
	for _, c := range r.Credentials {
		exp, err := ParseTimestamp(c.ExpiresAt)
		if err != nil || !now.Before(exp) {
			continue
		}
		out = append(out, CredentialHash{Hash: c.Hash, ExpiresAt: c.ExpiresAt})
	}
	return out
}
