// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/util"
)

func startLockNames(t *testing.T, home string) []string {
	t.Helper()
	entries, err := os.ReadDir(config.HostLocksDir(home))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "auth-start-") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestAuthSessionReleaseRetiresStartLocks(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ctx := context.Background()
	for _, protected := range []bool{false, true} {
		suffix := "ordinary"
		if protected {
			suffix = "protected"
		}
		ref, err := b.PrepareAuthSession(ctx, backend.StoreKey{Owner: key.Owner, Suffix: suffix}, suffix, protected, false)
		if err != nil {
			t.Fatal(err)
		}
		b.releaseAuthStart(ref) // the container started
		if len(startLockNames(t, b.opts.Host.Home)) != 1 {
			t.Fatal("preparation did not create its start lock")
		}
		if err := b.ReleaseAuthSession(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if names := startLockNames(t, b.opts.Host.Home); len(names) != 0 {
			t.Fatalf("%s session left start locks: %v", suffix, names)
		}
	}
}

func TestFailedAuthPreparationRollsBackReservation(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	// Preparation fails after admission: the private directories it creates
	// are not owned by the configured host user.
	b.opts.Host.UID = strconv.Itoa(os.Getuid() + 1)
	if _, err := b.PrepareAuthSession(context.Background(), key, "failed", true, false); err == nil {
		t.Fatal("preparation succeeded with foreign directory ownership")
	}
	reserved, err := hoststore.ReservedIncarnations(b.opts.Host.Home)
	if err != nil {
		t.Fatal(err)
	}
	if len(reserved) != 0 {
		t.Fatalf("failed preparation retained reservations: %v", reserved)
	}
	sessions, err := os.ReadDir(filepath.Join(config.HostStateRootDir(b.opts.Host.Home), "oauth-sessions"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(sessions) != 0 || len(startLockNames(t, b.opts.Host.Home)) != 0 {
		t.Fatalf("failed preparation left session state: %d trees, locks %v", len(sessions), startLockNames(t, b.opts.Host.Home))
	}
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	if protected, err := hoststore.IdentityProtected(b.opts.Host.Home, dir); err != nil || !protected {
		t.Fatalf("rollback changed saved protection: %v, %v", protected, err)
	}
}

func TestAuthAdmissionSweepsUnreservedLeftovers(t *testing.T) {
	b, key, engines := oauthTestBackend(t)
	ctx := context.Background()
	home := b.opts.Host.Home
	live, err := b.PrepareAuthSession(ctx, key, "live", true, false)
	if err != nil {
		t.Fatal(err)
	}
	b.releaseAuthStart(live)
	engines[backend.NameDocker] = []dockercmd.InspectResponse{{Name: "/live", State: &dockercmd.ContainerState{Status: "running", Running: true}}}
	if err := util.WithFileLock(b.authStartLock(live.Incarnation), func() error { return nil }); err != nil {
		t.Fatal(err)
	}

	sessions := filepath.Join(config.HostStateRootDir(home), "oauth-sessions")
	orphan := strings.Repeat("a", 64)
	preparing := strings.Repeat("b", 64)
	for _, incarnation := range []string{orphan, preparing, "not-an-incarnation"} {
		if err := os.MkdirAll(filepath.Join(sessions, incarnation, "view"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	held, err := util.AcquireFileLock(b.authStartLock(preparing), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	strayLock := filepath.Join(config.HostLocksDir(home), "auth-start-"+util.HashString("ended")+".lock")
	if err := os.WriteFile(strayLock, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	next, err := b.PrepareAuthSession(ctx, key, "next", true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseAuthStart(next)

	for _, kept := range []string{live.Incarnation, preparing, "not-an-incarnation"} {
		if _, err := os.Stat(filepath.Join(sessions, kept)); err != nil {
			t.Errorf("swept live, preparing or unrecognized session state %s: %v", kept, err)
		}
	}
	if _, err := os.Stat(filepath.Join(sessions, orphan)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("orphaned session tree survived: %v", err)
	}
	for _, lock := range []string{b.authStartLock(live.Incarnation), b.authStartLock(preparing), b.authStartLock(next.Incarnation)} {
		if _, err := os.Stat(lock); err != nil {
			t.Errorf("swept a reserved or held start lock: %v", err)
		}
	}
	for _, lock := range []string{strayLock, b.authStartLock(orphan)} {
		if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale start lock survived: %v", err)
		}
	}
	if err := b.validateAuthReservation(live); err != nil {
		t.Fatalf("sweep displaced a live reservation: %v", err)
	}
}

func TestEphemeralProtectedIdentityIsRemovedOnceUnmountable(t *testing.T) {
	b, _, engines := oauthTestBackend(t)
	ctx := context.Background()
	name, err := backend.NewProtectedEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	key := backend.StoreKey{Owner: "claude", Suffix: name}
	ref, err := b.PrepareAuthSession(ctx, key, "ephemeral", true, false)
	if err != nil {
		t.Fatal(err)
	}
	b.releaseAuthStart(ref)
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "oauth-grant.json"), "synthetic-grant")
	// A stopped gateway awaiting removal could still be restarted with the grant.
	engines[backend.NameDocker] = []dockercmd.InspectResponse{{Name: "/enclave-gateway-stale", State: &dockercmd.ContainerState{Status: "exited"}, Config: &dockercmd.ContainerConfig{}, Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: dir, Destination: "/oauth/grant"}}}}
	if err := b.ReleaseAuthSession(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "oauth-grant.json")); err != nil {
		t.Fatalf("grant removed while a container could still mount it: %v", err)
	}

	engines[backend.NameDocker] = nil
	next, err := b.PrepareAuthSession(ctx, backend.StoreKey{Owner: "claude", Suffix: "shared-name"}, "next", true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseAuthStart(next)
	for _, path := range []string{dir, hoststore.IdentityStatePath(b.opts.Host.Home, dir), hoststore.LockPath(b.opts.Host.Home, dir)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("ended ephemeral identity survived the sweep: %s: %v", path, err)
		}
	}
}

func TestLiveEphemeralProtectedIdentitySurvivesSweep(t *testing.T) {
	b, _, engines := oauthTestBackend(t)
	ctx := context.Background()
	name, err := backend.NewProtectedEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	key := backend.StoreKey{Owner: "claude", Suffix: name}
	live, err := b.PrepareAuthSession(ctx, key, "live", true, false)
	if err != nil {
		t.Fatal(err)
	}
	b.releaseAuthStart(live)
	engines[backend.NameDocker] = []dockercmd.InspectResponse{{Name: "/live", State: &dockercmd.ContainerState{Status: "running", Running: true}, Config: &dockercmd.ContainerConfig{}}}
	next, err := b.PrepareAuthSession(ctx, backend.StoreKey{Owner: "claude", Suffix: "shared-name"}, "next", true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseAuthStart(next)
	if err := b.validateAuthReservation(live); err != nil {
		t.Fatalf("sweep removed a live ephemeral identity: %v", err)
	}
}
