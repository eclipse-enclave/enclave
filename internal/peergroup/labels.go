// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import "enclave/internal/model"

// Engine labels (interfaces.md section 7).
const (
	// LabelHub is "true" on hub containers; teardown treats endpoints carrying
	// it as owned, not foreign.
	LabelHub = model.AppName + ".a2a.hub"
	// LabelGroup is the group name, on hub and member session containers.
	LabelGroup = model.AppName + ".a2a.group"
	// LabelGroupGeneration is the group generation, on hub and member session containers.
	LabelGroupGeneration = model.AppName + ".a2a.group_generation"
	// LabelMember is the member name, on member session containers.
	LabelMember = model.AppName + ".a2a.member"
	// LabelMemberGeneration is the member generation, on member session containers.
	LabelMemberGeneration = model.AppName + ".a2a.member_generation"
	// LabelRole is "orchestrator" or "worker", on member session containers.
	LabelRole = model.AppName + ".a2a.role"
)
