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
	if req.Interactive {
		confirmed, err := env.confirm(fmt.Sprintf("Remove %s?", target))
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

	_, _ = fmt.Fprintf(env.narrate(), "removed %s %q from %s\n", req.Kind.Label(), entry.Name, target)
	if entry.Source == config.SourceOverride {
		_, _ = fmt.Fprintf(env.narrate(), "the built-in %s %q is active again\n", req.Kind.Label(), entry.Name)
	}
	// Host state may belong to a reinstall.
	_, _ = fmt.Fprintf(env.narrate(), "host config and state for %q were left untouched\n", entry.Name)
	return ActionResult{Name: entry.Name, Action: ActionRemoved, Path: target}, nil
}
