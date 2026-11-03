// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"path/filepath"

	"enclave/internal/model"
)

// DisabledCommands resolves disabled_commands from the global config under
// home. Command discovery runs before flags and the full config load, so a
// broken file disables nothing here: the full load reports it.
func DisabledCommands(home string) (names []string, configPath string) {
	configPath = filepath.Join(hostConfigRoot(home), configFilename)
	defaults, _, err := readDefaults(configPath)
	if err != nil {
		return nil, configPath
	}
	opts := DefaultOptions()
	sources := model.DefaultOptionSources()
	applyListLayer(&opts, defaults, listLayer{source: model.SourceGlobal, scope: model.ScopeGlobal}, &sources)
	return opts.DisabledCommands, configPath
}
