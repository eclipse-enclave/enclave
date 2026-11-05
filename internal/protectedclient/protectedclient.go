// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package protectedclient selects the pinned GitHub CLI that protected-auth
// sessions run, both for the session command and for later execs into it.
package protectedclient

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

// Wrap runs argv after verifying the pinned GitHub CLI and putting it first
// on PATH.
func Wrap(argv []string) []string {
	// bash -c uses the next argument as $0; the rest become $1.. for exec "$@".
	return append([]string{"bash", "-c", GitHubSetup + "\nexec \"$@\"", "bash"}, argv...)
}
