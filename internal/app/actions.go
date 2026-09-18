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

// toolFreeActions never read the default tool: they list or manage sessions,
// extensions, configuration, and host-side setup, and read --tool at most as
// an explicit filter. The unset "auto" tool stays unresolved for them, so they
// neither ask which agent to use nor fail for lack of an answer.
var toolFreeActions = map[string]bool{
	"attach":                  true,
	"config":                  true,
	"extension-list":          true,
	"features":                true,
	"img-import":              true,
	"ps":                      true,
	"review-target":           true,
	"ssh-init":                true,
	"status":                  true,
	"theia":                   true,
	"theia-next":              true,
	"tools":                   true,
	"validate-extensions":     true,
	cli.ActionExtensionManage: true,
}

// actionNeedsTool reports whether an invocation commits to a concrete tool: it
// starts a session, builds an image, or scopes policy, stores, or containers by
// tool. `update` with explicit targets, `stop <session>` and `cleanup --all`
// name their scope themselves and never read the default.
func actionNeedsTool(parsed cli.Result) bool {
	switch parsed.Action {
	case "update":
		return len(parsed.Options.UpdateTools) == 0
	case "stop":
		return len(parsed.Options.CmdArgs) == 0
	case "cleanup":
		return !parsed.Options.CleanupAll
	}
	return !toolFreeActions[parsed.Action]
}
