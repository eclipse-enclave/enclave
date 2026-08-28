// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"fmt"
	"io"
	"os"

	"enclave/internal/extinstall"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// newExtinstallEnv wires the installer to the real host.
func newExtinstallEnv(ctx *AppContext, req extinstall.Request) (extinstall.Env, error) {
	host, err := ctx.Host()
	if err != nil {
		return extinstall.Env{}, err
	}
	fetcher, err := extinstall.NewGitFetcher()
	if err != nil {
		return extinstall.Env{}, err
	}
	return extinstall.Env{
		Paths:     ctx.Paths,
		Home:      host.Home,
		Fetcher:   fetcher,
		Stdin:     os.Stdin,
		Narration: narrationWriter(req),
		Style:     extinstall.TerminalStyle(narrationFile(req)),
		Version:   "enclave " + model.Version,
	}, nil
}

// narrationFile is the terminal narration is styled for, or nil under --json,
// where there is no narration to style.
func narrationFile(req extinstall.Request) *os.File {
	if req.JSON {
		return nil
	}
	return os.Stdout
}

// narrationWriter discards narration under --json, where the result envelope on
// stdout is the whole report.
func narrationWriter(req extinstall.Request) io.Writer {
	if req.JSON {
		return nil
	}
	return os.Stdout
}

// extensionOp is the shape of extinstall.Add/Update/Remove.
type extensionOp func(context.Context, extinstall.Env, extinstall.Request) ([]extinstall.ActionResult, error)

// extensionOps has no OpList entry: listing is served by the `tools`/`features`
// handlers.
var extensionOps = map[extinstall.Op]extensionOp{
	extinstall.OpAdd:    extinstall.Add,
	extinstall.OpUpdate: extinstall.Update,
	extinstall.OpRemove: extinstall.Remove,
}

func runExtensionManage(ctx *AppContext, req *extinstall.Request) int {
	if req == nil {
		logx.Errorf("missing extension request")
		return 1
	}
	op, ok := extensionOps[req.Op]
	if !ok {
		return reportExtensionResults(*req, nil, fmt.Errorf("unsupported %s operation %q", req.Kind.Label(), req.Op))
	}
	env, err := newExtinstallEnv(ctx, *req)
	if err != nil {
		return reportExtensionResults(*req, nil, err)
	}
	results, err := op(context.Background(), env, *req)
	return reportExtensionResults(*req, results, err)
}

// reportExtensionResults renders the outcome and returns the process exit code.
func reportExtensionResults(req extinstall.Request, results []extinstall.ActionResult, err error) int {
	if req.JSON {
		if err != nil {
			results = append(results, extinstall.ActionResult{Action: extinstall.ActionFailed, Error: err.Error()})
		}
		if writeErr := writeExtensionResultsJSON(os.Stdout, req.Kind, results); writeErr != nil {
			logx.Errorf("%v", writeErr)
			return 1
		}
		return extensionExitCode(results, err)
	}
	if err != nil {
		logx.Errorf("%v", err)
	}
	for _, result := range results {
		if result.Action != extinstall.ActionFailed {
			continue
		}
		logx.Errorf("%s", extensionErrorLine(result))
	}
	return extensionExitCode(results, err)
}

// extensionErrorLine formats one failed result for the human-facing log.
// Installer error messages never name the extension; ActionResult.Name does,
// and an empty one means a whole-run failure.
func extensionErrorLine(result extinstall.ActionResult) string {
	if result.Name == "" {
		return result.Error
	}
	return fmt.Sprintf("%s: %s", result.Name, result.Error)
}

func extensionExitCode(results []extinstall.ActionResult, err error) int {
	if err != nil || extinstall.HasFailure(results) {
		return 1
	}
	return 0
}
