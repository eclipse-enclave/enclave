// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import "enclave/internal/cli"

const actionContinue = "continue"

func isRunAction(action string) bool {
	return action == "run" || action == "shell" || action == actionContinue || action == "resume"
}

// backendFreeActions never touch a container engine, so the "auto" backend is
// left unresolved for them: detection would be wasted work, and a command that
// lists extensions, prints configuration, or renders policy must not ask which
// engine to use. Validation of the backend name only runs for the run-like
// commands and update, so the unresolved value is never checked.
var backendFreeActions = map[string]bool{
	"config":                  true,
	"devcontainer-generate":   true,
	"extension-list":          true,
	"features":                true,
	"network-diff":            true,
	"network-print":           true,
	"review-target":           true,
	"ssh-init":                true,
	"tools":                   true,
	"validate-extensions":     true,
	cli.ActionExtensionManage: true,
}

func actionUsesBackend(action string) bool {
	return !backendFreeActions[action]
}
