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

	results := make([]ActionResult, 0, len(req.Names))
	for _, name := range req.Names {
		entry, ok := inventory[name]
		if !ok {
			return nil, fmt.Errorf("%s %q is not installed", req.Kind.Label(), name)
		}
		result, removeErr := removeOne(env, req, entry)
		if removeErr != nil {
			result = ActionResult{Name: name, Action: ActionFailed, Error: removeErr.Error()}
		}
		results = append(results, result)
	}
	env.summarize(env.Style, req.Kind.Label(), results, req.DryRun)
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
	env.section(env.Style, entry.Name, req.Kind.Label())
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

	env.outcome(env.Style, markOK, logx.ColorGreen, "removed from %s", target)
	if entry.Source == config.SourceOverride {
		env.note(env.Style, "the built-in %s is active again", req.Kind.Label())
	}
	// Host state may belong to a reinstall.
	env.note(env.Style, "host config and state were left untouched")
	return ActionResult{Name: entry.Name, Action: ActionRemoved, Path: target}, nil
}
