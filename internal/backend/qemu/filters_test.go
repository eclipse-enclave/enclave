// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"context"
	"errors"
	"testing"

	"enclave/internal/backend"
)

// The guest has no gateway, so the backend refuses request filters before it
// prepares anything, whatever mode the request asks for.
func TestQEMURefusesRequestFilters(t *testing.T) {
	be := New(Options{})
	if be.Capabilities().RequestFilters {
		t.Fatal("the qemu backend claims to enforce request filters")
	}
	req := backend.Request{
		Network: backend.NetworkPolicy{Mode: backend.NetworkModeUnrestricted},
		Filters: []backend.FilterBundle{{Name: "gws", Policy: []byte("{}"), Credential: []byte("{}")}},
	}
	if _, err := be.Run(context.Background(), req, backend.AttachIO{}); !errors.Is(err, backend.ErrRequestFiltersUnsupported) {
		t.Fatalf("Run with filters = %v, want ErrRequestFiltersUnsupported", err)
	}
	if _, err := be.Start(context.Background(), req); !errors.Is(err, backend.ErrRequestFiltersUnsupported) {
		t.Fatalf("Start with filters = %v, want ErrRequestFiltersUnsupported", err)
	}
}
