// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build gen

package model

type OptionSources struct {
	GlobalOptionSources
	RunOptionSources
	AuthOptionSources
	BuildOptionSources
}

type GlobalOptionSources struct {
	Verbose OptionSource
}

type RunOptionSources struct {
	AddDirs         OptionSource
	AddReadonlyDirs OptionSource
	AllowAllNetwork OptionSource
	AllowDomains    OptionSource
	BridgePorts     OptionSource
	Ephemeral       OptionSource
	HostConfig      OptionSource
	HostConfigPaths OptionSource
	NetworkLog      OptionSource
	NoCache         OptionSource
	NoHistory       OptionSource
	NoMemory        OptionSource
	PlaywrightMCP   OptionSource
	Ports           OptionSource
	SessionName     OptionSource
	Tool            OptionSource
	Yolo            OptionSource
}

type AuthOptionSources struct {
	AuthName     OptionSource
	AuthScope    OptionSource
	NoAPIKey     OptionSource
	PassAPIKey   OptionSource
	PassEnv      OptionSource
	ResetAuth    OptionSource
	SecretsScope OptionSource
}

type BuildOptionSources struct {
	BaseImage       OptionSource
	BuildGID        OptionSource
	BuildUID        OptionSource
	BuildxCacheDir  OptionSource
	BuildxCacheFrom OptionSource
	BuildxCacheTo   OptionSource
	CacheFrom       OptionSource
	Devcontainer    OptionSource
	Features        OptionSource
	ForceBaseImage  OptionSource
	ForceRebuild    OptionSource
	ImageName       OptionSource
	NoRebuild       OptionSource
	Progress        OptionSource
	RuntimeUIDRemap OptionSource
	Slim            OptionSource
	UseRemoteUser   OptionSource
}

func DefaultOptionSources() OptionSources { return OptionSources{} }

func MergeOptionSources(base OptionSources, override OptionSources) OptionSources { return base }
