// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"fmt"
	"strings"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

func (b *Backend) CheckProtectedAuthAdmission(ctx context.Context, key backend.StoreKey, approved map[string]string) error {
	storeKey := key.Suffix
	if storeKey == "" {
		storeKey = "default"
	}
	for _, engine := range dockercmd.DetectCLIs() {
		filters := dockercmd.NewFilters()
		filters.Add("status", "running")
		filters.Add("label", model.LabelAgent+"="+key.Owner)
		filters.Add("label", model.LabelHash+"="+key.ProjectHash)
		containers, err := dockercmd.ContainerListForEngine(ctx, engine, dockercmd.ListOptions{Filters: filters})
		if err != nil {
			return fmt.Errorf("--protect-auth cannot check %s sessions: %w", engine, err)
		}
		for _, container := range containers {
			if existing := container.Labels[model.LabelConfigKey]; existing != "" && existing != storeKey {
				continue
			}
			session, managed := sessionFromSummary(container)
			if !managed || approved[container.Labels[model.LabelModeHolder]] == engine {
				continue
			}
			return fmt.Errorf("session %s (%s) holds this config store without a protected reservation; end it first", strings.TrimPrefix(session.Ref.Name, "/"), engine)
		}
	}
	return nil
}
