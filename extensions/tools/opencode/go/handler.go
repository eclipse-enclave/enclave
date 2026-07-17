// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package opencode

import (
	"enclave/internal/model"
	"enclave/internal/tools"
)

func init() {
	tools.RegisterHandler("opencode", Handler{})
}

type Handler struct {
	tools.BaseHandler
}

func (Handler) LoopbackPorts(model.RunContext) []string { return nil }
