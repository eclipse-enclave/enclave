// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dockercmd "enclave/internal/docker"
	"enclave/internal/peergroup"
)

func isEngineNoSuchContainer(err error) bool {
	if err == nil {
		return false
	}
	if dockercmd.IsNotFound(err) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such container")
}

// UseEngine points the container CLI wrapper at the engine a group record
// names; call it before any backend of this process touches the engine.
func UseEngine(name string) {
	dockercmd.SetBinary(name)
}

// ForeignEngineError: the group record names another engine or daemon, whose
// containers this engine cannot see.
type ForeignEngineError struct {
	Group  string
	Engine peergroup.Engine
	Here   peergroup.Engine
}

func (e *ForeignEngineError) Error() string {
	if e.Engine.Name != e.Here.Name {
		return fmt.Sprintf("peer group %s runs on %s, not %s; use --backend %s", e.Group, e.Engine.Name, e.Here.Name, e.Engine.Name)
	}
	return fmt.Sprintf("peer group %s runs on another %s daemon (%s), not this one (%s); point %s at that daemon", e.Group, e.Engine.Name, e.Engine.Root, e.Here.Root, e.Engine.Name)
}

// UnknownEngineError: a record written before the engine field, with nothing
// of its group generation on this engine.
type UnknownEngineError struct {
	Group string
	Here  peergroup.Engine
}

func (e *UnknownEngineError) Error() string {
	return fmt.Sprintf("peer group %s records no engine and neither its hub nor a member runs on %s; run `enclave a2a reconcile --peer-group %s --backend <engine>` with the engine it runs on", e.Group, e.Here.Name, e.Group)
}

// IsForeignEngine reports whether err says the group belongs to another engine.
func IsForeignEngine(err error) bool {
	var foreign *ForeignEngineError
	var unknown *UnknownEngineError
	return errors.As(err, &foreign) || errors.As(err, &unknown)
}

// claimEngine checks under the group lock that rec belongs to this engine.
// A record without an engine adopts this one when its hub or a member session
// of the recorded generations exists here, and stays untouched otherwise.
func (p *PeerGroups) claimEngine(ctx context.Context, rec *peergroup.GroupRecord) error {
	if rec.Engine != nil && rec.Engine.Name != "" {
		if rec.Engine.SameDaemon(p.engineID) {
			return nil
		}
		return &ForeignEngineError{Group: rec.Group, Engine: *rec.Engine, Here: p.engineID}
	}
	here, err := p.groupRunsHere(ctx, rec)
	if err != nil {
		return err
	}
	if !here {
		return &UnknownEngineError{Group: rec.Group, Here: p.engineID}
	}
	engine := p.engineID
	rec.Engine = &engine
	return p.store.Save(rec)
}

func (p *PeerGroups) groupRunsHere(ctx context.Context, rec *peergroup.GroupRecord) (bool, error) {
	hub, err := p.engine.inspectHub(ctx, rec.Group)
	if err != nil {
		return false, err
	}
	if hub.Exists && hub.GroupGeneration == string(rec.GroupGeneration) {
		return true, nil
	}
	for _, m := range rec.Members {
		session, err := p.engine.memberSession(ctx, m.SessionContainer)
		if err != nil {
			return false, err
		}
		if session.Exists && session.Labels[peergroup.LabelMemberGeneration] == string(m.Generation) {
			return true, nil
		}
	}
	return false, nil
}
