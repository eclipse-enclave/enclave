// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	backendqemu "enclave/internal/backend/qemu"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

type modeCleanupBackend struct {
	backend.Backend
	remove        func(context.Context, backend.SessionRef) error
	removeGateway func(context.Context, string, string, string) (bool, error)
}

func (b modeCleanupBackend) InspectModeSession(ctx context.Context, holderID, id string) (*backend.SessionRef, bool, error) {
	return backenddocker.New(backenddocker.Options{}).InspectModeSession(ctx, holderID, id)
}

func (b modeCleanupBackend) RemoveModeGateway(ctx context.Context, id, session, hash string) (bool, error) {
	return b.removeGateway(ctx, id, session, hash)
}

func (b modeCleanupBackend) Name() string { return "docker" }
func (b modeCleanupBackend) Remove(ctx context.Context, ref backend.SessionRef) error {
	return b.remove(ctx, ref)
}

func TestModeCleanupRequiresOwnerDeathAndContainerAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, state, engine, response string
		owned, fresh, dryRun          bool
		wantKept, wantFinalize        bool
		wantError                     bool
	}{
		{name: "crashed pending", state: "pending", response: "absent"},
		{name: "exited live finalizes", state: "live", response: "exited", wantFinalize: true},
		{name: "crashed finalizer finalizes", state: "finalizing", response: "exited", wantFinalize: true},
		{name: "lost ended write", state: "finalizing", response: "absent"},
		{name: "unreachable", state: "live", response: "unreachable", wantKept: true, wantError: true},
		{name: "owner still finalizing", state: "finalizing", owned: true, response: "absent", wantKept: true},
		{name: "exited live owner", state: "live", owned: true, response: "exited", wantKept: true},
		{name: "running", state: "live", response: "running", wantKept: true},
		{name: "recent pending", state: "pending", fresh: true, response: "absent", wantKept: true},
		{name: "other engine", state: "live", engine: "podman", response: "unreachable", wantKept: true},
		{name: "dry run absent", state: "pending", response: "absent", dryRun: true, wantKept: true},
		{name: "dry run exited", state: "live", response: "exited", dryRun: true, wantKept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			engine := tc.engine
			if engine == "" {
				engine = "docker"
			}
			holder := hoststore.ModeHolder{ID: "holder", Session: "session", Engine: engine}
			if err := hoststore.ReserveMode(home, dir, holder, nil); err != nil {
				t.Fatal(err)
			}
			if err := hoststore.WithModeHolders(home, dir, func(holders *[]hoststore.ModeHolder) error {
				h := &(*holders)[0]
				h.State, h.ContainerID = tc.state, "cid"
				h.UpdatedAt = time.Now().Add(-2 * modeStartTimeout)
				if tc.owned {
					h.OwnerPID = os.Getpid()
				}
				if tc.fresh {
					h.UpdatedAt = time.Now()
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\n"
			switch tc.response {
			case "absent":
				script += "echo 'No such container: cid' >&2\nexit 1\n"
			case "unreachable":
				script += "echo 'daemon unavailable' >&2\nexit 1\n"
			default:
				script += fmt.Sprintf("printf '%%s\\n' '%s'\n", fmt.Sprintf(`{"Id":"cid","Name":"/session","Config":{"Labels":{"%s":"holder"}},"State":{"Running":%t}}`, model.LabelModeHolder, tc.response == "running"))
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			file := config.HostLockPath(home, "mode-"+util.HashString(dir)+".json")
			before, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			finalized := false
			be := modeCleanupBackend{remove: func(_ context.Context, ref backend.SessionRef) error {
				if ref.ID != "cid" || ref.Name != "session" {
					t.Fatalf("wrong finalizer target: %+v", ref)
				}
				// This also proves Remove is outside the config-store lock.
				if err := hoststore.WithModeHolders(home, dir, func(holders *[]hoststore.ModeHolder) error {
					if len(*holders) != 1 || (*holders)[0].State != tc.state {
						t.Fatal("engine exit promoted the record before finalization")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				finalized = true
				if err := hoststore.AdvanceMode(home, dir, holder.ID, "finalizing", "", false); err != nil {
					return err
				}
				return hoststore.AdvanceMode(home, dir, holder.ID, "ended", "", false)
			}}
			err = cleanupModeStore(home, dir, be, tc.dryRun)
			if (err != nil) != tc.wantError || finalized != tc.wantFinalize {
				t.Fatalf("finalized=%t err=%v", finalized, err)
			}
			records, err := hoststore.ModeRecords(home)
			if err != nil || (len(records) > 0) != tc.wantKept {
				t.Fatalf("kept=%t records=%+v err=%v", tc.wantKept, records, err)
			}
			if tc.dryRun {
				after, err := os.Stat(file)
				if err != nil || !before.ModTime().Equal(after.ModTime()) {
					t.Fatalf("dry run rewrote the ledger: %v", err)
				}
			}
		})
	}
}

func TestPendingModeDiscoveryFindsRenamedContainerByHolder(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + util.ShellQuote(log) + "\n" +
		"if [ \"$1\" = ps ]; then echo cid; else printf '%s\\n' " +
		util.ShellQuote(fmt.Sprintf(`{"Id":"cid","Name":"/renamed","Config":{"Labels":{"%s":"holder"}},"State":{"Running":true}}`, model.LabelModeHolder)) + "; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ref, running, err := backenddocker.New(backenddocker.Options{}).InspectModeSession(context.Background(), "holder", "")
	if err != nil || !running || ref == nil || ref.Name != "renamed" {
		t.Fatalf("ref=%+v running=%t err=%v", ref, running, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "ps --no-trunc --format {{.ID}} --all --filter label="+model.LabelModeHolder+"=holder") {
		t.Fatalf("discovery did not use the holder label: %s", calls)
	}
}

func TestModeContainerQEMUDoesNotInferPendingAbsence(t *testing.T) {
	if _, _, err := backendqemu.New(backendqemu.Options{}).InspectModeSession(context.Background(), "holder", ""); err == nil {
		t.Fatal("pending QEMU guest without a recorded PID was inferred absent")
	}
	ref, running, err := backendqemu.New(backendqemu.Options{}).InspectModeSession(context.Background(), "holder", strconv.Itoa(os.Getpid()))
	if err != nil || ref == nil || !running {
		t.Fatalf("live QEMU PID: ref=%+v running=%t err=%v", ref, running, err)
	}
}

func TestAbsentProtectedCleanupRemovesOrphanGatewayBeforePrivateIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		found, correlated, removeFails, wantKept bool
	}{
		{name: "one correlated", found: true, correlated: true},
		{name: "one direct correlation", found: true},
		{name: "zero correlated", correlated: true},
		{name: "zero legacy", wantKept: true},
		{name: "teardown error", found: true, correlated: true, removeFails: true, wantKept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			home := t.TempDir()
			key := backend.StoreKey{Owner: "claude", ProjectHash: "syntheticproject", Suffix: "nonce"}
			dir, err := hoststore.ResolveDir(home, key, backend.StoreKindConfig)
			if err != nil {
				t.Fatal(err)
			}
			holder := hoststore.ModeHolder{ID: "holder-one", Session: "session", Tool: key.Owner, ProjectHash: key.ProjectHash, Protected: true, Ephemeral: true, GatewayCorrelated: tc.correlated, Engine: "docker"}
			if err := hoststore.ReserveMode(home, dir, holder, nil); err != nil {
				t.Fatal(err)
			}
			if err := hoststore.WithModeHolders(home, dir, func(holders *[]hoststore.ModeHolder) error {
				(*holders)[0].UpdatedAt = time.Now().Add(-2 * modeStartTimeout)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			native := hoststore.Dir(home, backend.StoreKey{Owner: key.Owner, Suffix: "ephemeral-" + key.Suffix}, backend.StoreKindAuth)
			views := filepath.Join(config.HostProjectToolDir(home, key.ProjectHash, key.Owner), "auth-views")
			own, sibling := filepath.Join(views, holder.ID+"-published"), filepath.Join(views, "holder-two-published")
			for _, path := range []string{native, own, sibling} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nif [ \"$1\" = ps ]; then exit 0; fi\necho 'No such container' >&2\nexit 1\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			removedGateway := false
			be := modeCleanupBackend{remove: func(context.Context, backend.SessionRef) error {
				t.Fatal("absent cleanup tried removing a possibly reused agent name")
				return nil
			}, removeGateway: func(_ context.Context, id, session, hash string) (bool, error) {
				if id != holder.ID || session != holder.Session || hash != holder.ProjectHash {
					t.Fatal("wrong orphan session")
				}
				for _, path := range []string{native, own} {
					if _, err := os.Stat(path); err != nil {
						t.Fatal("private state deleted before orphan gateway removal")
					}
				}
				if err := hoststore.WithModeHolders(home, dir, func(holders *[]hoststore.ModeHolder) error {
					if len(*holders) != 1 {
						t.Fatal("holder released before orphan teardown")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if tc.removeFails {
					return tc.found, fmt.Errorf("synthetic orphan removal failure")
				}
				removedGateway = tc.found
				return tc.found, nil
			}}
			err = cleanupModeStore(home, dir, be, false)
			if (err != nil) != tc.wantKept {
				t.Fatal(err)
			}
			for _, path := range []string{native, own} {
				_, err := os.Stat(path)
				if (!tc.wantKept) != os.IsNotExist(err) {
					t.Fatal("private state lifetime disagrees with orphan teardown")
				}
			}
			if removedGateway != (tc.found && !tc.removeFails) {
				t.Fatal("gateway removal result disagrees with evidence")
			}
			if _, err := os.Stat(sibling); err != nil {
				t.Fatal("cleanup deleted sibling view")
			}
			records, err := hoststore.ModeRecords(home)
			if err != nil || (len(records) > 0) != tc.wantKept {
				t.Fatal("holder lifetime disagrees with teardown")
			}
		})
	}
}
