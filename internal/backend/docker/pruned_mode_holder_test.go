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

	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestAstraPrunedEndedHolderDoesNotRegainFinalization(t *testing.T) {
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
	for _, state := range []string{"finalizing", "ended"} {
		if err := hoststore.AdvanceMode(home, dir, holder.ID, state, "old-cid", false); err != nil {
			t.Fatal(err)
		}
	}
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
