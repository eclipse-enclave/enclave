// Copyright (C) 2026 Dirk Fauth and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package copilot

import "enclave/internal/tools"

func init() {
	tools.RegisterHandler("copilot", Handler{})
}

type Handler struct {
	tools.BaseHandler
}