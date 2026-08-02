// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "testing"

func TestHostContainerIdentityFallsBackToHostIdentity(t *testing.T) {
	host := Host{UID: "1000", GID: "1001"}
	uid, gid := host.ContainerIdentity()
	if uid != "1000" || gid != "1001" {
		t.Fatalf("expected host identity fallback 1000:1001, got %s:%s", uid, gid)
	}
}

func TestHostContainerIdentityPrefersContainerIdentity(t *testing.T) {
	host := Host{UID: "1000", GID: "1001", ContainerUID: "0", ContainerGID: "0"}
	uid, gid := host.ContainerIdentity()
	if uid != "0" || gid != "0" {
		t.Fatalf("expected container identity 0:0, got %s:%s", uid, gid)
	}
}

func TestHostContainerIdentityIgnoresBlankContainerIdentity(t *testing.T) {
	host := Host{UID: "1000", GID: "1001", ContainerUID: "  ", ContainerGID: ""}
	uid, gid := host.ContainerIdentity()
	if uid != "1000" || gid != "1001" {
		t.Fatalf("expected host identity fallback 1000:1001, got %s:%s", uid, gid)
	}
}
