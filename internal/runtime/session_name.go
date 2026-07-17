// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/model"
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeSessionName normalises a user-provided session name to a value safe
// for use in Docker container names.
func sanitizeSessionName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = nonAlphanumeric.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if len(name) > 32 {
		name = name[:32]
		name = strings.TrimRight(name, "-")
	}
	return name
}

func isGatewaySidecar(name string) bool {
	return strings.HasSuffix(name, model.GatewayContainerSuffix)
}

// nextSessionName scans existing containers with the given tool and project
// hash prefix and returns the next sequential numeric name ("1", "2", ...).
func (r *Runtime) nextSessionName() string {
	if r.backend == nil {
		return "1"
	}
	prefix := model.AppName + "-" + r.profile.Name + "-" + r.project.Hash + "-"
	sessions, err := r.backend.List(context.Background(), backend.SessionFilter{
		All:        true,
		NamePrefix: prefix,
	})
	if err != nil {
		return "1"
	}
	max := 0
	for _, s := range sessions {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			rawName := strings.TrimPrefix(s.Ref.Name, "/")
			if !strings.HasPrefix(rawName, prefix) || isGatewaySidecar(rawName) {
				continue
			}
			name = strings.TrimPrefix(rawName, prefix)
		}
		if n, err := strconv.Atoi(name); err == nil && n > max {
			max = n
		}
	}
	return strconv.Itoa(max + 1)
}
