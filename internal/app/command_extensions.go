// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"

	"enclave/internal/config"
	"enclave/internal/extinstall"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func runCommandExtensions(ctx *AppContext, req *extinstall.Request) int {
	exts, err := config.ListCommandExtensions(ctx.Paths)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	inventory, err := extensionInventory(ctx.Paths, model.KindCommand, exts)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	if req != nil && req.JSON {
		// Installed means available: a command extension has nothing to enable.
		alwaysEnabled := func(string) bool { return true }
		entries := extensionListJSONEntries(exts, inventory, alwaysEnabled)
		if err := renderExtensionListJSON(os.Stdout, model.KindCommand, nil, entries); err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		return 0
	}

	if len(exts) == 0 {
		fmt.Printf("No command extensions installed (add one with `%s commands add <source>`)\n", model.AppName)
		return 0
	}
	for _, ext := range exts {
		fmt.Printf("✓ %s%s\n", ext.Name, listingSuffix(inventory[ext.Name]))
	}
	return 0
}
