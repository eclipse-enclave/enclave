// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import "enclave/internal/model"

type AppContext struct {
	Paths      model.Paths
	ProjectDir string

	host         model.Host
	project      model.Project
	hostResolved bool
}

func NewAppContext(paths model.Paths, project model.Project) *AppContext {
	return &AppContext{
		Paths:      paths,
		ProjectDir: project.Dir,
		project:    project,
	}
}

func (c *AppContext) Host() (model.Host, error) {
	if c.hostResolved {
		return c.host, nil
	}
	host, err := resolveHost()
	if err != nil {
		return model.Host{}, err
	}
	c.host = host
	c.hostResolved = true
	return host, nil
}

func (c *AppContext) Project() (model.Project, error) {
	return c.project, nil
}
