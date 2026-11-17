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

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/testutil"
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
					if next == "ended" {
						testutil.EndModeHolder(t, home, dir, holder.ID)
					} else if err := hoststore.AdvanceMode(home, dir, holder.ID, next, "cid", false); err != nil {
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

func TestProtectedAdmissionAcceptsPodmanSiblingThroughDockerShim(t *testing.T) {
	bin := t.TempDir()
	info := dockercmd.InspectResponse{ID: "podman-cid", Name: "/protected-sibling",
		Config: &dockercmd.ContainerConfig{Labels: map[string]string{
			model.LabelAgent: "claude", model.LabelHash: "project", model.LabelConfigKey: "default", model.LabelModeHolder: "holder",
		}}, State: &dockercmd.ContainerState{Running: true, Status: "running"}}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo 'podman version 4.3.1';;\nps) echo podman-cid;;\n*) printf '%s\\n' " + util.ShellQuote(string(data)) + ";;\nesac\n"
	for _, engine := range []string{"docker", "podman"} {
		if err := os.WriteFile(filepath.Join(bin, engine), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	if got := dockercmd.DetectCLIs(); len(got) != 1 || got[0] != "podman" {
		t.Fatalf("shim detection control: %v", got)
	}
	be := New(Options{Engine: "podman"})
	err = be.CheckProtectedAuthAdmission(context.Background(), backend.StoreKey{Owner: "claude", ProjectHash: "project"}, map[string]string{"holder": "podman"})
	if err != nil {
		t.Fatalf("known protected Podman sibling refused through docker shim: %v", err)
	}
}

func TestFailedStopAllowsForceRemoval(t *testing.T) {
	for _, failure := range []string{"stop", "reconcile"} {
		for _, labeled := range []bool{false, true} {
			name := "legacy"
			if labeled {
				name = "holder"
			}
			t.Run(failure+"/"+name, func(t *testing.T) {
				home, dir, bin := t.TempDir(), t.TempDir(), t.TempDir()
				labels := map[string]string{model.LabelAgent: "claude"}
				if labeled {
					labels[model.LabelModeHolder] = "holder"
					if err := hoststore.ReserveMode(home, dir, hoststore.ModeHolder{ID: "holder", Session: "session", Engine: "docker"}, nil); err != nil {
						t.Fatal(err)
					}
					if err := hoststore.AdvanceMode(home, dir, "holder", "live", "cid", true); err != nil {
						t.Fatal(err)
					}
				}
				info := dockercmd.InspectResponse{ID: "cid", Name: "/session",
					Config: &dockercmd.ContainerConfig{Labels: labels, Env: []string{model.EnvToolConfigDir + "=/config"}},
					Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: dir, Destination: "/config"}},
					State:  &dockercmd.ContainerState{Running: true, Status: "running"}}
				if failure == "reconcile" {
					// Reconciliation rejects the invalid path after a successful stop.
					info.Config.Env = append(info.Config.Env, model.EnvAuthDir+"=/auth", model.EnvAuthFiles+"=../outside")
					info.Mounts = append(info.Mounts, dockercmd.MountPoint{Type: dockercmd.MountTypeBind, Source: t.TempDir(), Destination: "/auth"})
				}
				data, err := json.Marshal(info)
				if err != nil {
					t.Fatal(err)
				}
				stop := "exit 0"
				if failure == "stop" {
					stop = "echo 'synthetic graceful stop failure' >&2; exit 1"
				}
				script := "#!/bin/sh\ncase \"$1\" in\nstop) " + stop + ";;\nrm) exit 0;;\n*) printf '%s\\n' " + util.ShellQuote(string(data)) + ";;\nesac\n"

				if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin)
				be := New(Options{Host: model.Host{Home: home}})
				ref := backend.SessionRef{Name: "session"}
				if err := be.Stop(context.Background(), ref, backend.StopOptions{Finalize: true}); err == nil {
					t.Fatal("stop/finalization failure control did not fail")
				}
				// app.stopContainer intentionally invokes this even when Stop fails.
				if err := be.RemoveWithoutFinalize(context.Background(), ref); err != nil {
					t.Fatalf("failed synchronous Stop retained a self-owned claim and blocked force removal: %v", err)
				}
			})
		}
	}
}

func TestPrunedEndedHolderCannotRegainFinalization(t *testing.T) {
	home, dir, bin := t.TempDir(), t.TempDir(), t.TempDir()
	info := dockercmd.InspectResponse{ID: "old-cid", Name: "/old-session", Config: &dockercmd.ContainerConfig{
		Env: []string{model.EnvToolConfigDir + "=/config"}, Labels: map[string]string{model.LabelModeHolder: "old-holder"},
	}, Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: dir, Destination: "/config"}}}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf '%s\\n' "+util.ShellQuote(string(data))+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	holder := hoststore.ModeHolder{ID: "old-holder", Session: "old-session", Engine: "docker"}
	if err := hoststore.ReserveMode(home, dir, holder, nil); err != nil {
		t.Fatal(err)
	}
	if err := hoststore.AdvanceMode(home, dir, holder.ID, "finalizing", "old-cid", false); err != nil {
		t.Fatal(err)
	}
	testutil.EndModeHolder(t, home, dir, holder.ID)
	be := New(Options{Host: model.Host{Home: home}})
	_, reconcile, _, err := be.beginContainerModeFinalization(context.Background(), "old-session")
	if err != nil || reconcile {
		t.Fatalf("ended control: reconcile=%t err=%v", reconcile, err)
	}
	// Ending deletes the record; an old labeled container must remain unable
	// to finalize after an opposite-mode holder is admitted.
	if err := hoststore.ReserveMode(home, dir, hoststore.ModeHolder{ID: "new-holder", Session: "new-session", Engine: "docker", Protected: true}, nil); err != nil {
		t.Fatal(err)
	}
	_, reconcile, _, err = be.beginContainerModeFinalization(context.Background(), "old-session")
	if err == nil && reconcile {
		t.Fatal("pruning ended record let an old labeled container reconcile during the opposite reservation")
	}
}
