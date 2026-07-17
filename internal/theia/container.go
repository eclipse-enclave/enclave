// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package theia

import (
	"context"

	"enclave/internal/backend"
)

// ContainerYoloEnabled reports whether the named container was started with
// YOLO on, read from the label set at creation. Absent (or any inspect error)
// means YOLO was off, so the always_allow preferences are correctly withheld
// on attach.
func ContainerYoloEnabled(ctx context.Context, be backend.Backend, name string) bool {
	sess, err := be.Inspect(ctx, backend.SessionRef{Name: name})
	if err != nil || sess == nil {
		return false
	}
	return sess.Yolo
}
