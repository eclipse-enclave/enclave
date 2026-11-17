// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package testutil

import (
	"fmt"
	"slices"
	"testing"

	"enclave/internal/backend/hoststore"
)

// EndModeHolder deletes a finalizing holder as a successful
// hoststore.EndMode does, without its owner and finalizer checks.
func EndModeHolder(t *testing.T, home, dir, id string) {
	t.Helper()
	err := hoststore.WithModeHolders(home, dir, func(holders *[]hoststore.ModeHolder) error {
		before := len(*holders)
		*holders = slices.DeleteFunc(*holders, func(holder hoststore.ModeHolder) bool {
			return holder.ID == id && holder.State == hoststore.ModeFinalizing
		})
		if len(*holders) == before {
			return fmt.Errorf("no finalizing session mode holder %s", id)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
