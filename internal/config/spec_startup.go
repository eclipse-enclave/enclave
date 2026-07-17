// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import "fmt"

// validateStartupCommands rejects commands.startup entries that request a root
// user. enclave never emulates root startup: running a startup process as
// root would require broadening the sandbox's sudoers per-kit, an
// audit-relevant change we deliberately refuse. Root install-time setup belongs
// in install.sh (build time); a non-root startup command drops the user field.
func validateStartupCommands(doc specDocument, specPath string) error {
	if doc.Commands == nil {
		return nil
	}
	for i, c := range doc.Commands.Startup {
		if isRootUser(c.User) {
			return fmt.Errorf("%s: commands.startup[%d] requests user %q; root startup commands are not supported — drop the user field to run as the sandbox user, or move root setup to install.sh (which runs at build time)", specPath, i, c.User)
		}
	}
	return nil
}

// isRootUser reports whether a spec command's user field designates root.
func isRootUser(u string) bool {
	return u == "0" || u == "root"
}
