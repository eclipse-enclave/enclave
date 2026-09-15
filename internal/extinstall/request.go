// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import "enclave/internal/model"

// Op is the requested operation. It travels from the CLI layer to the app
// handlers alongside Kind.
type Op string

const (
	OpList   Op = "list"
	OpAdd    Op = "add"
	OpRemove Op = "remove"
	OpUpdate Op = "update"
)

// Request is the parsed CLI state for one extension-management invocation.
type Request struct {
	Kind        model.ExtensionKind
	Op          Op
	Source      string
	Path        string
	Ref         string
	Names       []string
	All         bool
	Yes         bool
	Force       bool
	DryRun      bool
	JSON        bool
	Interactive bool
}
