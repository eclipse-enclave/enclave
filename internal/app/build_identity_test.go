// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestEffectiveBuildIdentityUsesContainerIdentity(t *testing.T) {
	// Rootless Docker maps the host user to container UID 0, so the image must
	// bake the agent user at 0:0 rather than at the host ids.
	host := model.Host{UID: "1000", GID: "1000", ContainerUID: "0", ContainerGID: "0"}
	uid, gid := effectiveBuildIdentity(host, model.BuildOptions{})
	if uid != "0" || gid != "0" {
		t.Fatalf("expected build identity 0:0, got %s:%s", uid, gid)
	}
}

func TestEffectiveBuildIdentityFallsBackToHostIdentity(t *testing.T) {
	host := model.Host{UID: "1000", GID: "1000"}
	uid, gid := effectiveBuildIdentity(host, model.BuildOptions{})
	if uid != "1000" || gid != "1000" {
		t.Fatalf("expected build identity 1000:1000, got %s:%s", uid, gid)
	}
}

func TestEffectiveBuildIdentityExplicitFlagsWin(t *testing.T) {
	host := model.Host{UID: "1000", GID: "1000", ContainerUID: "0", ContainerGID: "0"}
	uid, gid := effectiveBuildIdentity(host, model.BuildOptions{BuildUID: "2000", BuildGID: "2001"})
	if uid != "2000" || gid != "2001" {
		t.Fatalf("expected explicit build identity 2000:2001, got %s:%s", uid, gid)
	}
}

func TestEffectiveBuildIdentityHashSuffixDistinguishesContainerIdentity(t *testing.T) {
	rootful := model.Host{UID: "1000", GID: "1000", ContainerUID: "1000", ContainerGID: "1000"}
	rootless := model.Host{UID: "1000", GID: "1000", ContainerUID: "0", ContainerGID: "0"}
	rootfulSuffix := appendEffectiveBuildIdentityHashSuffix("", rootful, model.BuildOptions{})
	rootlessSuffix := appendEffectiveBuildIdentityHashSuffix("", rootless, model.BuildOptions{})
	if rootfulSuffix == rootlessSuffix {
		t.Fatalf("expected rootful and rootless image hashes to differ, both were %q", rootfulSuffix)
	}
}

func TestResolveContainerIdentityKeepsHostIdentityForNonDockerBackend(t *testing.T) {
	// The QEMU backend shares host ownership over 9p and never consults Docker,
	// so it must not inherit the rootless container identity.
	host := model.Host{UID: "1000", GID: "1000"}
	uid, gid := resolveContainerIdentity(backend.NameQEMU, host)
	if uid != "1000" || gid != "1000" {
		t.Fatalf("expected host identity 1000:1000 for qemu backend, got %s:%s", uid, gid)
	}
}
