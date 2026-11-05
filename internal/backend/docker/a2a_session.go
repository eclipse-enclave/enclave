// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"fmt"
	"strings"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/peergroup"
)

// OnSessionNetwork runs the join steps that need the member's bridge: the
// Podman isolation check, the hub address and the overlay the gateway reads
// at start.
func (j *Join) OnSessionNetwork(ctx context.Context, network string) error {
	if err := j.checkActive(); err != nil {
		return err
	}
	if err := checkPeerGroupNetworkIsolation(ctx, network); err != nil {
		return err
	}
	info, err := networkInspect(ctx, network)
	if err != nil {
		return fmt.Errorf("inspect session network %s: %w", network, err)
	}
	return j.beforeGateway(ctx, network, sessionNetworkFromInspect(info).Subnet)
}

// OnGateway runs the join steps that need the gateway's bridge address.
func (j *Join) OnGateway(ctx context.Context, gatewayName, network string) error {
	if err := j.checkActive(); err != nil {
		return err
	}
	endpoints, err := hubEndpoints(ctx, gatewayName)
	if err != nil {
		return fmt.Errorf("inspect gateway %s: %w", gatewayName, err)
	}
	endpoint, ok := findHubEndpoint(endpoints, network)
	if !ok || strings.TrimSpace(endpoint.IPAddress) == "" {
		return fmt.Errorf("gateway %s has no address on %s", gatewayName, network)
	}
	return j.afterGateway(ctx, strings.TrimSpace(endpoint.IPAddress))
}

// LeaveOnExit leaves the group after a foreground session ends, while the
// gateway and bridge still exist, so revocation precedes the hub disconnect.
func (j *Join) LeaveOnExit(ctx context.Context) {
	j.releaseStartedLock()
	res, err := j.p.Leave(ctx, LeaveSpec{Group: j.spec.Group, Member: j.spec.Member, Generation: j.generation})
	j.mu.Lock()
	j.leave = &res
	j.mu.Unlock()
	switch {
	case err != nil:
		logx.Warnf("Leaving peer group %s failed; run `enclave a2a reconcile`: %v", j.spec.Group, err)
	case res.Degraded:
		logx.Warnf("Left peer group %s degraded: %s", j.spec.Group, strings.Join(res.Problems, "; "))
	}
}

// sessionPeerGroupFromLabels reads a member session's A2A identity; nil
// without the group label.
func sessionPeerGroupFromLabels(labels map[string]string) *backend.SessionPeerGroup {
	group := strings.TrimSpace(labels[peergroup.LabelGroup])
	if group == "" {
		return nil
	}
	return &backend.SessionPeerGroup{
		Group:            group,
		GroupGeneration:  strings.TrimSpace(labels[peergroup.LabelGroupGeneration]),
		Member:           strings.TrimSpace(labels[peergroup.LabelMember]),
		MemberGeneration: strings.TrimSpace(labels[peergroup.LabelMemberGeneration]),
		Role:             a2a.Role(strings.TrimSpace(labels[peergroup.LabelRole])),
	}
}
