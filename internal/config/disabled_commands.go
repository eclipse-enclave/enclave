// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import "path/filepath"

// DisabledCommands reads disabled_commands from the global config under home.
// Discovery runs before LoadDefaults, which rejects a broken file before any
// verb runs, so returning nothing on a read error here fails closed.
func DisabledCommands(home string) (names []string, configPath string) {
	configPath = filepath.Join(hostConfigRoot(home), configFilename)
	defaults, _, err := readDefaults(configPath)
	if err != nil {
		return nil, configPath
	}
	return defaults.DisabledCommands, configPath
}
