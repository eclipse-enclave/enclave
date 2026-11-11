// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"path/filepath"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/network"
)

func TestAppContextPolicyNetworkModeReadsPolicyFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	projectDir := t.TempDir()
	ctx := NewAppContext(model.Paths{}, projectDir)
	project, err := ctx.Project()
	if err != nil {
		t.Fatal(err)
	}
	hostHome, err := config.ResolveHostHome()
	if err != nil {
		t.Fatal(err)
	}

	assertMode := func(want string) {
		t.Helper()
		got, err := NewAppContext(model.Paths{}, projectDir).PolicyNetworkMode()
		if err != nil || got != want {
			t.Fatalf("PolicyNetworkMode() = %q, %v; want %q", got, err, want)
		}
	}
	assertMode(model.NetworkModeRestricted)

	if err := network.SavePolicy(network.GlobalPolicyPath(hostHome), network.Policy{Mode: model.NetworkModeUnrestricted}); err != nil {
		t.Fatal(err)
	}
	assertMode(model.NetworkModeUnrestricted)

	if err := network.SavePolicy(network.ProjectPolicyPath(hostHome, project.Hash), network.Policy{Mode: model.NetworkModeRestricted}); err != nil {
		t.Fatal(err)
	}
	assertMode(model.NetworkModeRestricted)
}
