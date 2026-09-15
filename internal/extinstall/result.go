// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import "slices"

// Per-extension outcomes. These strings are an integration contract: they
// appear verbatim in `--json` output.
const (
	ActionInstalled = "installed"
	ActionUpdated   = "updated"
	ActionUnchanged = "unchanged"
	ActionRemoved   = "removed"
	ActionSkipped   = "skipped"
	ActionFailed    = "failed"
)

// ActionResult is what happened to one extension. The field names are an
// integration contract: they appear verbatim in the `--json` result envelope,
// which internal/app marshals.
type ActionResult struct {
	Name     string   `json:"name"`
	Action   string   `json:"action"`
	Commit   string   `json:"commit,omitempty"`
	Path     string   `json:"path,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// HasFailure reports whether any extension failed, so a caller can pick a
// non-zero exit code while still keeping the successes.
func HasFailure(results []ActionResult) bool {
	return slices.ContainsFunc(results, func(r ActionResult) bool { return r.Action == ActionFailed })
}
