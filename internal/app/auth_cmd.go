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
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

type authCommandContext struct {
	host        model.Host
	project     model.Project
	profile     model.Profile
	authFiles   []string
	hostAuthDir string
	storage     backend.StoreManager
	backend     backend.Backend
	authName    string
}

func loadAuthCommandContext(input *CommandInput) (authCommandContext, int) {
	if code := requireDocker(); code != 0 {
		return authCommandContext{}, code
	}
	if input.Sources.Tool != model.SourceCLI {
		logx.Errorf("--tool is required for auth commands")
		return authCommandContext{}, 1
	}
	host, err := input.Ctx.Host()
	if err != nil {
		logx.Errorf("%v", err)
		return authCommandContext{}, 1
	}
	project, err := input.Ctx.Project()
	if err != nil {
		logx.Errorf("%v", err)
		return authCommandContext{}, 1
	}
	profile, err := loadProfileOrReport(input.Ctx.Paths, input.Options.Tool)
	if err != nil {
		return authCommandContext{}, 1
	}
	authFiles, err := backend.ValidateAuthFilePaths(profile.ProviderAuthFiles())
	if err != nil {
		logx.Errorf("invalid provider auth_files for %s: %v", profile.Name, err)
		return authCommandContext{}, 1
	}
	if len(authFiles) == 0 {
		logx.Errorf("tool %s does not define provider auth_files", profile.Name)
		return authCommandContext{}, 1
	}
	hostAuthDir := config.HostProfileConfigDir(host.Home, profile)
	if hostAuthDir == "" {
		logx.Errorf("host auth directory is not configured for %s", profile.Name)
		return authCommandContext{}, 1
	}
	be, err := selectBackend(input.Options, dockerBackendOptions(host, input.Ctx.Paths, model.BuildOptions{}, input.Options.RunOptions))
	if err != nil {
		logx.Errorf("%v", err)
		return authCommandContext{}, 1
	}
	storage := be.Storage()
	if storage == nil {
		logx.Errorf("backend %s does not provide persistent storage", be.Name())
		return authCommandContext{}, 1
	}
	authName := strings.TrimSpace(input.Options.AuthName)
	if authName != "" {
		normalized, err := config.ValidateAuthName(authName)
		if err != nil {
			logx.Errorf("%v", err)
			return authCommandContext{}, 1
		}
		authName = normalized
		if input.Options.AuthScope == model.AuthScopeProject {
			logx.Warnf("--auth-name is ignored under --auth-scope=project; auth lives in the per-project config store")
			authName = ""
		}
	}
	return authCommandContext{host: host, project: project, profile: profile, authFiles: authFiles, hostAuthDir: hostAuthDir, storage: storage, backend: be, authName: authName}, 0
}

func runProtectedAuthAction(input *CommandInput) int {
	if input.AuthFeature != "" {
		return runProtectedFeatureAuthAction(input)
	}
	ctx, code := loadAuthCommandContext(input)
	if code != 0 {
		return code
	}
	identity := ctx.authName
	if input.Options.AuthScope == model.AuthScopeProject {
		identity = backend.ProtectedProjectIdentity(ctx.project.Hash)
	}
	if _, known := protectedauth.Lookup(ctx.profile.Name); !known || identity == "" {
		logx.Errorf("protected auth commands require a built-in --tool with --auth-scope project or --auth-scope shared --auth-name <name>")
		return 1
	}
	manager, ok := ctx.backend.(backend.AuthSessionManager)
	if !ok {
		logx.Errorf("backend does not support protected auth identities")
		return 1
	}
	key := backend.StoreKey{Owner: ctx.profile.Name, Suffix: identity}
	var err error
	if input.Action == "auth-reset" {
		err = manager.ResetAuthIdentity(context.Background(), key)
	} else {
		err = manager.DowngradeAuthIdentity(context.Background(), key)
	}
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	if input.Action == "auth-reset" {
		logx.Successf("Cleared the saved grants of %s identity %s; it remains protected", ctx.profile.Name, identity)
	} else {
		logx.Successf("Removed protection from empty %s identity %s; future logins use ordinary auth files", ctx.profile.Name, identity)
	}
	return 0
}

func runProtectedFeatureAuthAction(input *CommandInput) int {
	if input.AuthFeature != "github-cli" {
		logx.Errorf("protected feature auth supports only --feature github-cli")
		return 1
	}
	if input.Sources.Tool == model.SourceCLI || input.Sources.AuthName == model.SourceCLI || input.Sources.AuthScope == model.SourceCLI {
		logx.Errorf("feature auth identities are shared globally; do not combine --feature with --tool, --auth-name or --auth-scope")
		return 1
	}
	if code := requireDocker(); code != 0 {
		return code
	}
	host, err := input.Ctx.Host()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	be, err := selectBackend(input.Options, dockerBackendOptions(host, input.Ctx.Paths, model.BuildOptions{}, input.Options.RunOptions))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	manager, ok := be.(backend.FeatureAuthIdentityManager)
	if !ok {
		logx.Errorf("backend does not support protected feature auth identities")
		return 1
	}
	if input.Action == "auth-reset" {
		err = manager.ResetFeatureAuthIdentity(context.Background(), input.AuthFeature)
	} else {
		err = manager.DowngradeFeatureAuthIdentity(context.Background(), input.AuthFeature)
	}
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	if input.Action == "auth-reset" {
		logx.Successf("Cleared saved credentials for feature %s; identity remains protected", input.AuthFeature)
	} else {
		logx.Successf("Removed protection from empty feature identity %s", input.AuthFeature)
	}
	return 0
}

func runAuthImport(input *CommandInput) int {
	ctx, code := loadAuthCommandContext(input)
	if code != 0 {
		return code
	}
	missing := missingHostAuthFiles(ctx.hostAuthDir, ctx.authFiles)
	if len(missing) > 0 {
		logx.Errorf("missing host auth files: %s", strings.Join(missing, ", "))
		return 1
	}

	store, err := resolveAuthStore(input.Options.AuthScope, ctx.authName, ctx.profile, ctx.project)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	if err := importAuthFiles(ctx, store); err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	logx.Successf("Imported auth files for %s: %s", ctx.profile.Name, strings.Join(ctx.authFiles, ", "))
	return 0
}

func runAuthExport(input *CommandInput) int {
	ctx, code := loadAuthCommandContext(input)
	if code != 0 {
		return code
	}
	if err := os.MkdirAll(ctx.hostAuthDir, 0o700); err != nil {
		logx.Errorf("failed to create host auth directory: %v", err)
		return 1
	}

	store, err := resolveAuthStore(input.Options.AuthScope, ctx.authName, ctx.profile, ctx.project)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	if err := requireAuthStore(ctx.storage, store); err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	if err := exportAuthFiles(ctx, store); err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	logx.Successf("Exported auth files for %s: %s", ctx.profile.Name, strings.Join(ctx.authFiles, ", "))
	return 0
}

func resolveAuthStore(scope string, authName string, profile model.Profile, project model.Project) (backend.StoreRef, error) {
	switch scope {
	case model.AuthScopeShared:
		// Suffix selects the named auth identity; empty selects the default store.
		return backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: profile.Name, Suffix: authName}}, nil
	case model.AuthScopeProject:
		return backend.StoreRef{Kind: backend.StoreKindConfig, Key: backend.StoreKey{Owner: profile.Name, ProjectHash: project.Hash}}, nil
	default:
		return backend.StoreRef{}, fmt.Errorf("invalid auth scope: %s", scope)
	}
}

// requireAuthStore fails when the auth store does not exist yet, so export
// reports a clear error instead of materializing an empty store.
func requireAuthStore(storage backend.StoreManager, store backend.StoreRef) error {
	inspector, ok := storage.(backend.StoreInspector)
	if !ok {
		return nil
	}
	exists, err := inspector.StoreExists(context.Background(), store.Key, store.Kind)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("auth store not found for %s (scope %s)", store.Key.Owner, store.Kind)
	}
	return nil
}

func missingHostAuthFiles(hostAuthDir string, authFiles []string) []string {
	missing := []string{}
	for _, authFile := range authFiles {
		hostPath := filepath.Join(hostAuthDir, authFile)
		if !util.PathExists(hostPath) {
			missing = append(missing, hostPath)
		}
	}
	return missing
}

func importAuthFiles(ctx authCommandContext, store backend.StoreRef) error {
	items := make([]backend.SeedItem, 0, len(ctx.authFiles))
	for _, authFile := range ctx.authFiles {
		items = append(items, backend.SeedItem{
			HostPath: filepath.Join(ctx.hostAuthDir, authFile),
			StoreRel: authFile,
		})
	}
	c := context.Background()
	if err := ctx.storage.Ensure(c, store.Key, store.Kind, ""); err != nil {
		return fmt.Errorf("failed to create auth store: %w", err)
	}
	if err := ctx.storage.Seed(c, store.Key, store.Kind, items); err != nil {
		return fmt.Errorf("failed to copy auth files: %w", err)
	}
	if owner := util.ChownSpec(ctx.host.UID, ctx.host.GID); owner != "" {
		return ctx.storage.Ensure(c, store.Key, store.Kind, owner)
	}
	return nil
}

func exportAuthFiles(ctx authCommandContext, store backend.StoreRef) error {
	contents := make(map[string][]byte, len(ctx.authFiles))
	missing := []string{}
	for _, authFile := range ctx.authFiles {
		data, err := ctx.storage.ReadFile(context.Background(), store.Key, store.Kind, authFile)
		if err != nil {
			missing = append(missing, authFile)
			continue
		}
		contents[authFile] = data
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing auth files in store: %s", strings.Join(missing, ", "))
	}

	for _, authFile := range ctx.authFiles {
		destPath := filepath.Join(ctx.hostAuthDir, authFile)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
			return fmt.Errorf("failed to create auth directory: %w", err)
		}
		if err := os.WriteFile(destPath, contents[authFile], 0o600); err != nil {
			return fmt.Errorf("failed to write %s: %w", destPath, err)
		}
	}
	return nil
}
