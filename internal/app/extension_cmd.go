// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"strings"

	"enclave/internal/config"
	"enclave/internal/logx"
)

func runExtensionList(ctx *AppContext) int {
	tools, err := config.ListTools(ctx.Paths)
	if err != nil {
		logx.Errorf("list tools: %v", err)
		return 1
	}
	features, err := config.ListFeatures(ctx.Paths)
	if err != nil {
		logx.Errorf("list features: %v", err)
		return 1
	}

	fmt.Println("TOOL EXTENSIONS")
	if len(tools) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, name := range tools {
			builtinDir, userDir := config.ResolveToolDirs(ctx.Paths, name)
			source := extensionSourceLabel(builtinDir, userDir)
			description := ""
			if ext, loadErr := config.LoadToolExtension(ctx.Paths, name); loadErr == nil {
				description = strings.TrimSpace(ext.Description)
			} else {
				// Surface the failure instead of an unexplained blank line so a
				// malformed spec is visible here, not only in validate-extensions.
				description = "(load error) " + loadErrorSummary(loadErr)
			}
			printExtensionLine(name, source, description)
		}
	}

	fmt.Println("")
	fmt.Println("FEATURE EXTENSIONS")
	if len(features) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, ext := range features {
			builtinDir, userDir := config.ResolveFeatureDirs(ctx.Paths, ext.Name)
			source := extensionSourceLabel(builtinDir, userDir)
			printExtensionLine(ext.Name, source, strings.TrimSpace(ext.Description))
		}
	}

	return 0
}

// loadErrorSummary renders a compact single-line summary of a load error for
// the extension list. It collapses whitespace/newlines so a multi-line spec
// error stays on one row, and trims an over-long message.
func loadErrorSummary(err error) string {
	msg := strings.Join(strings.Fields(err.Error()), " ")
	const maxLen = 120
	if len(msg) > maxLen {
		msg = msg[:maxLen-1] + "…"
	}
	return msg
}

func extensionSourceLabel(builtinDir string, userDir string) string {
	switch {
	case builtinDir != "" && userDir != "":
		return "override"
	case userDir != "":
		return "user"
	default:
		return "builtin"
	}
}

func printExtensionLine(name string, source string, description string) {
	if description == "" {
		fmt.Printf("  %-14s %-9s\n", name, source)
		return
	}
	fmt.Printf("  %-14s %-9s %s\n", name, source, description)
}
