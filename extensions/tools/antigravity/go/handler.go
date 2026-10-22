// Copyright (C) 2026 Dirk Fauth and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package antigravity

import "enclave/internal/tools"

func init() {
	tools.RegisterHandler("antigravity", Handler{})
}

type Handler struct {
	tools.BaseHandler
}
