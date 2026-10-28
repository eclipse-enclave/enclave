// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"io/fs"

	"enclave/internal/model"
)

// ExtensionsRepo is the official repository for tools and features that are
// not built in, as accepted by `enclave tools|features add`.
const ExtensionsRepo = "eclipse-enclave/enclave-extensions"

type retiredExtension struct {
	replacement string
	// moved means the replacement lives in ExtensionsRepo; otherwise it is
	// the built-in extension's new name.
	moved bool
}

// retiredExtensions maps the old names of built-in extensions that moved to
// ExtensionsRepo or were renamed to their replacements, so that selecting an
// old name says where it went instead of only that it is unknown.
var retiredExtensions = map[model.ExtensionKind]map[string]retiredExtension{
	model.KindTool: {
		"mistral-vibe": {replacement: "mistral-vibe", moved: true},
	},
	model.KindFeature: {
		"native-build-deps": {replacement: "theia-dev"},
		"playwright":        {replacement: "playwright-mcp", moved: true},
		"shell-extras":      {replacement: "shell-extras", moved: true},
	},
}

// PlaywrightMCPReplacement says what replaced the removed playwright_mcp
// option and --playwright-mcp flag.
const PlaywrightMCPReplacement = "install the playwright-mcp feature (enclave features add " + ExtensionsRepo + " --name playwright-mcp) and select +playwright-mcp"

// RetiredExtensionHint says where a retired built-in extension went, or
// returns "" if name was never retired.
func RetiredExtensionHint(kind model.ExtensionKind, name string) string {
	retired, ok := retiredExtensions[kind][name]
	if !ok {
		return ""
	}
	if !retired.moved {
		return fmt.Sprintf("%s %s was renamed to %s", kind, name, retired.replacement)
	}
	where := "enclave-extensions"
	if retired.replacement != name {
		where += " as " + retired.replacement
	}
	return fmt.Sprintf("%s %s moved to %s: enclave %ss add %s --name %s", kind, name, where, kind, ExtensionsRepo, retired.replacement)
}

// retiredExtensionError is a not-found error that carries the hint for a
// retired name.
type retiredExtensionError string

func (e retiredExtensionError) Error() string { return string(e) }
func (retiredExtensionError) Unwrap() error   { return fs.ErrNotExist }

// notFoundWithHint replaces err with the hint for a retired name when err
// reports that the extension does not exist.
func notFoundWithHint(err error, kind model.ExtensionKind, name string) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if hint := RetiredExtensionHint(kind, name); hint != "" {
		return retiredExtensionError(hint)
	}
	return err
}
