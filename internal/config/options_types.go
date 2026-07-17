// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import "enclave/internal/model"

type OptionSpec struct {
	Name                    string
	Group                   OptionGroup
	ApplyDefaultsWithSource func(*model.Options, Defaults, model.OptionSource, *model.OptionSources)
	CLIFlags                []CLIFlag
}
