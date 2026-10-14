// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/util"
)

// IdentityReservation belongs to a logical session, not its CLI or proxy
// process. Incarnation must be freshly generated for every new session even
// when the engine reuses the same container name.
type IdentityReservation struct {
	Engine      string `json:"engine"`
	Session     string `json:"session"`
	Incarnation string `json:"incarnation"`
	Protected   bool   `json:"protected"`
}

const maxIdentityStateBytes = 65536

type identityState struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Protected     bool                  `json:"protected"`
	Reservations  []IdentityReservation `json:"reservations,omitempty"`
}

// RequireUnprotectedAuth is a fail-closed guard for legacy mounts, resets and
// reconcile helpers. Missing host-only metadata is legacy; corrupt metadata is not.
// Callers already holding WithLock may call it without taking another lock.
func RequireUnprotectedAuth(home, dir string) error {
	state, err := readIdentityState(home, dir)
	if err != nil {
		return err
	}
	if state.Protected {
		return fmt.Errorf("auth identity is gateway-protected (%s); refusing legacy credential access; use --protect-auth or explicitly reset and downgrade the idle identity", IdentityStatePath(home, dir))
	}
	return nil
}

// ProtectIdentity enrolls only a fresh identity. verifyUnused must inspect all
// supported engines while the admission lock is held, including sessions that
// predate reservation tracking.
func ProtectIdentity(home, dir string, verifyUnused func() (bool, error)) error {
	return withIdleIdentity(home, dir, verifyUnused, func(state *identityState) error {
		if !state.Protected {
			entries, err := os.ReadDir(dir)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if len(entries) != 0 {
				return errors.New("protected OAuth requires a fresh auth identity; existing files are not imported or deleted")
			}
		}
		if err := ensureIdentityDir(dir); err != nil {
			return err
		}
		state.Protected = true
		return nil
	})
}

// DowngradeIdentity is an explicit host-only transition after a trusted reset.
// Requiring an empty store avoids exposing broker state or silently translating
// a saved grant to a native file. Reset alone never changes protection.
func DowngradeIdentity(home, dir string, verifyUnused func() (bool, error)) error {
	return withIdleIdentity(home, dir, verifyUnused, func(state *identityState) error {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(entries) != 0 {
			return errors.New("reset the protected grant before explicitly downgrading its identity")
		}
		state.Protected = false
		return nil
	})
}

// WithIdleProtectedIdentity coordinates trusted reset/import with admission and
// the broker writer using the exact same host-path lock. The callback must not
// remove or replace the host-only identity metadata; a reset retains protection.
func WithIdleProtectedIdentity(home, dir string, verifyUnused func() (bool, error), mutate func() error) error {
	if mutate == nil {
		return errors.New("protected identity mutation is required")
	}
	return withIdleIdentity(home, dir, verifyUnused, func(state *identityState) error {
		if !state.Protected {
			return errors.New("auth identity is not protected")
		}
		return mutate()
	})
}

// IdentityProtected reads only trusted host metadata. Saved protection remains
// effective when a later session omits the enrollment option.
func IdentityProtected(home, dir string) (bool, error) {
	state, err := readIdentityState(home, dir)
	return state.Protected, err
}

// AcquireAuthMountAdmission serializes workload bind admission with protected
// enrollment across engines. Acquire it before any individual identity lock;
// workloads retain it until the engine confirms their container was started.
func AcquireAuthMountAdmission(home string) (func(), error) {
	if home == "" {
		return func() {}, nil
	}
	path := config.HostLockPath(home, "auth-mount-admission.lock")
	if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), path, true); err != nil {
		return nil, err
	}
	return util.AcquireFileLock(path, func() {
		logx.Infof("Waiting for another Enclave session to finish starting or stopping...")
	})
}

// HasProtectedIdentities examines trusted metadata, not workload auth trees.
// It still finds saved protection when an identity directory has been removed.
func HasProtectedIdentities(home string) (bool, error) {
	dir := filepath.Dir(config.HostLockPath(home, "auth-mount-admission.lock"))
	if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), dir, true); err != nil {
		return false, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	protected := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "store-") || !strings.HasSuffix(entry.Name(), ".identity.json") {
			continue
		}
		state, err := readIdentityStateFile(home, filepath.Join(dir, entry.Name()))
		if err != nil {
			return false, err
		}
		protected = protected || state.Protected
	}
	return protected, nil
}

// ReservedIncarnations collects the incarnations reserved by any identity, so
// leftovers of other incarnations can be retired. Callers hold mount admission,
// which serializes every reservation change. Unreadable metadata is an error,
// never an empty set.
func ReservedIncarnations(home string) (map[string]bool, error) {
	dir := config.HostLocksDir(home)
	if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), dir, true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	reserved := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "store-") || !strings.HasSuffix(entry.Name(), ".identity.json") {
			continue
		}
		state, err := readIdentityStateFile(home, filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		for _, reservation := range state.Reservations {
			reserved[reservation.Incarnation] = true
		}
	}
	return reserved, nil
}

// AdmitIdentity combines stale-owner reconciliation, optional fresh enrollment
// and reservation before any workload store exposure. Callers hold mount admission
// across verification and commit; engine inspection never holds the broker lock.
// verifyAccess must reject all credential/control mounts except exact gateway
// binds belonging to the supplied live protected reservations.
func AdmitIdentity(home, dir string, reservation IdentityReservation, reset bool, verifyAccess func([]IdentityReservation) (bool, error), verifyEnded func(IdentityReservation) (bool, error), resetGrant func() error) error {
	return admitIdentity(home, dir, reservation, reset, false, verifyAccess, verifyEnded, resetGrant)
}

// AdmitNativeIdentity transfers an existing native feature store to gateway
// custody. Only the gateway may subsequently parse or project its contents.
func AdmitNativeIdentity(home, dir string, reservation IdentityReservation, verifyAccess func([]IdentityReservation) (bool, error), verifyEnded func(IdentityReservation) (bool, error)) error {
	return admitIdentity(home, dir, reservation, false, true, verifyAccess, verifyEnded, nil)
}

func admitIdentity(home, dir string, reservation IdentityReservation, reset, native bool, verifyAccess func([]IdentityReservation) (bool, error), verifyEnded func(IdentityReservation) (bool, error), resetGrant func() error) error {
	if err := validateReservation(reservation); err != nil {
		return err
	}
	if verifyAccess == nil || verifyEnded == nil {
		return errors.New("cross-engine identity verification is required")
	}
	return mutateVerifiedIdentity(home, dir, func(state *identityState) error {
		if err := reconcileReservations(state, verifyEnded); err != nil {
			return err
		}
		if state.Protected && !reservation.Protected {
			return errors.New("saved identity requires protected authentication")
		}
		if reservation.Protected {
			if (!state.Protected || reset) && len(state.Reservations) != 0 {
				return errors.New("stop all auth identity sessions before enrollment or reset")
			}
			// Existing protected gateways may share the grant and its lock, but
			// only for these host-owned reservations. Enrollment/reset stay idle-only.
			safe, err := verifyAccess(slices.Clone(state.Reservations))
			if err != nil {
				return err
			}
			if !safe {
				return errors.New("auth identity has conflicting container mounts; stop legacy sessions or restart older protected sessions with a rebuilt gateway")
			}
		}
		return nil
	}, func(state *identityState) error {
		if reservation.Protected {
			if !state.Protected {
				entries, err := os.ReadDir(dir)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if len(entries) != 0 && !native {
					return errors.New("protected OAuth requires a fresh auth identity; existing credentials are not imported")
				}
				if err := ensureIdentityDir(dir); err != nil {
					return err
				}
				state.Protected = true
			}
			if reset {
				if resetGrant == nil {
					return errors.New("trusted protected grant reset is required")
				}
				if err := resetGrant(); err != nil {
					return err
				}
			}
		}
		if reservationConflicts(state.Reservations, reservation) {
			return errors.New("auth identity already reserves this session or incarnation")
		}
		state.Reservations = append(state.Reservations, reservation)
		return nil
	})
}

// ReconcileIdentityReservations removes only owners whose preparation lease and
// engine state have both been verified ended by the trusted caller.
func ReconcileIdentityReservations(home, dir string, verifyEnded func(IdentityReservation) (bool, error)) error {
	if verifyEnded == nil {
		return errors.New("session end verification is required")
	}
	release, err := AcquireAuthMountAdmission(home)
	if err != nil {
		return err
	}
	defer release()
	return mutateVerifiedIdentity(home, dir, func(state *identityState) error { return reconcileReservations(state, verifyEnded) }, nil)
}

func reconcileReservations(state *identityState, verifyEnded func(IdentityReservation) (bool, error)) error {
	remaining := make([]IdentityReservation, 0, len(state.Reservations))
	for _, reservation := range state.Reservations {
		ended, err := verifyEnded(reservation)
		if err != nil {
			return err
		}
		if !ended {
			remaining = append(remaining, reservation)
		}
	}
	state.Reservations = remaining
	return nil
}

// RequireIdentityReservation confirms that a prepared request still owns its
// exact incarnation; a handle or workload label cannot create admission.
func RequireIdentityReservation(home, dir string, reservation IdentityReservation) error {
	return WithLock(home, dir, func() error {
		state, err := readIdentityState(home, dir)
		if err != nil {
			return err
		}
		if state.Protected != reservation.Protected || !slices.Contains(state.Reservations, reservation) {
			return errors.New("auth session reservation is missing or no longer valid")
		}
		return nil
	})
}

// ReserveIdentity atomically admits cooperating sessions across projects and
// engines in the same auth mode. Activation must reserve legacy sessions too,
// before preparing or exposing their stores. Merely checking
// RequireUnprotectedAuth is not admission.
func ReserveIdentity(home, dir string, reservation IdentityReservation) error {
	if err := validateReservation(reservation); err != nil {
		return err
	}
	return mutateIdentity(home, dir, func(state *identityState) error {
		if reservation.Protected != state.Protected {
			return errors.New("session auth mode does not match saved identity protection")
		}
		if reservationConflicts(state.Reservations, reservation) {
			return errors.New("auth identity already reserves this session or incarnation")
		}
		state.Reservations = append(state.Reservations, reservation)
		return nil
	})
}

// ReleaseIdentity only removes the exact incarnation after a trusted engine
// query verifies that session ended. CLI exit, elapsed time and proxy reload
// never count as evidence. An inspection failure keeps the reservation. Callers
// hold mount admission so a verified end cannot race a workload start.
func ReleaseIdentity(home, dir string, reservation IdentityReservation, verifyEnded func(IdentityReservation) (bool, error)) error {
	if verifyEnded == nil {
		return errors.New("session end verification is required")
	}
	return mutateVerifiedIdentity(home, dir, func(state *identityState) error {
		for i, existing := range state.Reservations {
			if existing != reservation {
				continue
			}
			ended, err := verifyEnded(existing)
			if err != nil {
				return err
			}
			if !ended {
				return errors.New("auth identity owner is still active")
			}
			state.Reservations = slices.Delete(state.Reservations, i, i+1)
			return nil
		}
		return nil
	}, nil)
}

func withIdleIdentity(home, dir string, verifyUnused func() (bool, error), mutate func(*identityState) error) error {
	release, err := AcquireAuthMountAdmission(home)
	if err != nil {
		return err
	}
	defer release()
	if verifyUnused == nil {
		return errors.New("cross-engine session verification is required")
	}
	return mutateVerifiedIdentity(home, dir, func(state *identityState) error {
		if len(state.Reservations) != 0 {
			return errors.New("auth identity has active session reservations")
		}
		unused, err := verifyUnused()
		if err != nil {
			return err
		}
		if !unused {
			return errors.New("auth identity is in use")
		}
		return nil
	}, mutate)
}

// RemoveIdleIdentity deletes an identity with its grant, admission metadata and
// lock once every reserving session has verifiably ended and verifyUnused
// confirms no container can mount it. It serves per-session identities, which
// nothing reuses. Callers hold mount admission.
func RemoveIdleIdentity(home, dir string, verifyEnded func(IdentityReservation) (bool, error), verifyUnused func() (bool, error)) error {
	if verifyEnded == nil || verifyUnused == nil {
		return errors.New("cross-engine session verification is required")
	}
	err := mutateVerifiedIdentity(home, dir, func(state *identityState) error {
		if err := reconcileReservations(state, verifyEnded); err != nil {
			return err
		}
		if len(state.Reservations) != 0 {
			return errors.New("auth identity has active session reservations")
		}
		unused, err := verifyUnused()
		if err != nil {
			return err
		}
		if !unused {
			return errors.New("auth identity is in use")
		}
		return nil
	}, func(*identityState) error {
		if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), dir, true); err != nil {
			return err
		}
		return os.RemoveAll(dir)
	})
	if err != nil {
		return err
	}
	for _, path := range []string{IdentityStatePath(home, dir), legacyIdentityStatePath(home, dir), LockPath(home, dir)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncIdentityDir(config.HostLocksDir(home))
}

var errIdentityChanged = errors.New("auth identity changed during verification; retry the operation")

// Engine verification can take seconds. Inspect an atomic metadata snapshot
// without the broker lock, then compare it under that lock before committing.
// Grant mutations still run under the exact lock mounted by every gateway.
func mutateVerifiedIdentity(home, dir string, verify, mutate func(*identityState) error) error {
	const maxAttempts = 3
	for range maxAttempts {
		before, err := readIdentityState(home, dir)
		if err != nil {
			return err
		}
		checked := before
		checked.Reservations = slices.Clone(before.Reservations)
		if err := verify(&checked); err != nil {
			return err
		}
		err = mutateIdentity(home, dir, func(current *identityState) error {
			if current.Protected != before.Protected || !slices.Equal(current.Reservations, before.Reservations) {
				return errIdentityChanged
			}
			*current = checked
			if mutate != nil {
				return mutate(current)
			}
			return nil
		})
		if !errors.Is(err, errIdentityChanged) {
			return err
		}
	}
	return errIdentityChanged
}

func mutateIdentity(home, dir string, mutate func(*identityState) error) error {
	if home == "" || !filepath.IsAbs(dir) {
		return errors.New("protected identity requires a host home and absolute store path")
	}
	file := IdentityStatePath(home, dir)
	if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), file, true); err != nil {
		return err
	}
	// Persist the metadata parent before WithLock can create it implicitly.
	if err := ensureIdentityDir(filepath.Dir(file)); err != nil {
		return err
	}
	return WithLock(home, dir, func() error {
		if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), dir, true); err != nil {
			return err
		}
		state, err := readIdentityState(home, dir)
		if err != nil {
			return err
		}
		if err := mutate(&state); err != nil {
			return err
		}
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if len(data) > maxIdentityStateBytes {
			return errors.New("auth identity reservation metadata is full")
		}
		if err := util.WriteFileAtomic(file, data, 0o600); err != nil {
			return err
		}
		return syncIdentityDir(filepath.Dir(file))
	})
}

// IdentityStatePath returns trusted host-only admission metadata next to the
// store lock. It is never inside an auth or config directory mounted into the
// workload, so a legacy session cannot enroll or corrupt identity protection.
func IdentityStatePath(home, dir string) string {
	if home == "" || !filepath.IsAbs(dir) {
		return ""
	}
	return config.HostLockPath(home, storeLockBase(home, dir)+".identity.json")
}

// legacyIdentityStatePath is where builds before path-relative keys kept the
// metadata, keyed by the raw directory path.
func legacyIdentityStatePath(home, dir string) string {
	return config.HostLockPath(home, "store-"+util.HashString(dir)+".identity.json")
}

// adoptLegacyIdentityState moves metadata an earlier build kept under the raw
// path key to the current key, so saved protection is never lost.
func adoptLegacyIdentityState(home, dir, file string) error {
	legacy := legacyIdentityStatePath(home, dir)
	if legacy == file {
		return nil
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), legacy, true); err != nil {
		return err
	}
	info, err := os.Lstat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("invalid protected auth identity metadata file")
	}
	// A concurrent adopter may have moved it first.
	if err := os.Rename(legacy, file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncIdentityDir(filepath.Dir(file))
}

func readIdentityState(home, dir string) (state identityState, err error) {
	state = identityState{SchemaVersion: 1}
	file := IdentityStatePath(home, dir)
	if file == "" {
		return state, errors.New("protected identity requires a host home and absolute store path")
	}
	if info, err := os.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return state, errors.New("auth identity directory is not a real directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return state, err
	}
	if err := adoptLegacyIdentityState(home, dir, file); err != nil {
		return state, err
	}
	return readIdentityStateFile(home, file)
}

func readIdentityStateFile(home, file string) (state identityState, err error) {
	state = identityState{SchemaVersion: 1}
	defer func() {
		if err != nil {
			err = fmt.Errorf("auth identity metadata %q: %w", file, err)
		}
	}()
	if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), file, true); err != nil {
		return state, err
	}
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxIdentityStateBytes {
		return state, errors.New("invalid protected auth identity metadata file")
	}
	data, err := os.ReadFile(file) // #nosec G304 -- fixed metadata path in the trusted, symlink-checked host locks directory.
	if err != nil {
		return state, err
	}
	fields, err := decodeIdentityFields(data, []string{"schemaVersion", "protected", "reservations"})
	if err != nil {
		return state, errors.New("invalid protected auth identity metadata")
	}
	var protected *bool
	var schema *int
	if json.Unmarshal(fields["schemaVersion"], &schema) != nil || schema == nil || *schema != 1 || json.Unmarshal(fields["protected"], &protected) != nil || protected == nil {
		return state, errors.New("invalid protected auth identity metadata")
	}
	state.Protected = *protected
	if raw, ok := fields["reservations"]; ok {
		state.Reservations, err = decodeReservations(raw, state.Protected)
		if err != nil {
			return state, err
		}
	}
	return state, nil
}

func decodeReservations(data []byte, protected bool) ([]IdentityReservation, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return nil, errors.New("invalid protected auth identity reservations")
	}
	var reservations []IdentityReservation
	for _, raw := range rows {
		fields, err := decodeIdentityFields(raw, []string{"engine", "session", "incarnation", "protected"})
		if err != nil || len(fields) != 4 {
			return nil, errors.New("invalid protected auth identity reservation")
		}
		var reservation IdentityReservation
		var reservationProtected *bool
		if err := json.Unmarshal(fields["protected"], &reservationProtected); err != nil || reservationProtected == nil {
			return nil, errors.New("invalid protected auth identity reservation")
		}
		if err := json.Unmarshal(raw, &reservation); err != nil || validateReservation(reservation) != nil || reservation.Protected != protected {
			return nil, errors.New("invalid protected auth identity reservation")
		}
		if reservationConflicts(reservations, reservation) {
			return nil, errors.New("duplicate auth identity reservation")
		}
		reservations = append(reservations, reservation)
	}
	return reservations, nil
}

func reservationConflicts(reservations []IdentityReservation, candidate IdentityReservation) bool {
	return slices.ContainsFunc(reservations, func(existing IdentityReservation) bool {
		return existing.Incarnation == candidate.Incarnation || (existing.Engine == candidate.Engine && existing.Session == candidate.Session)
	})
}

func decodeIdentityFields(data []byte, allowed []string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("identity metadata must be an object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || !slices.Contains(allowed, key) || fields[key] != nil {
			return nil, errors.New("unknown or duplicate identity metadata field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing identity metadata")
	}
	return fields, nil
}

func ensureIdentityDir(dir string) error {
	var missing []string
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302,G703 -- private directory needs owner traversal; caller validates its complete path beneath the state root.
		return err
	}
	for _, created := range missing {
		if err := syncIdentityDir(filepath.Dir(created)); err != nil {
			return err
		}
	}
	return nil
}

func syncIdentityDir(dir string) error {
	f, err := os.Open(dir) // #nosec G304 -- caller validates this trusted identity directory against symlinks.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func validateReservation(reservation IdentityReservation) error {
	if reservation.Engine != backend.NameDocker && reservation.Engine != backend.NamePodman {
		return fmt.Errorf("unsupported protected auth engine %q", reservation.Engine)
	}
	if reservation.Session == "" || len(reservation.Session) > 255 || len(reservation.Incarnation) < 32 || len(reservation.Incarnation) > 128 {
		return errors.New("auth reservation requires a session name and fresh unpredictable incarnation")
	}
	return nil
}
