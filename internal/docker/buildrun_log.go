// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import "enclave/internal/logx"

// buildWarnf emits RunBuild's user-facing warnings; kept apart so the build
// policy itself has no logging dependency.
var buildWarnf = logx.Warnf
