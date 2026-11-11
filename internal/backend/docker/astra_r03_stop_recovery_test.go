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
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestAstraFailedStopAllowsItsSynchronousForceRemoval(t *testing.T) {
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
