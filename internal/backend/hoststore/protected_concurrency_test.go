// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/util"
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

func TestIdentityVerificationDoesNotHoldBrokerLock(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	verify := func() (bool, error) {
		release, err := util.TryAcquireFileLock(LockPath(home, dir))
		if err != nil {
			return false, err
		}
		if release == nil {
			return false, errors.New("engine verification held the broker lock")
		}
		release()
		return true, nil
	}
	if err := ProtectIdentity(home, dir, verify); err != nil {
		t.Fatal(err)
	}
	owner := testIdentityOwner(backend.NameDocker, "a", true)
	if err := AdmitIdentity(home, dir, owner, false, func([]IdentityReservation) (bool, error) { return verify() }, func(IdentityReservation) (bool, error) { return verify() }, nil); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseIdentity(home, dir, owner, func(IdentityReservation) (bool, error) { return verify() }); err != nil {
		t.Fatal(err)
	}
	if err := ReserveIdentity(home, dir, owner); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileIdentityReservations(home, dir, func(IdentityReservation) (bool, error) { return verify() }); err != nil {
		t.Fatal(err)
	}
	if err := WithIdleProtectedIdentity(home, dir, verify, func() error {
		release, err := util.TryAcquireFileLock(LockPath(home, dir))
		if err != nil {
			return err
		}
		if release != nil {
			release()
			return errors.New("grant mutation escaped the broker lock")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := DowngradeIdentity(home, dir, verify); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionRevalidatesReservationSnapshot(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(strconv.FormatBool(reset), func(t *testing.T) {
			home, dir := protectedIdentityTestDir(t)
			if err := ProtectIdentity(home, dir, unusedIdentity); err != nil {
				t.Fatal(err)
			}
			newOwner := testIdentityOwner(backend.NameDocker, "a", true)
			racingOwner := testIdentityOwner(backend.NamePodman, "b", true)
			checks, resets := 0, 0
			err := AdmitIdentity(home, dir, newOwner, reset, func(owners []IdentityReservation) (bool, error) {
				checks++
				if checks == 1 {
					return true, ReserveIdentity(home, dir, racingOwner)
				}
				if !slices.Equal(owners, []IdentityReservation{racingOwner}) {
					return false, errors.New("verification did not see the new owner")
				}
				return true, nil
			}, func(IdentityReservation) (bool, error) { return false, nil }, func() error { resets++; return nil })
			if reset {
				if err == nil || resets != 0 {
					t.Fatalf("stale idle snapshot reset an active grant: %v, resets=%d", err, resets)
				}
				if err := RequireIdentityReservation(home, dir, newOwner); err == nil {
					t.Fatal("failed reset admitted its owner")
				}
			} else {
				if err != nil || checks != 2 {
					t.Fatalf("admission did not retry changed metadata: %v, checks=%d", err, checks)
				}
				if err := RequireIdentityReservation(home, dir, newOwner); err != nil {
					t.Fatal(err)
				}
			}
			if err := RequireIdentityReservation(home, dir, racingOwner); err != nil {
				t.Fatalf("stale snapshot lost a reservation: %v", err)
			}
		})
	}
}

func TestAdmissionSnapshotRetriesAreBounded(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	if err := ProtectIdentity(home, dir, unusedIdentity); err != nil {
		t.Fatal(err)
	}
	owner := testIdentityOwner(backend.NameDocker, "a", true)
	checks := 0
	err := AdmitIdentity(home, dir, owner, false, func([]IdentityReservation) (bool, error) {
		checks++
		other := testIdentityOwner(backend.NameDocker, strconv.FormatInt(int64(checks+10), 16), true)
		return true, ReserveIdentity(home, dir, other)
	}, func(IdentityReservation) (bool, error) { return false, nil }, nil)
	if !errors.Is(err, errIdentityChanged) || checks != 3 {
		t.Fatalf("unbounded or stale admission: %v, checks=%d", err, checks)
	}
	if err := RequireIdentityReservation(home, dir, owner); err == nil {
		t.Fatal("admitted an unverified snapshot")
	}
}
