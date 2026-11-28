// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"enclave/internal/apikeys"
	"enclave/internal/auth"
	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// maxSecretInput bounds a value piped to `secrets set`.
const maxSecretInput = 64 << 10

// secretsTarget is the secrets file a `set` or `unset` request edits and the
// tools whose sessions read the key from it.
type secretsTarget struct {
	Path string
	// Set is every alias `set` writes. The first is the key's main alias; the
	// rest are the first alias of each tool that does not declare it, so every
	// tool in Uses sees the key.
	Set []string
	// Remove lists the aliases dropped from Path, so the file cannot hold two
	// disagreeing values for one key.
	Remove []string
	// Skipped describes the variables left alone because another credential
	// declares them too.
	Skipped []string
	Uses    []apikeys.Use
}

// resolveSecretsTarget maps a request onto one of the four secrets layers:
// without --tool the file every tool reads, with it that tool's own file;
// --project selects the current project's variant of either. Variables in
// claimed belong to other credentials too: they are neither written nor
// removed, so set and unset touch the same lines and the other credential
// never receives this key.
func resolveSecretsTarget(home string, project model.Project, key apikeys.Key, req *cli.SecretsRequest, claimed map[string]string) (secretsTarget, error) {
	var target secretsTarget
	if req.Tool == "" {
		target.Path = config.HostSecretsGlobalSharedFile(home)
		if req.Project {
			target.Path = config.HostSecretsProjectSharedFile(home, project.Hash)
		}
		target.Set = []string{key.Variable}
		for _, use := range key.Uses {
			if !slices.Contains(use.EnvVars, key.Variable) && !slices.Contains(target.Set, use.EnvVars[0]) {
				target.Set = append(target.Set, use.EnvVars[0])
			}
		}
		target.Remove = key.EnvVars
		target.Uses = key.Uses
	} else {
		var uses []apikeys.Use
		for _, use := range key.Uses {
			if use.Tool == req.Tool {
				uses = append(uses, use)
			}
		}
		switch len(uses) {
		case 0:
			return secretsTarget{}, fmt.Errorf("%s does not use the %s key; tools that do: %s", req.Tool, key.Name, strings.Join(key.Tools(), ", "))
		case 1:
		default:
			ids := make([]string, len(uses))
			for i, use := range uses {
				ids[i] = use.SecretID
			}
			return secretsTarget{}, fmt.Errorf("%s declares several credentials for the %s key (%s); edit its secrets file by hand", req.Tool, key.Name, strings.Join(ids, ", "))
		}
		use := uses[0]
		variable := use.EnvVars[0]
		if slices.Contains(use.EnvVars, key.Variable) {
			variable = key.Variable
		}
		target.Path = config.HostSecretsGlobalFile(home, use.Tool)
		if req.Project {
			target.Path = config.HostSecretsProjectFile(home, project.Hash, use.Tool)
		}
		target.Set = []string{variable}
		target.Remove = use.EnvVars
		target.Uses = []apikeys.Use{use}
	}
	var skipped []string
	target.Set, skipped = splitClaimed(target.Set, claimed)
	target.Remove, _ = splitClaimed(target.Remove, claimed)
	if len(target.Set) == 0 {
		return secretsTarget{}, fmt.Errorf("%s; enclave secrets cannot manage this key", strings.Join(skipped, "; "))
	}
	target.Skipped = skipped
	return target, nil
}

// splitClaimed drops the variables in claimed from vars and describes each
// one dropped.
func splitClaimed(vars []string, claimed map[string]string) ([]string, []string) {
	var kept, skipped []string
	for _, v := range vars {
		if owner, ok := claimed[v]; ok {
			skipped = append(skipped, fmt.Sprintf("%s is also declared by %s", v, owner))
			continue
		}
		kept = append(kept, v)
	}
	return kept, skipped
}

// claimedVariables maps each variable that a credential other than key's
// declares, in any tool or feature, to that credential: a shared file may
// hold it for that credential, so editing key must leave it alone.
func claimedVariables(profiles []model.Profile, features []model.Extension, key apikeys.Key) map[string]string {
	own := map[string]bool{}
	for _, use := range key.Uses {
		own[use.SecretID] = true
	}
	claimed := map[string]string{}
	collect := func(owner string, secrets map[string]model.SecretConfig) {
		for id, secret := range secrets {
			if own[id] {
				continue
			}
			for _, v := range secret.EnvVars {
				if _, ok := claimed[v]; !ok {
					claimed[v] = fmt.Sprintf("%s (%s)", owner, id)
				}
			}
		}
	}
	for _, profile := range profiles {
		collect("tool "+profile.Name, profile.Secrets)
	}
	for _, feature := range features {
		collect("feature "+feature.Name, feature.Secrets)
	}
	return claimed
}

// toolOptions resolves and caches the options a session of each tool would
// run with in a project, so per-tool overrides and that project's own config
// apply, CLI flags still win, and conflicts settle as they do for a session.
type toolOptions struct {
	input   *CommandInput
	home    string
	current string
	cache   map[[2]string]model.Options
	project map[string]config.Defaults
}

func newToolOptions(input *CommandInput, home string, project model.Project) *toolOptions {
	return &toolOptions{
		input:   input,
		home:    home,
		current: project.Hash,
		cache:   map[[2]string]model.Options{},
		project: map[string]config.Defaults{project.Hash: input.ProjectDefaults},
	}
}

// get returns tool's options in the current project.
func (t *toolOptions) get(tool string) model.Options {
	return t.getIn(t.current, tool)
}

func (t *toolOptions) getIn(hash string, tool string) model.Options {
	if opts, ok := t.cache[[2]string{hash, tool}]; ok {
		return opts
	}
	in := t.input
	projectDefaults, ok := t.project[hash]
	if !ok {
		var err error
		if projectDefaults, err = config.LoadProjectDefaults(t.home, hash); err != nil {
			logx.Warnf("read config of project %s: %v; assuming the global settings", hash, err)
		}
		t.project[hash] = projectDefaults
	}
	opts, _, _ := config.ResolveOptionsForTool(in.CLIOptions, in.CLISources, in.GlobalDefaults, projectDefaults, tool)
	if resolved, _, err := validateAuthOptions(opts, opts.Sources); err != nil {
		logx.Warnf("%s: %v", tool, err)
	} else {
		opts = resolved
	}
	opts, _ = normalizeOptions(opts)
	if opts.SecretsScope == "" {
		opts.SecretsScope = model.SecretsScopeBoth
	}
	t.cache[[2]string{hash, tool}] = opts
	return opts
}

// policyRestricted reports whether the current project's network policy
// restricts traffic, which is when gateway protection checks key prefixes.
func policyRestricted(input *CommandInput) bool {
	mode, err := input.Ctx.PolicyNetworkMode()
	if err != nil {
		logx.Warnf("read network policy: %v", err)
	}
	return mode != model.NetworkModeUnrestricted
}

// prefixRefused reports whether gateway protection would refuse value for use
// in a session with opts.
func prefixRefused(use apikeys.Use, value string, opts model.Options, restricted bool) bool {
	return use.RequiredPrefix != "" && restricted && !opts.AllowAllNetwork && !strings.HasPrefix(value, use.RequiredPrefix)
}

// loadAllProfiles loads every tool profile and warns about each one that
// fails to load; see config.LoadAllProfiles.
func loadAllProfiles(paths model.Paths) ([]model.Profile, []string, map[string]error, error) {
	profiles, names, failed, err := config.LoadAllProfiles(paths)
	for _, name := range names {
		if loadErr, ok := failed[name]; ok {
			logx.Warnf("Skipping tool %s: %v", name, loadErr)
		}
	}
	return profiles, names, failed, err
}

func runSecrets(input *CommandInput) int {
	req := input.Secrets
	if req == nil {
		logx.Errorf("missing secrets request")
		return 1
	}
	host, err := input.Ctx.Host()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	project, err := input.Ctx.Project()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	profiles, toolNames, failed, err := loadAllProfiles(input.Ctx.Paths)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	if req.Tool != "" && !slices.Contains(toolNames, req.Tool) {
		logx.Errorf("unknown tool: %s", req.Tool)
		logx.Infof("Available tools: %s", strings.Join(toolNames, " "))
		return 1
	}
	if loadErr, ok := failed[req.Tool]; ok {
		logx.Errorf("tool %s could not be loaded, so its keys are unknown: %v", req.Tool, loadErr)
		return 1
	}
	keys := apikeys.Catalog(profiles)
	opts := newToolOptions(input, host.Home, project)

	if input.Action == cli.ActionSecretsList {
		// A broken extension only fails a listing that would have shown it.
		return runSecretsList(input, host.Home, project, opts, keys, len(failed) > 0 && req.Tool == "" && req.Key == "")
	}
	if len(failed) > 0 && req.Tool == "" {
		// A shared file feeds every tool; without the broken tool's spec its
		// variables are unknown and could be overwritten or removed.
		broken := make([]string, 0, len(failed))
		for _, name := range toolNames {
			if _, ok := failed[name]; ok {
				broken = append(broken, name)
			}
		}
		logx.Errorf("cannot edit a shared secrets file while %s cannot be loaded; fix it, or use --tool", strings.Join(broken, ", "))
		return 1
	}
	key, err := apikeys.Find(keys, req.Key)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	features, err := config.ListFeatures(input.Ctx.Paths)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	target, err := resolveSecretsTarget(host.Home, project, key, req, claimedVariables(profiles, features, key))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	for _, skipped := range target.Skipped {
		logx.Warnf("%s, so it is left alone", skipped)
	}
	switch input.Action {
	case cli.ActionSecretsSet:
		return runSecretsSet(host.Home, project, opts, policyRestricted(input), key, target)
	case cli.ActionSecretsUnset:
		return runSecretsUnset(input, host.Home, project, opts, key, target)
	default:
		logx.Errorf("unsupported action")
		return 1
	}
}

func runSecretsSet(home string, project model.Project, opts *toolOptions, restricted bool, key apikeys.Key, target secretsTarget) int {
	value, err := readSecretValue(fmt.Sprintf("%s (%s): ", target.Set[0], secretsFileLabel(home, target.Path)))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	if _, err := auth.EditEnvFile(home, target.Path, target.Remove, target.Set, value); err != nil {
		logx.Errorf("save %s: %v", target.Set[0], err)
		return 1
	}
	logx.Successf("Saved the %s key as %s in %s, for %s", key.Name, strings.Join(target.Set, " and "), target.Path, strings.Join(apikeys.ToolsOf(target.Uses), ", "))
	// Gateway protection checks the prefix only under restricted networking,
	// so the value is saved and the mismatch reported rather than refused.
	for _, use := range target.Uses {
		if prefixRefused(use, value, opts.get(use.Tool), restricted) {
			logx.Warnf("%s: the key does not start with %q, so sessions with restricted networking will refuse it", use.Tool, use.RequiredPrefix)
		}
	}
	reportSecretsOverrides(home, project, opts, key, target, value)
	return 0
}

// reportSecretsOverrides warns about whatever keeps a session from using the
// value just written: a scope that skips its file, or a different value from
// a source session resolution prefers, such as the shell, a higher layer, or
// a file-first file source.
func reportSecretsOverrides(home string, project model.Project, opts *toolOptions, key apikeys.Key, target secretsTarget, value string) {
	inspector := newSecretsInspector(home, project, opts.get, os.Getenv)
	var notes toolNotes
	for _, use := range target.Uses {
		scope := opts.get(use.Tool).SecretsScope
		if !slices.Contains(auth.SecretsLayerPaths(home, project.Hash, use.Tool, scope), target.Path) {
			notes.add(fmt.Sprintf("secrets_scope is %q, so sessions ignore this file", scope), use.Tool)
			continue
		}
		row := inspector.describe(key.Name, use)
		if row.Source == "error" || row.value == value {
			continue
		}
		notes.add(fmt.Sprintf("sessions use a different %s from %s", row.Variable, sourcePhrase(row.Source)), use.Tool)
	}
	notes.each(logx.Warnf)
}

func runSecretsUnset(input *CommandInput, home string, project model.Project, opts *toolOptions, key apikeys.Key, target secretsTarget) int {
	removed, err := auth.EditEnvFile(home, target.Path, target.Remove, nil, "")
	if err != nil {
		logx.Errorf("update %s: %v", target.Path, err)
		return 1
	}
	removedValues := map[string]bool{}
	removedKeys := make([]string, 0, len(removed))
	for _, pair := range removed {
		removedKeys = append(removedKeys, pair.Key)
		if pair.Value != "" {
			removedValues[pair.Value] = true
		}
	}
	if len(removed) > 0 {
		logx.Successf("Removed %s from %s", strings.Join(removedKeys, ", "), target.Path)
	}

	// Sessions save resolved keys in the env store and fall back to them,
	// so the key would keep reaching the agent until those copies go. Only
	// copies of the removed value go: a saved key that came from the shell or
	// another layer is not this file's to delete.
	cleared := 0
	if len(removedValues) > 0 {
		for _, use := range target.Uses {
			for _, hash := range envStoreHashes(home, project, use.Tool, input.Secrets.Project) {
				// A session would restore the copy from that project's own file,
				// so clearing it there would change nothing.
				stillProvided := func() bool {
					return projectStillProvides(home, hash, use, opts.getIn(hash, use.Tool).SecretsScope, removedValues)
				}
				ok, err := removeFromEnvStore(home, use.Tool, hash, use.EnvVars, removedValues, stillProvided)
				if err != nil {
					logx.Warnf("Could not remove the saved copy for %s in project %s: %v; run it once with --reset-auth to clear it", use.Tool, hash, err)
					continue
				}
				if ok {
					cleared++
				}
			}
		}
	}
	if cleared > 0 {
		logx.Infof("Removed %d saved copies from the tools' env stores", cleared)
	}
	if len(removed) == 0 && cleared == 0 {
		logx.Infof("The %s key is not set in %s", key.Name, target.Path)
	}

	inspector := newSecretsInspector(home, project, opts.get, os.Getenv)
	var notes toolNotes
	for _, use := range target.Uses {
		row := inspector.describe(key.Name, use)
		switch row.Source {
		case "-", "error":
		case "saved":
			notes.add(fmt.Sprintf("still gets %s from a saved copy that did not come from this file; run the tool once with --reset-auth to clear it", row.Variable), use.Tool)
		default:
			notes.add(fmt.Sprintf("still gets %s from %s", row.Variable, sourcePhrase(row.Source)), use.Tool)
		}
	}
	notes.each(logx.Infof)
	return 0
}

// projectStillProvides reports whether a project's secrets files, read with
// scope, still hold one of values for use after the unset.
func projectStillProvides(home string, hash string, use apikeys.Use, scope string, values map[string]bool) bool {
	layers, err := auth.ResolveSecretsLayers(home, hash, use.Tool, scope)
	if err != nil {
		return false
	}
	for _, layer := range layers {
		for _, alias := range use.EnvVars {
			if values[layer.Values[alias]] {
				return true
			}
		}
	}
	return false
}

// sourcePhrase words a list SOURCE value for a message.
func sourcePhrase(source string) string {
	switch {
	case source == "shell":
		return "your shell"
	case strings.HasPrefix(source, "file "):
		return "the file " + strings.TrimPrefix(source, "file ")
	default:
		return source
	}
}

// toolNotes collects messages and the tools each applies to, so a note shared
// by several tools of one key prints once.
type toolNotes struct {
	order []string
	tools map[string][]string
}

func (n *toolNotes) add(message string, tool string) {
	if n.tools == nil {
		n.tools = map[string][]string{}
	}
	if _, ok := n.tools[message]; !ok {
		n.order = append(n.order, message)
	}
	n.tools[message] = append(n.tools[message], tool)
}

func (n *toolNotes) each(log func(string, ...any)) {
	for _, message := range n.order {
		log("%s: %s", strings.Join(n.tools[message], ", "), message)
	}
}

// envStoreHashes lists the projects whose saved copy of a tool's key an unset
// clears: the current one, plus every other with an env store unless the
// request is limited to this project.
func envStoreHashes(home string, project model.Project, tool string, projectOnly bool) []string {
	hashes := []string{project.Hash}
	if projectOnly {
		return hashes
	}
	others, err := projectHashesWithEnvStore(home, tool)
	if err != nil {
		logx.Warnf("Could not list other projects' saved keys for %s (%v); clearing only this project's copy", tool, err)
	}
	for _, hash := range others {
		if hash != project.Hash {
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

// secretsFileLabel names a secrets file relative to the secrets directory.
func secretsFileLabel(home string, path string) string {
	if rel, err := filepath.Rel(config.HostSecretsDir(home), path); err == nil {
		return rel
	}
	return path
}

// readSecretValue prompts without echo on a terminal and otherwise reads the
// value from stdin, so keys never need to appear on the command line.
func readSecretValue(prompt string) (string, error) {
	fd := int(os.Stdin.Fd()) // #nosec G115 -- file descriptor from Fd() fits in int on all supported platforms.
	if term.IsTerminal(fd) {
		_, _ = fmt.Fprint(os.Stderr, prompt)
		raw, err := term.ReadPassword(fd)
		_, _ = fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read key: %w", err)
		}
		return validateSecretValue(string(raw))
	}
	return readPipedSecret(os.Stdin)
}

// readPipedSecret reads a key from a pipe and refuses one over the limit
// rather than saving a truncated value.
func readPipedSecret(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxSecretInput+1))
	if err != nil {
		return "", fmt.Errorf("read key: %w", err)
	}
	if len(raw) > maxSecretInput {
		return "", fmt.Errorf("the key is longer than %d KiB; nothing was saved", maxSecretInput>>10)
	}
	return validateSecretValue(string(raw))
}

func validateSecretValue(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	switch {
	case value == "":
		return "", errors.New("no key given")
	case strings.ContainsAny(value, "\r\n"):
		return "", errors.New("the key must be a single line")
	case strings.ContainsAny(value[:1]+value[len(value)-1:], "\"'"):
		return "", errors.New("the key must not start or end with a quote")
	}
	return value, nil
}

func projectHashesWithEnvStore(home string, tool string) ([]string, error) {
	entries, err := os.ReadDir(config.HostProjectsDir(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hashes []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if info, err := os.Lstat(config.HostStoreEnvDir(home, tool, entry.Name())); err == nil && info.IsDir() {
			hashes = append(hashes, entry.Name())
		}
	}
	return hashes, nil
}

// readEnvStoreValues returns the persisted env of one tool and project, or nil when
// none exists. The store is agent-writable, so it is read through the
// symlink-safe store root.
func readEnvStoreValues(home string, tool string, hash string) (map[string]string, error) {
	var values map[string]string
	err := withEnvStore(home, tool, hash, func(files *hoststore.Files) error {
		var err error
		_, values, err = readEnvStoreFile(files)
		return err
	})
	return values, err
}

// removeFromEnvStore drops the keys holding one of values from the persisted
// env of one tool and project, and reports whether any were there. It leaves
// the store alone when stillProvided, consulted only if a key matches, says a
// session would restore the copy anyway.
func removeFromEnvStore(home string, tool string, hash string, keys []string, values map[string]bool, stillProvided func() bool) (bool, error) {
	var removed bool
	err := withEnvStore(home, tool, hash, func(files *hoststore.Files) error {
		data, stored, err := readEnvStoreFile(files)
		if err != nil {
			return err
		}
		var matching []string
		for _, key := range keys {
			if values[stored[key]] {
				matching = append(matching, key)
			}
		}
		if len(matching) == 0 || stillProvided() {
			return nil
		}
		content, _ := auth.EditEnvContent(data, matching, nil, "")
		removed = true
		return files.WriteFile("env", []byte(content), 0o600)
	})
	return removed, err
}

// readEnvStoreFile reads and parses an env store's env file; a missing file
// yields no content.
func readEnvStoreFile(files *hoststore.Files) (string, map[string]string, error) {
	data, err := files.ReadFile("env")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	values, err := auth.ParseEnvContent(string(data))
	return string(data), values, err
}

// withEnvStore runs fn on an existing env store under the lock sessions use.
// A missing store is not an error and skips fn.
func withEnvStore(home string, tool string, hash string, fn func(*hoststore.Files) error) error {
	key := backend.EnvStoreKey(tool, hash)
	dir, err := hoststore.ResolveDir(home, key, backend.StoreKindEnv)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return hoststore.WithLock(home, dir, func() error {
		files, err := hoststore.OpenStore(home, key, backend.StoreKindEnv, false)
		if err != nil {
			return err
		}
		defer func() { _ = files.Close() }()
		return fn(files)
	})
}

type secretsListRow struct {
	Key, Tool, Variable, Source, Value, Note string
	// value is the unmasked value, for checks; it is never printed.
	value string
}

// secretsInspector resolves keys the way a session of each tool in this
// project would, reading each tool's secrets layers and env store once.
type secretsInspector struct {
	home    string
	project model.Project
	optsFor func(tool string) model.Options
	getenv  func(string) string
	tools   map[string]toolSecrets
}

type toolSecrets struct {
	layers []auth.SecretsLayer
	stored map[string]string
}

func newSecretsInspector(home string, project model.Project, optsFor func(tool string) model.Options, getenv func(string) string) *secretsInspector {
	return &secretsInspector{home: home, project: project, optsFor: optsFor, getenv: getenv, tools: map[string]toolSecrets{}}
}

func (i *secretsInspector) state(tool string) toolSecrets {
	if state, ok := i.tools[tool]; ok {
		return state
	}
	opts := i.optsFor(tool)
	layers, err := auth.ResolveSecretsLayers(i.home, i.project.Hash, tool, opts.SecretsScope)
	if err != nil {
		logx.Warnf("%s: %v", tool, err)
	}
	state := toolSecrets{layers: layers}
	// Sessions without persistence (ephemeral) neither read nor write the
	// env store.
	if opts.Persist {
		if state.stored, err = readEnvStoreValues(i.home, tool, i.project.Hash); err != nil {
			logx.Warnf("read saved env for %s: %v", tool, err)
		}
	}
	i.tools[tool] = state
	return state
}

func (i *secretsInspector) describe(keyName string, use apikeys.Use) secretsListRow {
	state := i.state(use.Tool)
	return describeAPIKey(i.home, i.project, keyName, use, i.getenv, state.layers, state.stored)
}

func runSecretsList(input *CommandInput, home string, project model.Project, opts *toolOptions, keys []apikeys.Key, failed bool) int {
	req := input.Secrets
	if req.Key != "" {
		key, err := apikeys.Find(keys, req.Key)
		if err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		keys = []apikeys.Key{key}
	}
	restricted := policyRestricted(input)
	inspector := newSecretsInspector(home, project, opts.get, os.Getenv)
	var rows []secretsListRow
	for _, key := range keys {
		for _, use := range key.Uses {
			if req.Tool != "" && use.Tool != req.Tool {
				continue
			}
			row := inspector.describe(key.Name, use)
			row.Note = secretsRowNote(row, use, opts.get(use.Tool), restricted)
			rows = append(rows, row)
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "KEY\tTOOL\tVARIABLE\tSOURCE\tVALUE\tNOTE")
	for _, row := range rows {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", row.Key, row.Tool, row.Variable, row.Source, row.Value, row.Note)
	}
	_ = w.Flush()
	if failed {
		return 1
	}
	return 0
}

// secretsRowNote flags a resolved key that a default session of the tool
// would still not use: withheld by the API-key options, or refused by gateway
// protection under restricted networking.
func secretsRowNote(row secretsListRow, use apikeys.Use, opts model.Options, restricted bool) string {
	if row.value == "" {
		return ""
	}
	switch {
	case opts.NoAPIKey:
		return "withheld by no_api_key"
	case opts.Ephemeral && !opts.PassAPIKey:
		return "withheld: ephemeral without pass_api_key"
	case prefixRefused(use, row.value, opts, restricted):
		return fmt.Sprintf("refused under restricted networking: must start with %q", use.RequiredPrefix)
	}
	return ""
}

func describeAPIKey(home string, project model.Project, keyName string, use apikeys.Use, getenv func(string) string, layers []auth.SecretsLayer, stored map[string]string) secretsListRow {
	row := secretsListRow{Key: keyName, Tool: use.Tool, Variable: use.EnvVars[0], Source: "-", Value: "-"}
	lookup := auth.SecretLookup{ID: use.SecretID, EnvVars: use.EnvVars, File: use.File, Priority: use.Priority}
	resolved, found, err := auth.ResolveSecret(lookup, home, layers, stored, getenv)
	if err != nil {
		logx.Warnf("%s: %v", use.Tool, err)
		row.Source = "error"
		return row
	}
	if !found {
		return row
	}
	if resolved.EnvVar != "" {
		row.Variable = resolved.EnvVar
	}
	row.value = resolved.Value
	row.Value = util.RedactSecret(resolved.Value)
	switch resolved.Source {
	case auth.SecretSourceEnv:
		row.Source = "shell"
	case auth.SecretSourceSecrets:
		row.Source = secretsLayerLabel(home, project, use.Tool, resolved.Layer)
	case auth.SecretSourcePersisted:
		row.Source = "saved"
	case auth.SecretSourceFile:
		row.Variable, row.Source = "-", "file "+resolved.Layer
	}
	return row
}

// secretsLayerLabel names a layer by its reach: "global" and "project" are
// read by every tool, "global/<tool>" and "project/<tool>" by one.
func secretsLayerLabel(home string, project model.Project, tool string, path string) string {
	switch filepath.Clean(path) {
	case filepath.Clean(config.HostSecretsGlobalSharedFile(home)):
		return "global"
	case filepath.Clean(config.HostSecretsGlobalFile(home, tool)):
		return "global/" + tool
	case filepath.Clean(config.HostSecretsProjectSharedFile(home, project.Hash)):
		return "project"
	case filepath.Clean(config.HostSecretsProjectFile(home, project.Hash, tool)):
		return "project/" + tool
	default:
		return secretsFileLabel(home, path)
	}
}
