// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"context"
	"fmt"
	"path/filepath"

	"enclave/internal/config"
	"enclave/internal/logx"
)

// Remove deletes installed user extensions. Built-ins are never touched, and an
// extension the installer did not put in place needs --force.
func Remove(_ context.Context, env Env, req Request) ([]ActionResult, error) {
	if len(req.Names) == 0 {
		return nil, fmt.Errorf("name at least one %s extension to remove", req.Kind.Label())
	}
	inventory, err := Inventory(env.Paths, req.Kind, nil, InventoryProvenance)
	if err != nil {
		return nil, err
	}

	// Every name is checked before anything is deleted, so an unknown one in a
	// list cannot leave the run reporting a failure for extensions it did remove.
	for _, name := range req.Names {
		if _, ok := inventory[name]; !ok {
			return nil, fmt.Errorf("%s %q is not installed", req.Kind.Label(), name)
		}
	}

	results := make([]ActionResult, 0, len(req.Names))
	for _, name := range req.Names {
		result, removeErr := removeOne(env, req, inventory[name])
		if removeErr != nil {
			result = ActionResult{Name: name, Action: ActionFailed, Error: removeErr.Error()}
		}
		results = append(results, result)
	}
	env.summarize(req.Kind.Label(), results, req.DryRun)
	return results, nil
}

func removeOne(env Env, req Request, entry Managed) (ActionResult, error) {
	if entry.Source == config.SourceBuiltin {
		return ActionResult{}, fmt.Errorf("a built-in %s cannot be removed", req.Kind.Label())
	}
	if entry.Origin == nil && !req.Force {
		if entry.Problem != "" {
			return ActionResult{}, fmt.Errorf("its provenance file could not be read; pass --force to delete it anyway")
		}
		return ActionResult{}, fmt.Errorf("not installed by enclave; pass --force to delete it anyway")
	}

	target := filepath.Join(env.kindDir(req.Kind), entry.Name)
	env.section(entry.Name, req.Kind.Label())
	if req.Interactive {
		confirmed, err := env.confirm(fmt.Sprintf("%sRemove %s?", bodyIndent, target))
		if err != nil {
			return ActionResult{}, err
		}
		if !confirmed {
			return ActionResult{}, fmt.Errorf("aborted; nothing removed")
		}
	}
	if err := removeInstalled(env, req.Kind, entry.Name); err != nil {
		return ActionResult{}, err
	}

	env.outcome(markOK, logx.ColorGreen, "removed from %s", target)
	if entry.Source == config.SourceOverride {
		env.note("the built-in %s is active again", req.Kind.Label())
	}
	// Host state may belong to a reinstall.
	env.note("host config and state were left untouched")
	return ActionResult{Name: entry.Name, Action: ActionRemoved, Path: target}, nil
}
