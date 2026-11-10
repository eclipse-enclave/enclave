// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
)

func TestModeReservationMatrix(t *testing.T) {
	for _, protected := range []bool{false, true} {
		for _, state := range []string{"pending", "live", "finalizing", "ended"} {
			for _, sameMode := range []bool{false, true} {
				home, dir := t.TempDir(), filepath.Join(t.TempDir(), "config")
				first := ModeHolder{ID: "first", Session: "holder", Protected: protected}
				if err := ReserveMode(home, dir, first, nil); err != nil {
					t.Fatal(err)
				}
				for _, next := range []string{"live", "finalizing", "ended"} {
					if state == "pending" {
						break
					}
					if err := AdvanceMode(home, dir, first.ID, next, "cid", false); err != nil {
						t.Fatal(err)
					}
					if state == next {
						break
					}
				}
				second := ModeHolder{ID: "second", Session: "candidate", Protected: protected == sameMode}
				err := ReserveMode(home, dir, second, nil)
				wantRefusal := !sameMode && state != "ended"
				if (err != nil) != wantRefusal || err != nil && !strings.Contains(err.Error(), "holder") {
					t.Fatalf("protected=%t state=%s same=%t: %v", protected, state, sameMode, err)
				}
			}
		}
	}
}

func TestModeAdmissionAndPublicationShareOneLock(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	protectedErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		protectedErr <- ReserveMode(home, dir, ModeHolder{ID: "protected", Session: "protected", Protected: true}, func([]ModeHolder) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ordinaryErr := make(chan error, 1)
	go func() {
		ordinaryErr <- ReserveMode(home, dir, ModeHolder{ID: "ordinary", Session: "ordinary"}, nil)
	}()
	close(release)
	wg.Wait()
	if err := <-protectedErr; err != nil {
		t.Fatal(err)
	}
	if err := <-ordinaryErr; err == nil {
		t.Fatal("ordinary reservation crossed protected admission/publication")
	}
}

func TestModeDiscoveryFailureDoesNotReserve(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	unreachable := errors.New("engine unreachable")
	err := ReserveMode(home, dir, ModeHolder{ID: "protected", Session: "protected", Protected: true}, func([]ModeHolder) error { return unreachable })
	if !errors.Is(err, unreachable) {
		t.Fatalf("discovery: %v", err)
	}
	if err := ReserveMode(home, dir, ModeHolder{ID: "ordinary", Session: "ordinary"}, nil); err != nil {
		t.Fatalf("failed admission left a holder: %v", err)
	}
}

func TestModeTransitionsAndDetachedHandoff(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	holder := ModeHolder{ID: "holder", Session: "holder", OwnerPID: os.Getpid()}
	if err := ReserveMode(home, dir, holder, nil); err != nil {
		t.Fatal(err)
	}
	if err := AdvanceMode(home, dir, holder.ID, "ended", "", false); err == nil {
		t.Fatal("pending skipped finalization")
	}
	if err := AdvanceMode(home, dir, holder.ID, "live", "cid", true); err != nil {
		t.Fatal(err)
	}
	if err := AdvanceMode(home, dir, holder.ID, "ended", "", false); err == nil {
		t.Fatal("live skipped finalization")
	}
	if err := WithModeHolders(home, dir, func(holders *[]ModeHolder) error {
		got := (*holders)[0]
		if got.OwnerPID != 0 || got.ContainerID != "cid" {
			t.Fatalf("detached handoff: %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"finalizing", "ended"} {
		if err := AdvanceMode(home, dir, holder.ID, state, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := AdvanceMode(home, dir, holder.ID, "live", "", false); err == nil {
		t.Fatal("ended holder resurrected")
	}
	if !ModeOwnerAlive(os.Getpid()) || ModeOwnerAlive(0) {
		t.Fatal("host owner liveness")
	}
}

func TestModeOwnerProbe(t *testing.T) {
	if os.Getenv("ENCLAVE_TEST_MODE_OWNER") == "child" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestModeOwnerProbe$")
	cmd.Env = append(os.Environ(), "ENCLAVE_TEST_MODE_OWNER=child")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	alive := ModeOwnerAlive(cmd.Process.Pid)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if !alive || ModeOwnerAlive(cmd.Process.Pid) {
		t.Fatal("owner process death was not detected")
	}
}

func TestEndModePinsFinalizerAndRetainsFailedTeardown(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	id := "end-owner"
	if err := ReserveMode(home, dir, ModeHolder{ID: id, Session: id, OwnerPID: os.Getpid()}, nil); err != nil {
		t.Fatal(err)
	}
	called := false
	finalize := func() error { called = true; return nil }
	if err := EndMode(home, dir, id, finalize); err == nil || called {
		t.Fatal("unconfirmed pending holder finalized")
	}
	if err := AdvanceMode(home, dir, id, "live", "container", false); err != nil {
		t.Fatal(err)
	}
	if err := EndMode(home, dir, id, finalize); err == nil || called {
		t.Fatal("running holder finalized")
	}
	if err := AdvanceMode(home, dir, id, "finalizing", "", false); err != nil {
		t.Fatal(err)
	}
	if err := EndMode(home, dir, id, func() error { return errors.New("synthetic deletion failure") }); err == nil {
		t.Fatal("failed teardown ended holder")
	}
	if err := ReserveMode(home, dir, ModeHolder{ID: "opposite", Session: "opposite", Protected: true}, nil); err == nil {
		t.Fatal("failed teardown released reservation")
	}
	if err := WithModeHolders(home, dir, func(holders *[]ModeHolder) error {
		if (*holders)[0].OwnerPID != 0 {
			t.Fatal("failed synchronous finalizer kept live ownership")
		}
		(*holders)[0].OwnerPID = os.Getpid()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := EndMode(home, dir, id, finalize); err != nil || !called {
		t.Fatal("confirmed holder did not finalize")
	}
	called = false
	if err := EndMode(home, dir, id, finalize); err == nil || called {
		t.Fatal("deleted holder regained finalization authority")
	}
}

func TestEndModeRemovesOwnViewsAndOnlyExplicitEphemeralIdentity(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		t.Run(map[bool]string{false: "persistent identity", true: "ephemeral identity"}[ephemeral], func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			home := t.TempDir()
			key := backend.StoreKey{Owner: "claude", ProjectHash: "syntheticproject", Suffix: "session-key"}
			dir, err := ResolveDir(home, key, backend.StoreKindConfig)
			if err != nil {
				t.Fatal(err)
			}
			holder := ModeHolder{ID: "own-id", Session: "session", Tool: key.Owner, ProjectHash: key.ProjectHash, Protected: true, Ephemeral: ephemeral, OwnerPID: os.Getpid()}
			if err := ReserveMode(home, dir, holder, nil); err != nil {
				t.Fatal(err)
			}
			if err := AdvanceMode(home, dir, holder.ID, "finalizing", "", false); err != nil {
				t.Fatal(err)
			}
			views := filepath.Join(config.HostProjectToolDir(home, key.ProjectHash, key.Owner), "auth-views")
			own := filepath.Join(views, "own-id-random")
			stage := filepath.Join(views, ".own-id-stage")
			sibling := filepath.Join(views, "sibling-id-random")
			native := Dir(home, backend.StoreKey{Owner: key.Owner, Suffix: "ephemeral-" + key.Suffix}, backend.StoreKindAuth)
			named := Dir(home, backend.StoreKey{Owner: key.Owner, Suffix: "ephemeral-personal"}, backend.StoreKindAuth)
			for _, path := range []string{own, stage, sibling, native, named} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := EndMode(home, dir, holder.ID, nil); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{own, stage} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("owned view survived end")
				}
			}
			for _, path := range []string{sibling, named} {
				if _, err := os.Stat(path); err != nil {
					t.Fatal("end removed sibling or named identity")
				}
			}
			_, err = os.Stat(native)
			if ephemeral != os.IsNotExist(err) {
				t.Fatalf("native deletion did not follow explicit ephemeral flag: %v", err)
			}
		})
	}
}
