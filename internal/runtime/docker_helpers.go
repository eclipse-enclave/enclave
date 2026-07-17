// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"strings"

	"enclave/internal/backend"
)

func bindMount(source string, target string, readOnly bool) backend.Mount {
	return backend.Mount{
		Type:          backend.MountTypeBind,
		Source:        source,
		ContainerPath: target,
		ReadOnly:      readOnly,
	}
}

func lookupEnv(entries []string, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	prefix := key + "="
	for _, entry := range entries {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
	}
	return "", false
}

func envHasKey(env []string, key string) bool {
	_, ok := lookupEnv(env, key)
	return ok
}
