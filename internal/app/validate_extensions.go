// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"enclave/internal/config"
	"enclave/internal/logx"
)

func runValidateExtensions(ctx *AppContext) int {
	validation, err := config.ValidateExtensions(ctx.Paths)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	for _, warning := range validation.Warnings {
		logx.Warnf(warning)
	}
	if len(validation.Errors) > 0 {
		for _, issue := range validation.Errors {
			logx.Errorf(issue)
		}
		return 1
	}
	logx.Successf("Extensions validated")
	return 0
}
