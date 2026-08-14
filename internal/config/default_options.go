// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import "enclave/internal/model"

func DefaultOptions() model.Options {
	return model.Options{
		RunOptions: model.RunOptions{
			Tool:             "claude",
			Backend:          "docker",
			HostConfig:       model.HostConfigNone,
			NetworkLog:       model.NetworkLogCoarse,
			ProjectMount:     model.ProjectMountWritable,
			WorktreeMetadata: model.WorktreeMetadataFollow,
			Persist:          true,
		},
		AuthOptions: model.AuthOptions{
			AuthScope:    model.AuthScopeShared,
			SecretsScope: model.SecretsScopeBoth,
		},
		BuildOptions: model.BuildOptions{
			ImageName: model.ImageName,
			Progress:  model.BuildProgressCompact,
		},
	}
}
