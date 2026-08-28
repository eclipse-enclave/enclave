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
	"sort"

	"enclave/internal/config"
)

// Update refreshes managed extensions of req.Kind. With no names it targets
// every managed extension of that kind.
func Update(ctx context.Context, env Env, req Request) ([]ActionResult, error) {
	inventory, err := Inventory(env.Paths, req.Kind, nil, InventoryProvenance)
	if err != nil {
		return nil, err
	}

	targets, err := updateTargets(req, inventory)
	if err != nil {
		return nil, err
	}
	if req.Ref != "" && len(targets) > 1 {
		return nil, fmt.Errorf("--ref applies to a single extension; name one")
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no %s extensions are installed from a git source", req.Kind.Label())
	}

	stage, err := newStaging(env, req.Kind, req.DryRun)
	if err != nil {
		return nil, err
	}
	defer stage.close()

	fetched := newFetchCache(env.Fetcher)
	defer fetched.close()

	results := make([]ActionResult, 0, len(targets))
	for _, name := range targets {
		entry := inventory[name]
		if entry.Origin == nil {
			reason := "not installed by enclave; re-add it to manage it"
			if entry.Problem != "" {
				reason = "its provenance file could not be read; re-add it to manage it"
			}
			results = append(results, ActionResult{
				Name:   name,
				Action: ActionSkipped,
				Error:  reason,
			})
			continue
		}
		result, updateErr := updateOne(ctx, env, req, stage, fetched, entry)
		if updateErr != nil {
			result = ActionResult{Name: name, Action: ActionFailed, Error: updateErr.Error()}
		}
		results = append(results, result)
	}
	return results, nil
}

// updateTargets resolves which extensions to consider: the named ones, or every
// installed extension of this kind. Unmanaged names stay in the list so they can
// be reported as skipped rather than vanishing.
func updateTargets(req Request, inventory map[string]Managed) ([]string, error) {
	if len(req.Names) > 0 {
		for _, name := range req.Names {
			if _, ok := inventory[name]; !ok {
				return nil, fmt.Errorf("%s %q is not installed", req.Kind.Label(), name)
			}
		}
		return req.Names, nil
	}
	names := make([]string, 0, len(inventory))
	for name, entry := range inventory {
		if entry.Source == config.SourceBuiltin {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func updateOne(ctx context.Context, env Env, req Request, stage *staging, fetched *fetchCache, entry Managed) (ActionResult, error) {
	origin := *entry.Origin
	installed := filepath.Join(env.kindDir(req.Kind), entry.Name)

	src, err := parseSource(origin.Source)
	if err != nil {
		// A sidecar written by an older or hand-edited install may not round
		// trip through parseSource; the recorded remote is authoritative.
		src = source{Raw: origin.Source, RemoteURL: origin.Remote, Subpath: origin.Subpath}
	}
	src.RemoteURL = origin.Remote
	src.Subpath = origin.Subpath
	src.Ref = origin.Ref
	if req.Ref != "" {
		src.Ref = req.Ref
	}

	// The installed tree is read once here, for this target only, and the
	// manifest is carried into the plan for the changed-file list.
	recorded := recordedTreeHash(origin)
	installedTree, treeErr := readTreeManifest(installed)
	if treeErr != nil && recorded != "" && !req.Force {
		return ActionResult{}, treeErr
	}
	modified := recorded != "" && treeErr == nil && installedTree.Hash != recorded

	// A commit pin is immutable: nothing to check unless the user asks for a
	// different ref or forces a reinstall.
	if origin.RefType == RefTypeCommit && req.Ref == "" && !req.Force {
		if !modified {
			_, _ = fmt.Fprintf(env.narrate(), "%s: pinned to %s, up to date\n", entry.Name, ShortCommit(origin.Commit))
			return ActionResult{Name: entry.Name, Action: ActionUnchanged, Commit: origin.Commit, Path: installed}, nil
		}
		return ActionResult{}, fmt.Errorf("pinned to %s and has local modifications; pass --force to reinstall it", ShortCommit(origin.Commit))
	}

	resolved, err := fetched.resolve(ctx, src.RemoteURL, src.Ref)
	if err != nil {
		return ActionResult{}, err
	}
	if resolved.Commit == origin.Commit && !modified && !req.Force {
		_, _ = fmt.Fprintf(env.narrate(), "%s: up to date at %s\n", entry.Name, ShortCommit(origin.Commit))
		return ActionResult{Name: entry.Name, Action: ActionUnchanged, Commit: origin.Commit, Path: installed}, nil
	}
	if modified && !req.Force {
		return ActionResult{}, fmt.Errorf("has local modifications; pass --force to discard them")
	}

	// The reference is already resolved, so this fetch needs no round trip.
	repo, err := fetched.open(ctx, src.RemoteURL, resolved)
	if err != nil {
		return ActionResult{}, err
	}

	candidates, err := candidateDirs(repo.Files(), origin.Subpath)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%s no longer provides it: %w", src.Display(), err)
	}
	if err := materializeCandidates(ctx, repo, candidates); err != nil {
		return ActionResult{}, err
	}
	match, _, skipped := selectKind(classify(repo.Dir(), candidates), req.Kind)
	if len(match) == 0 {
		reason := "no matching extension"
		if len(skipped) > 0 {
			reason = skipped[0].Skip
		}
		return ActionResult{}, fmt.Errorf("%s: %s", src.Display(), reason)
	}
	var selected *classified
	for i := range match {
		if match[i].Name == entry.Name {
			selected = &match[i]
			break
		}
	}
	if selected == nil {
		return ActionResult{}, fmt.Errorf("%s no longer provides this %s", src.Display(), req.Kind.Label())
	}

	before, inspectErr := inspect(installed, req.Kind)
	plan := planFromRepo(src, repo, *selected, ActionUpdated)
	if inspectErr == nil {
		plan.Before = &before
	}
	if treeErr == nil {
		plan.BeforeTree = &installedTree
	}
	return applyPlan(env, req, plan, stage)
}
