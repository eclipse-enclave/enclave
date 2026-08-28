// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package extinstall installs enclave tool and feature extensions from git
// repositories into the user-global extension root.
package extinstall

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// installPlan is one extension's worth of work, shared by add and update.
type installPlan struct {
	Name    string
	Source  source
	RepoDir string // materialized source directory for this extension
	Subpath string
	Commit  string
	Ref     string
	RefType string
	Action  string        // ActionInstalled or ActionUpdated
	Before  *capabilities // previous capabilities, for the update diff
	// BeforeTree is the installed tree as already read for the
	// local-modification check, reused for the changed-file list. It is nil
	// when that tree could not be read.
	BeforeTree *treeManifest
}

// Add installs extensions of req.Kind from req.Source.
func Add(ctx context.Context, env Env, req Request) ([]ActionResult, error) {
	src, err := parseSource(req.Source)
	if err != nil {
		return nil, err
	}
	if src, err = src.WithRef(req.Ref); err != nil {
		return nil, err
	}
	if src, err = src.WithPath(req.Path); err != nil {
		return nil, err
	}

	repo, err := env.Fetcher.Open(ctx, src.RemoteURL, src.Ref)
	if err != nil {
		return nil, err
	}
	defer repo.Close()

	candidates, err := candidateDirs(repo.Files(), src.Subpath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.Display(), err)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%s contains no extension (no %s found)", src.Display(), config.SpecFilename)
	}
	if err := materializeCandidates(ctx, repo, candidates); err != nil {
		return nil, err
	}

	match, other, skipped := selectKind(classify(repo.Dir(), candidates), req.Kind)
	for _, entry := range skipped {
		env.outcome(env.Style, markWarn, logx.ColorYellow, "skipping %s: %s", displayDir(entry.Dir), entry.Skip)
	}
	if len(match) == 0 {
		if len(other) > 0 {
			return nil, fmt.Errorf("%s contains %d %s extension(s) but no %s; use `enclave %s add`",
				src.Display(), len(other), req.Kind.Other().Label(), req.Kind.Label(), req.Kind.Other().Verb())
		}
		return nil, fmt.Errorf("%s contains no %s extension", src.Display(), req.Kind.Label())
	}

	if err := detectNameCollisions(req.Kind, match); err != nil {
		return nil, err
	}

	selected, err := selectExtensions(env, req, match)
	if err != nil {
		return nil, err
	}

	stage, err := newStaging(env, req.Kind, req.DryRun)
	if err != nil {
		return nil, err
	}
	defer stage.close()

	results := make([]ActionResult, 0, len(selected))
	for _, entry := range selected {
		plan, planErr := planFor(env, req, src, repo, entry)
		if planErr != nil {
			results = append(results, ActionResult{Name: entry.Name, Action: ActionFailed, Error: planErr.Error()})
			continue
		}
		if plan == nil {
			results = append(results, ActionResult{Name: entry.Name, Action: ActionUnchanged, Commit: repo.Commit()})
			continue
		}
		result, applyErr := applyPlan(env, req, *plan, stage)
		if applyErr != nil {
			result = ActionResult{Name: entry.Name, Action: ActionFailed, Error: applyErr.Error()}
		}
		results = append(results, result)
	}
	env.summarize(env.Style, req.Kind.Label(), results, req.DryRun)
	return results, nil
}

// materializeCandidates checks out the directories of every discovered
// candidate. An empty Dir is the repository root: that extension owns the whole
// tree, so no set of paths describes it and the checkout has to stay full. A
// sparse cone naming the other candidates would bring in the root's files
// without any of its subdirectories.
func materializeCandidates(ctx context.Context, repo Repo, candidates []candidate) error {
	dirs := make([]string, 0, len(candidates))
	for _, cand := range candidates {
		if cand.Dir == "" {
			return repo.Materialize(ctx, nil)
		}
		dirs = append(dirs, cand.Dir)
	}
	return repo.Materialize(ctx, dirs)
}

// planFromRepo builds the shared part of an install plan: what to copy, and the
// commit it came from.
func planFromRepo(src source, repo Repo, entry classified, action string) installPlan {
	return installPlan{
		Name:    entry.Name,
		Source:  src,
		RepoDir: filepath.Join(repo.Dir(), filepath.FromSlash(entry.Dir)),
		Subpath: entry.Dir,
		Commit:  repo.Commit(),
		Ref:     repo.Ref(),
		RefType: repo.RefType(),
		Action:  action,
	}
}

func displayDir(dir string) string {
	if dir == "" {
		return "(repository root)"
	}
	return dir
}

// detectNameCollisions rejects a discovered set where two or more candidates
// of the matching kind share a base name, naming every colliding directory so
// the user can disambiguate with --path.
func detectNameCollisions(kind model.ExtensionKind, match []classified) error {
	dirsByName := map[string][]string{}
	for _, entry := range match {
		dirsByName[entry.Name] = append(dirsByName[entry.Name], displayDir(entry.Dir))
	}
	for _, name := range slices.Sorted(maps.Keys(dirsByName)) {
		dirs := dirsByName[name]
		if len(dirs) < 2 {
			continue
		}
		sort.Strings(dirs)
		return fmt.Errorf("multiple %s extensions named %q found (%s); disambiguate with --path",
			kind.Label(), name, strings.Join(dirs, ", "))
	}
	return nil
}

// selectExtensions narrows discovered candidates using --name, --all, or a
// prompt. It never guesses: a non-interactive run with several candidates and
// no selector is an error.
func selectExtensions(env Env, req Request, match []classified) ([]classified, error) {
	if len(req.Names) > 0 {
		byName := map[string]classified{}
		for _, entry := range match {
			byName[entry.Name] = entry
		}
		selected := make([]classified, 0, len(req.Names))
		for _, name := range req.Names {
			entry, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("no %s extension named %q in this source", req.Kind.Label(), name)
			}
			selected = append(selected, entry)
		}
		return selected, nil
	}
	if len(match) == 1 || req.All {
		return match, nil
	}

	_, _ = fmt.Fprintf(env.narrate(), "\nFound %d %s extensions:\n\n", len(match), req.Kind.Label())
	for _, entry := range match {
		_, _ = fmt.Fprintf(env.narrate(), "%s%s %s\n", bodyIndent, env.Style.paint(markInfo, logx.ColorDim), entry.Name)
	}
	if !req.Interactive {
		return nil, fmt.Errorf("several %s extensions found; pass --name or --all", req.Kind.Label())
	}
	confirmed, err := env.confirm(fmt.Sprintf("\n%sInstall all?", bodyIndent))
	if err != nil {
		return nil, err
	}
	if !confirmed {
		return nil, fmt.Errorf("aborted; pass --name to install a subset")
	}
	return match, nil
}

// validExtensionName rejects a spec-declared name that is unsafe as an
// extension identity: a repository-root extension takes its name straight from
// the untrusted spec document, and that name becomes a path segment under the
// destination directory, a build-context path, and an interpolation into the
// generated Dockerfile.
func validExtensionName(name string) error {
	if err := config.ValidateExtensionName(name); err != nil {
		return fmt.Errorf("invalid extension name: %s", config.ExtensionNameCharset)
	}
	return nil
}

// planFor resolves the collision policy for one discovered extension. It
// returns (nil, nil) when the extension is already installed at this commit and
// unmodified, which add reports as unchanged.
func planFor(env Env, req Request, src source, repo Repo, entry classified) (*installPlan, error) {
	if err := validExtensionName(entry.Name); err != nil {
		return nil, err
	}
	target := filepath.Join(env.kindDir(req.Kind), entry.Name)
	plan := planFromRepo(src, repo, entry, ActionInstalled)

	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			if builtinExists(env, req.Kind, entry.Name) && !req.Force {
				return nil, fmt.Errorf("already exists as a built-in %s; installing would shadow it, pass --force to install anyway", req.Kind.Label())
			}
			return &plan, nil
		}
		return nil, err
	}

	origin, err := readOrigin(target)
	if err != nil {
		return nil, err
	}
	if origin == nil {
		if !req.Force {
			return nil, fmt.Errorf("already installed and was not installed by enclave; pass --force to replace it")
		}
		return &plan, nil
	}

	plan.Action = ActionUpdated
	sameSource := origin.Remote == RedactRemote(src.RemoteURL) && origin.Subpath == entry.Dir
	if !sameSource && !req.Force {
		return nil, fmt.Errorf("already installed from %s; pass --force to replace it", origin.Source)
	}

	recorded := recordedTreeHash(*origin)
	manifest, manifestErr := readTreeManifest(target)
	if manifestErr != nil && recorded != "" {
		return nil, manifestErr
	}
	modified := recorded != "" && manifest.Hash != recorded
	if modified && !req.Force {
		return nil, fmt.Errorf("has local modifications; pass --force to discard them")
	}
	if sameSource && !modified && origin.Commit == repo.Commit() {
		return nil, nil
	}

	if manifestErr == nil {
		plan.BeforeTree = &manifest
	}
	before, err := inspect(target, req.Kind)
	if err == nil {
		plan.Before = &before
	}
	return &plan, nil
}

// applyPlan stages, validates, summarizes, confirms, and installs one
// extension. Nothing is written to the destination until the swap at the end,
// so a validation failure or a declined prompt leaves the previous state alone.
func applyPlan(env Env, req Request, plan installPlan, stage *staging) (ActionResult, error) {
	staged := stage.extDir(plan.Name)
	if err := copyExtensionTree(plan.RepoDir, staged, defaultLimits()); err != nil {
		return ActionResult{}, err
	}
	defer stage.discard(plan.Name)

	// The section opens before any validation so that everything this
	// extension has to say, warnings and failures included, lands inside its
	// own block rather than between the previous extension's block and this
	// one's.
	env.section(env.Style, plan.Name, fmt.Sprintf("%s @ %s", req.Kind.Label(), ShortCommit(plan.Commit)))

	warnings, err := validateStaged(env, stage, plan.Name)
	if err != nil {
		return ActionResult{}, err
	}
	for _, warning := range warnings {
		env.outcome(env.Style, markWarn, logx.ColorYellow, "%s", warning)
	}
	if len(warnings) > 0 {
		_, _ = fmt.Fprintln(env.narrate())
	}

	caps, err := inspect(staged, req.Kind)
	if err != nil {
		return ActionResult{}, err
	}
	caps.ShadowsBuiltin = builtinExists(env, req.Kind, plan.Name)

	target := stage.finalPath(plan.Name)
	var stagedHash string
	// contentChanged decides what to promise about the image. Both tree hashes
	// exclude the provenance sidecar, so comparing them answers it exactly; an
	// unknown (an unreadable tree, or a fresh install with nothing to compare
	// against) counts as changed.
	contentChanged := true
	if plan.Before != nil {
		changes, stagedTree := treeChanges(plan.BeforeTree, staged)
		stagedHash = stagedTree.Hash
		contentChanged = plan.BeforeTree == nil || plan.BeforeTree.Hash != stagedHash
		renderUpdate(env, changes, *plan.Before, caps)
	} else {
		caps.render(env.narrate(), env.Style, plan.Source.Display())
	}

	if req.DryRun {
		env.outcome(env.Style, markInfo, logx.ColorCyan, "dry run: would be written to %s", target)
		return ActionResult{Name: plan.Name, Action: ActionSkipped, Commit: plan.Commit, Path: target}, nil
	}
	if req.Interactive {
		stage.touch(env)
		question := fmt.Sprintf("%s%s %s?", bodyIndent, verbFor(plan.Action), target)
		confirmed, confirmErr := env.confirm(question)
		if confirmErr != nil {
			return ActionResult{}, confirmErr
		}
		if !confirmed {
			return ActionResult{}, fmt.Errorf("aborted; nothing %s", plan.Action)
		}
	}
	stage.touch(env)

	installedPath, err := stage.commit(env, plan.Name, func(installedPath string) error {
		treeHash := stagedHash
		if treeHash == "" {
			var hashErr error
			if treeHash, hashErr = TreeHash(installedPath); hashErr != nil {
				return hashErr
			}
		}
		origin := Origin{
			SchemaVersion: OriginSchemaVersion,
			Kind:          string(req.Kind),
			Name:          plan.Name,
			Remote:        RedactRemote(plan.Source.RemoteURL),
			Source:        RedactRemote(plan.Source.Raw),
			Subpath:       plan.Subpath,
			Ref:           plan.Ref,
			RefType:       plan.RefType,
			Commit:        plan.Commit,
			InstalledAt:   env.now().UTC().Format(time.RFC3339),
			InstalledBy:   env.Version,
			TreeHash:      treeHash,
		}
		return WriteOrigin(installedPath, origin)
	})
	if err != nil {
		return ActionResult{}, err
	}

	env.outcome(env.Style, markOK, logx.ColorGreen, "%s at %s", plan.Action, installedPath)
	printPostInstallHints(env, req.Kind, plan.Name, caps, contentChanged)
	return ActionResult{Name: plan.Name, Action: plan.Action, Commit: plan.Commit, Path: installedPath, Warnings: warnings}, nil
}

func verbFor(action string) string {
	if action == ActionUpdated {
		return "Update"
	}
	return "Install to"
}

// renderUpdate shows an update's trust summary: the changed-file list
// followed by the capability diff. The caller has already opened the section.
func renderUpdate(env Env, changedFiles []changedFile, before capabilities, after capabilities) {
	printChangedFiles(env.narrate(), changedFiles)
	changes := diffCapabilities(before, after)
	if len(changes) == 0 {
		_, _ = fmt.Fprintf(env.narrate(), "%s%s\n\n", bodyIndent, env.Style.paint("no capability changes", logx.ColorDim))
		return
	}
	for _, change := range changes {
		_, _ = fmt.Fprintf(env.narrate(), "%s%s\n", bodyIndent, change)
	}
	_, _ = fmt.Fprintln(env.narrate())
}

// printPostInstallHints tells the user what to do next: the image rebuilds on
// its own when the extension's content changed, but an opt-in feature still has
// to be enabled. The image identity hash is content-based and excludes the
// provenance sidecar, so re-pinning to a new commit that carries identical
// content rebuilds nothing and must not say otherwise.
func printPostInstallHints(env Env, kind model.ExtensionKind, name string, caps capabilities, contentChanged bool) {
	if contentChanged {
		env.note(env.Style, "the next run rebuilds the image")
	}
	if kind != model.KindFeature {
		return
	}
	if caps.Spec.DefaultEnabled != nil && !*caps.Spec.DefaultEnabled {
		env.note(env.Style, "enable it with: enclave --features +%s", name)
	}
}

// validateStaged runs config's user-extension validation against the staged
// copy of one extension, which is checked exactly as it would be once
// installed. Errors abort: --force overrides collisions, never validity.
func validateStaged(env Env, stage *staging, name string) ([]string, error) {
	result := config.ValidateExtensionDir(stage.extDir(name), stage.kind, env.Paths)
	if len(result.Errors) > 0 {
		return result.Warnings, fmt.Errorf("%s", strings.Join(result.Errors, "; "))
	}
	return result.Warnings, nil
}

func builtinExists(env Env, kind model.ExtensionKind, name string) bool {
	builtinDir, _ := config.ResolveExtensionDirs(env.Paths, kind, name)
	return builtinDir != ""
}
