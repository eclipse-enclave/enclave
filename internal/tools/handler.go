// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tools

import "enclave/internal/model"

type BaseHandler struct{}

func (BaseHandler) PortHints(ctx model.RunContext) []string { return OAuthPortHints(ctx) }

func (BaseHandler) LoopbackPorts(ctx model.RunContext) []string { return OAuthLoopbackPorts(ctx) }

func (BaseHandler) ValidateRun(ctx model.RunContext) error { return OAuthPortValidateRun(ctx) }
