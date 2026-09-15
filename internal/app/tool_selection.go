// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"enclave/internal/config"
	"enclave/internal/model"
)

func listToolExtensions(paths model.Paths) ([]model.Extension, error) {
	names, err := config.ListProfiles(paths)
	if err != nil {
		return nil, err
	}

	tools := make([]model.Extension, 0, len(names))
	for _, name := range names {
		ext, err := config.LoadToolExtension(paths, name)
		if err != nil {
			ext = model.Extension{
				Type:            model.ExtensionKindSandbox,
				Name:            name,
				DefaultIncluded: true,
			}
		}
		tools = append(tools, ext)
	}
	return tools, nil
}
