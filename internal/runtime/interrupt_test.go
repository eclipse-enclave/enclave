// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"slices"
	"testing"

	"enclave/internal/termtint"
)

// termtint re-raises any interrupt signal the run does not claim, which kills
// the process before the gateway teardown runs.
func TestInterruptSignalsCoverTermtint(t *testing.T) {
	for _, sig := range termtint.InterruptSignals() {
		if !slices.Contains(interruptSignals, sig) {
			t.Errorf("termtint intercepts %v but the run does not survive it", sig)
		}
	}
}
