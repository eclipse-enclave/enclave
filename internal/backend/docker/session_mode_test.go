// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestExternalFinalizerRespectsHostOwnership(t *testing.T) {
	for _, state := range []string{"live", "finalizing", "ended"} {
		for _, owned := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/owned=%t", state, owned), func(t *testing.T) {
				home, dir, bin := t.TempDir(), t.TempDir(), t.TempDir()
				info := dockercmd.InspectResponse{ID: "cid", Name: "/holder", Config: &dockercmd.ContainerConfig{
					Env:    []string{model.EnvToolConfigDir + "=/config"},
					Labels: map[string]string{model.LabelModeHolder: "holder"},
				}, Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: dir, Destination: "/config"}}}
				data, err := json.Marshal(info)
				if err != nil {
					t.Fatal(err)
				}
				script := "#!/bin/sh\nprintf '%s\\n' " + util.ShellQuote(string(data)) + "\n"
				if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				pid := 0
				if owned {
					pid = os.Getpid()
				}
				holder := hoststore.ModeHolder{ID: "holder", Session: "holder", Engine: "docker", OwnerPID: pid}
				if err := hoststore.ReserveMode(home, dir, holder, nil); err != nil {
					t.Fatal(err)
				}
				for _, next := range []string{"live", "finalizing", "ended"} {
					if err := hoststore.AdvanceMode(home, dir, holder.ID, next, "cid", false); err != nil {
						t.Fatal(err)
					}
					if next == state {
						break
					}
				}
				be := New(Options{Host: model.Host{Home: home}})
				finish, reconcile, _, err := be.beginContainerModeFinalization(context.Background(), "holder")
				if owned && state == "finalizing" {
					if err == nil {
						t.Fatal("parallel finalizer was not refused")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				wantReconcile := !owned && state != "ended"
				if reconcile != wantReconcile {
					t.Fatalf("reconcile=%t want=%t", reconcile, wantReconcile)
				}
				if wantReconcile {
					// A second CLI must not perform a parallel finalizer or end
					// the reservation while the first one is still writing.
					_, secondSync, _, err := be.beginContainerModeFinalization(context.Background(), "holder")
					if err == nil || secondSync {
						t.Fatalf("parallel finalizer: sync=%t err=%v", secondSync, err)
					}
					if err := hoststore.ReserveMode(home, dir, hoststore.ModeHolder{ID: "opposite", Session: "opposite", Protected: true}, nil); err == nil {
						t.Fatal("parallel CLI released the active finalizer")
					}
					finish.release()
					if err := hoststore.ReserveMode(home, dir, hoststore.ModeHolder{ID: "aborted-opposite", Session: "opposite", Protected: true}, nil); err == nil {
						t.Fatal("aborting a finalizer ended its workload reservation")
					}
					replacement, sync, _, err := be.beginContainerModeFinalization(context.Background(), "holder")
					if err != nil || !sync {
						t.Fatalf("sequential takeover: sync=%t err=%v", sync, err)
					}
					finish.release()
					if _, _, _, err := be.beginContainerModeFinalization(context.Background(), "holder"); err == nil {
						t.Fatal("old release cleared a later active finalizer")
					}
					finish = replacement
				}
				if err := finish(true); err != nil {
					t.Fatal(err)
				}
				err = hoststore.ReserveMode(home, dir, hoststore.ModeHolder{ID: "after", Session: "after", Protected: true}, nil)
				wantBlocked := owned && state != "ended"
				if (err != nil) != wantBlocked {
					t.Fatalf("after finalizer: blocked=%t err=%v", wantBlocked, err)
				}
			})
		}
	}
}
