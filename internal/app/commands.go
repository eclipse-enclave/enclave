// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

type CommandInput struct {
	Ctx     *AppContext
	Action  string
	Options model.Options
	Sources model.OptionSources
	// CLIOptions/CLISources hold the parsed CLI state before global/project/
	// tool-override defaults are layered on. The `update` command re-resolves
	// these per target tool so each image matches a `--tool <tool>` run.
	CLIOptions      model.Options
	CLISources      model.OptionSources
	BaseOptions     model.Options
	GlobalDefaults  config.Defaults
	ProjectDefaults config.Defaults
	ToolDefaults    config.Defaults
	HasToolDefaults bool
	ConfigView      model.ConfigView
	// UserCommandMount, when set, exposes the host session command tree read-only
	// inside the container. Populated only for `enclave <name>` session
	// commands dispatched through the run pipeline.
	UserCommandMount *model.UserCommandMount
}

func dispatchCommand(input *CommandInput) int {
	// Verbose is applied once in Run before the per-action early returns.
	switch input.Action {
	case "info":
		return runInfo(input.Ctx, input.Options)
	case "config":
		return runConfig(input.Ctx.Paths, input.Options, input.BaseOptions, input.GlobalDefaults, input.ProjectDefaults, input.ToolDefaults, input.HasToolDefaults, input.ConfigView, input.Ctx.ProjectDir)
	case "auth-import":
		return runAuthImport(input)
	case "auth-export":
		return runAuthExport(input)
	case "ssh-init":
		if err := setupSSH(); err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		return 0
	case "validate-extensions":
		return runValidateExtensions(input.Ctx)
	case "tools":
		return runTools(input.Ctx, input.Options, input.Sources)
	case "features":
		return runFeatures(input.Ctx, input.Options, input.Sources)
	case "extension-list":
		return runExtensionList(input.Ctx)
	case "update":
		return runUpdate(input)
	case "devcontainer-generate":
		return runDevcontainerGenerate(input)
	case "network-status":
		return runNetworkStatus(input)
	case "network-print":
		return runNetworkPrint(input)
	case "network-diff":
		return runNetworkDiff(input)
	case "network-apply":
		return runNetworkApply(input)
	case "network-add-domain":
		return runNetworkAddDomain(input)
	case "network-remove-domain":
		return runNetworkRemoveDomain(input)
	case "network-set-mode":
		return runNetworkSetMode(input)
	case "img-import":
		return runImgImport(input)
	case "exec", "run", "shell", actionContinue, "resume":
		return runExecutionCommand(input)
	default:
		logx.Errorf("unsupported action")
		return 1
	}
}
