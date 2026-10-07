// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
)

func TestProtectedAdmissionSharesOnlyVerifiedReservations(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	first := testIdentityOwner(backend.NameDocker, "a", true)
	second := testIdentityOwner(backend.NamePodman, "b", true)
	live := func(IdentityReservation) (bool, error) { return false, nil }
	if err := AdmitIdentity(home, dir, first, false, func(owners []IdentityReservation) (bool, error) {
		if len(owners) != 0 {
			t.Fatal("fresh enrollment exempted existing owners")
		}
		return true, nil
	}, live, nil); err != nil {
		t.Fatal(err)
	}
	verify := func(owners []IdentityReservation) (bool, error) {
		if !slices.Equal(owners, []IdentityReservation{first}) {
			t.Fatalf("shared admission did not pass exact live reservation: %+v", owners)
		}
		owners[0].Session = "cannot-mutate-trusted-state"
		return true, nil
	}
	if err := AdmitIdentity(home, dir, second, false, verify, live, nil); err != nil {
		t.Fatal(err)
	}
	if err := RequireIdentityReservation(home, dir, first); err != nil {
		t.Fatal(err)
	}
	reset := false
	if err := AdmitIdentity(home, dir, testIdentityOwner(backend.NameDocker, "c", true), true, verify, live, func() error { reset = true; return nil }); err == nil || reset {
		t.Fatal("reset changed grant while siblings were active")
	}
	third := testIdentityOwner(backend.NameDocker, "c", true)
	if err := AdmitIdentity(home, dir, third, false, func([]IdentityReservation) (bool, error) { return false, nil }, live, nil); err == nil {
		t.Fatal("shared admission ignored conflicting exposure")
	}
	if err := RequireIdentityReservation(home, dir, third); err == nil {
		t.Fatal("failed admission retained reservation")
	}
	if err := AdmitIdentity(home, dir, third, false, verify, func(owner IdentityReservation) (bool, error) { return owner == second, nil }, nil); err != nil {
		t.Fatal(err)
	}
	if err := RequireIdentityReservation(home, dir, second); err == nil {
		t.Fatal("verified ended sibling was not reconciled")
	}
	if err := RequireIdentityReservation(home, dir, first); err != nil {
		t.Fatalf("stale-owner reconciliation removed live sibling: %v", err)
	}
}

func TestIdentityReservationsRejectDuplicateNamesAndIncarnations(t *testing.T) {
	for _, duplicate := range []string{"name", "incarnation"} {
		t.Run(duplicate, func(t *testing.T) {
			home, dir := protectedIdentityTestDir(t)
			if err := ProtectIdentity(home, dir, unusedIdentity); err != nil {
				t.Fatal(err)
			}
			first := testIdentityOwner(backend.NameDocker, "a", true)
			second := first
			if duplicate == "name" {
				second.Incarnation = strings.Repeat("b", 32)
			} else {
				second.Engine, second.Session = backend.NamePodman, "other"
			}
			if err := ReserveIdentity(home, dir, first); err != nil {
				t.Fatal(err)
			}
			if err := ReserveIdentity(home, dir, second); err == nil {
				t.Fatal("duplicate reservation admitted")
			}
			data, err := json.Marshal(identityState{SchemaVersion: 1, Protected: true, Reservations: []IdentityReservation{first, second}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(IdentityStatePath(home, dir), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readIdentityState(home, dir); err == nil {
				t.Fatal("duplicate durable reservation accepted")
			}
		})
	}
}
