// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "strings"

// EffectiveBuildIdentity returns the UID and GID baked into the runtime image:
// --build-uid and --build-gid when given, otherwise the host's.
func EffectiveBuildIdentity(host Host, opts BuildOptions) (uid string, gid string) {
	uid = strings.TrimSpace(opts.BuildUID)
	if uid == "" {
		uid = host.UID
	}
	gid = strings.TrimSpace(opts.BuildGID)
	if gid == "" {
		gid = host.GID
	}
	return uid, gid
}
