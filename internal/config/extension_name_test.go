// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import "testing"

// TestValidateExtensionNameRejectsUnsafeNames pins the charset that keeps an
// extension name safe in every position it later occupies: a path segment
// under the user extension directory, a build-context path, a Dockerfile
// comment, a COPY operand, a build stage name, and a FEATURES assignment in a
// shell-form RUN. A name reaching those positions can come straight from an
// untrusted repository's spec.yaml, so the rejected set below is deliberately
// wider than "cannot be used as a filename".
func TestValidateExtensionNameRejectsUnsafeNames(t *testing.T) {
	rejected := map[string]string{
		"empty":              "",
		"dot":                ".",
		"dotdot":             "..",
		"traversal":          "../../../etc/evil",
		"forward slash":      "a/b",
		"back slash":         "a\\b",
		"absolute":           "/etc/passwd",
		"dot prefix":         ".hidden-name",
		"command sub":        "jq-helper$(id)",
		"backtick":           "jq-helper`id`",
		"variable":           "jq-helper${HOME}",
		"semicolon":          "jq-helper;id",
		"pipe":               "jq-helper|sh",
		"space":              "jq helper",
		"newline":            "jq-helper\nRUN id",
		"carriage return":    "jq-helper\rRUN id",
		"escape sequence":    "jq-helper\x1b[2J",
		"double quote":       "jq-\"helper",
		"single quote":       "jq-'helper",
		"uppercase":          "JqHelper",
		"leading dash":       "-helper",
		"leading underscore": "_helper",
	}
	for label, name := range rejected {
		if err := ValidateExtensionName(name); err == nil {
			t.Errorf("ValidateExtensionName(%q) [%s] = nil, want an error", name, label)
		}
	}

	// Every built-in extension name must remain acceptable.
	accepted := []string{
		"claude", "codex", "mistral-vibe", "opencode", "pi", "theia", "theia-next",
		"debug-tools", "devtools", "github-cli", "gitlab-cli", "node-dev",
		"playwright", "python-dev", "shell-extras",
		"foo", "foo_bar", "foo.bar", "a", "0", "tool2",
	}
	for _, name := range accepted {
		if err := ValidateExtensionName(name); err != nil {
			t.Errorf("ValidateExtensionName(%q) = %v, want nil", name, err)
		}
	}
}
