// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"slices"
	"strings"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	"enclave/internal/model"
)

// PeerGroupMember is what the runtime knows about a session when it joins its peer group.
type PeerGroupMember struct {
	SessionContainer string
	// Member is the sanitized session name.
	Member             string
	ConfigStore        backend.StoreKey
	ContainerConfigDir string
	ProjectReadOnly    bool
}

// PeerGroupJoiner joins the session to its peer group right before the
// container starts; the result goes into backend.Request.PeerGroup, and the
// backend completes or rolls back the join.
type PeerGroupJoiner func(ctx context.Context, member PeerGroupMember) (*backend.PeerGroupSession, error)

// SetPeerGroupJoiner wires the --peer-group join; sessions without a peer group never call it.
func (r *Runtime) SetPeerGroupJoiner(join PeerGroupJoiner) {
	r.peerGroupJoiner = join
}

// peerGroupRole is the session's A2A role, empty without --peer-group. Every
// session carries the a2a_role default, so the group decides.
func (r *Runtime) peerGroupRole() a2a.Role {
	if strings.TrimSpace(r.run.PeerGroup) == "" {
		return ""
	}
	if a2a.Role(r.run.A2ARole) == a2a.RoleOrchestrator {
		return a2a.RoleOrchestrator
	}
	return a2a.RoleWorker
}

// servesPeerGroup reports a worker, whose main process is the A2A adapter rather than the tool.
func (r *Runtime) servesPeerGroup() bool {
	return r.peerGroupRole() == a2a.RoleWorker
}

// orchestratorArgs are the tool's a2a.orchestratorArgs for an orchestrator session.
func (r *Runtime) orchestratorArgs() []string {
	if r.peerGroupRole() != a2a.RoleOrchestrator || r.profile.A2A == nil {
		return nil
	}
	return r.profile.A2A.OrchestratorArgs
}

// appendAfterUserArgs places extra after the user's arguments, so a variadic
// flag in extra cannot swallow a positional prompt, but before a "--"
// terminator, after which it would become positional itself.
func appendAfterUserArgs(agentArgs []string, userArgs []string, extra []string) []string {
	if len(extra) == 0 {
		return append(agentArgs, userArgs...)
	}
	if i := slices.Index(userArgs, "--"); i >= 0 {
		agentArgs = append(agentArgs, userArgs[:i]...)
		agentArgs = append(agentArgs, extra...)
		return append(agentArgs, userArgs[i:]...)
	}
	agentArgs = append(agentArgs, userArgs...)
	return append(agentArgs, extra...)
}

// joinPeerGroup runs the joiner for a --peer-group session and records the result on ec.
func (r *Runtime) joinPeerGroup(ctx context.Context, ec *ExecutionContext) error {
	if r.peerGroupRole() == "" || r.peerGroupJoiner == nil {
		return nil
	}
	member := PeerGroupMember{
		SessionContainer: ec.ContainerName,
		Member:           r.resolvedSessionName(),
		ProjectReadOnly:  model.ProjectMountIsReadonly(r.run.ProjectMount),
	}
	if ec.ConfigStore != nil {
		member.ConfigStore = ec.ConfigStore.Key
	}
	if strings.TrimSpace(r.profile.ConfigDir) != "" {
		member.ContainerConfigDir = resolveContainerProfilePath(r.containerHome, r.profile.ConfigDir)
	}
	session, err := r.peerGroupJoiner(ctx, member)
	if err != nil {
		return err
	}
	ec.PeerGroup = session
	return nil
}
