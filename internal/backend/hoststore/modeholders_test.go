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
					if err := AdvanceMode(home, dir, first.ID, next, "cid", "gid", false); err != nil {
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
	if err := AdvanceMode(home, dir, holder.ID, "ended", "", "", false); err == nil {
		t.Fatal("pending skipped finalization")
	}
	if err := AdvanceMode(home, dir, holder.ID, "live", "cid", "gid", true); err != nil {
		t.Fatal(err)
	}
	if err := AdvanceMode(home, dir, holder.ID, "ended", "", "", false); err == nil {
		t.Fatal("live skipped finalization")
	}
	if err := WithModeHolders(home, dir, func(holders *[]ModeHolder) error {
		got := (*holders)[0]
		if got.OwnerPID != 0 || got.ContainerID != "cid" || got.GatewayID != "gid" {
			t.Fatalf("detached handoff: %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"finalizing", "ended"} {
		if err := AdvanceMode(home, dir, holder.ID, state, "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := AdvanceMode(home, dir, holder.ID, "live", "", "", false); err == nil {
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
