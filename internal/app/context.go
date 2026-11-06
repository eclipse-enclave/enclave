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
	"enclave/internal/policy"
)

type AppContext struct {
	Paths      model.Paths
	ProjectDir string

	host            model.Host
	project         model.Project
	hostResolved    bool
	projectResolved bool
}

func NewAppContext(paths model.Paths, projectDir string) *AppContext {
	return &AppContext{Paths: paths, ProjectDir: projectDir}
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
	if c.projectResolved {
		return c.project, nil
	}
	project, err := config.ResolveProjectFromDir(c.ProjectDir)
	if err != nil {
		return model.Project{}, err
	}
	c.project = project
	c.projectResolved = true
	return project, nil
}

// PolicyNetworkMode returns the network mode the global and project network
// policy files set, before --allow-all-network is applied.
func (c *AppContext) PolicyNetworkMode() (string, error) {
	host, err := c.Host()
	if err != nil {
		return "", err
	}
	project, err := c.Project()
	if err != nil {
		return "", err
	}
	return policy.NetworkMode(host.Home, project.Hash)
}
