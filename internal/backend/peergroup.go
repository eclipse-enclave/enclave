// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package backend

import (
	"context"

	"enclave/internal/a2a"
)

// HubSpec describes the hub container of one peer group (proposal 5, 6).
type HubSpec struct {
	Group           string
	GroupGeneration string
	Image           string
	// Host directories mounted at the peergroup.Hub*MountPath container paths:
	// snapshots read-only, status and audit read-write.
	SnapshotDir string
	StatusDir   string
	AuditDir    string
	Env         []EnvVar
	Labels      map[string]string
	// Initial is the first member's attachment; the hub is created on that
	// bridge (CR-A6-1, CR-A7-2). Without it StartHub only starts an existing hub.
	Initial *HubAttachment
}

// HubRef identifies a hub container.
type HubRef struct {
	Group string
	ID    string
	Name  string
}

// HubAttachment is the hub's endpoint on one member's session bridge.
type HubAttachment struct {
	Network string
	// IPv4 is the static hub address inside the bridge subnet.
	IPv4   string
	Member string
}

// HubInfo is a hub as found by listing, for reconcile.
type HubInfo struct {
	Ref             HubRef
	GroupGeneration string
	Running         bool
	Attachments     []HubAttachment
}

// WorkerSignalResult is the parsed ctl response of a worker.
type WorkerSignalResult struct {
	Response a2a.CtlResponse
}

// PeerGroupBackend is implemented by engine backends that support
// --peer-group. Host lifecycle (join, leave, reconcile) drives it under the
// group lock; none of these calls waits for snapshot acknowledgments.
type PeerGroupBackend interface {
	// StartHub creates and starts the hub, or returns the running one.
	StartHub(ctx context.Context, spec HubSpec) (HubRef, error)
	// StopHub stops and removes the hub; all attachments must be gone.
	StopHub(ctx context.Context, ref HubRef) error
	// ReloadHub sends SIGHUP; a nudge, never an acknowledgment.
	ReloadHub(ctx context.Context, ref HubRef) error
	// ConnectHub attaches the hub to a session bridge at a static address.
	ConnectHub(ctx context.Context, ref HubRef, att HubAttachment) error
	// DisconnectHub detaches the hub; it is the connectivity fallback.
	DisconnectHub(ctx context.Context, ref HubRef, network string) error
	// SignalWorker runs "enclave-a2a-agent ctl" in the worker via engine exec.
	SignalWorker(ctx context.Context, ref SessionRef, req a2a.CtlRequest) (WorkerSignalResult, error)
	// ReloadGatewayOverlay sends SIGHUP to the session's gateway and waits for
	// the overlay marker of generation.
	ReloadGatewayOverlay(ctx context.Context, ref SessionRef, generation uint64) error
	// ListHubs returns every enclave hub on the engine.
	ListHubs(ctx context.Context) ([]HubInfo, error)
}

// PeerGroupJoin completes a member's join inside session start (CR-A7-1).
// The backend calls the hooks in order; any error fails the start.
type PeerGroupJoin interface {
	// OnSessionNetwork runs once the session bridge exists, before the gateway.
	OnSessionNetwork(ctx context.Context, network string) error
	// OnGateway runs once the gateway is ready, before the session container.
	OnGateway(ctx context.Context, gatewayContainer, network string) error
	// Started releases the group lock once the session container exists.
	Started()
	// Fail rolls the join back; calling it after Started or twice is a no-op.
	Fail(ctx context.Context) error
	// LeaveOnExit leaves the group when a foreground session ends, before
	// its gateway and bridge are removed.
	LeaveOnExit(ctx context.Context)
}

// PeerGroupSession is what a member session's start needs beyond Request
// (backend.Request.PeerGroup, CR-W0B-1).
type PeerGroupSession struct {
	// Join carries the join this start completes; required.
	Join             PeerGroupJoin
	Group            string
	GroupGeneration  string
	Member           string
	MemberGeneration string
	Role             a2a.Role
	// OverlayDir is the host directory mounted read-only at
	// peergroup.GatewayOverlayDir in the gateway.
	OverlayDir string
	Env        []EnvVar
	Labels     map[string]string
}

// SessionPeerGroup is a listed member session's A2A identity, read from its
// enclave.a2a.* labels (interfaces.md section 7).
type SessionPeerGroup struct {
	Group            string
	GroupGeneration  string
	Member           string
	MemberGeneration string
	Role             a2a.Role
}
