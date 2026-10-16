// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"os"

	"enclave/internal/model"
)

func (r *Runtime) beginTerminal(ctx context.Context, target model.TerminalTarget, handled ...os.Signal) func(error) {
	if r.terminalLifecycle == nil {
		return func(error) {}
	}
	return r.terminalLifecycle(ctx, target, handled...)
}
