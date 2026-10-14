// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package protectedclient selects the pinned clients that protected-auth
// sessions run, both for the session command and for later execs into it.
package protectedclient

import (
	"path"
	"strings"

	"enclave/internal/util"
)

// GitHubSetup verifies the pinned GitHub CLI and puts it first on PATH. It
// travels in the runtime request, so --no-rebuild cannot silently run an old
// image without the protected feature's image-provided startup hook.
const GitHubSetup = `if ! protected_gh_version="$(/opt/enclave/gh-2.87.3/bin/gh --version)"; then
    echo 'Protected GitHub CLI is missing or unusable; rebuild the image' >&2
    exit 1
fi
case "$protected_gh_version" in
    'gh version 2.87.3 '*) ;;
    *) echo 'Protected GitHub CLI version mismatch; rebuild the image' >&2; exit 1 ;;
esac
export PATH="/opt/enclave/gh-2.87.3/bin:$PATH"`

// ClaudeLauncher is the pinned Claude launcher below the container home.
func ClaudeLauncher(home string) string {
	return path.Join(home, ".local/share/enclave/claude-2.1.63/bin/claude")
}

// claudeSetup mirrors the Claude entrypoint hook for processes that do not
// pass through the entrypoint.
func claudeSetup(home string) string {
	launcher := util.ShellQuote(ClaudeLauncher(home))
	return `if [ ! -x ` + launcher + ` ] || ! protected_claude_version="$(` + launcher + ` --version)" || [ "$protected_claude_version" != "2.1.63 (Claude Code)" ]; then
    echo 'Protected Claude client is missing or unusable; rebuild the image' >&2
    exit 1
fi
export PATH=` + util.ShellQuote(path.Dir(ClaudeLauncher(home))) + `":$PATH"`
}

// Wrap runs argv after verifying the selected pinned clients and putting them
// first on PATH. Without a selection argv is returned unchanged.
func Wrap(home string, claude, github bool, argv []string) []string {
	var setup []string
	if claude {
		setup = append(setup, claudeSetup(home))
	}
	if github {
		setup = append(setup, GitHubSetup)
	}
	if len(setup) == 0 {
		return argv
	}
	// bash -c uses the next argument as $0; the rest become $1.. for exec "$@".
	return append([]string{"bash", "-c", strings.Join(setup, "\n") + "\nexec \"$@\"", "bash"}, argv...)
}
