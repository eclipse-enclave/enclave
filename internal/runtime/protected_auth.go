// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/gateway/exchange"
	"enclave/internal/model"
	"enclave/internal/placeholderstore"
)

func ValidateProtectedAuth(run model.RunOptions, auth model.AuthOptions, build model.BuildOptions, profile model.Profile) error {
	if !run.ProtectAuth {
		return nil
	}
	switch {
	case profile.Name != "claude" || len(profile.Providers) != 1 || profile.Providers[0].TokenEndpoint == nil || profile.ConfigDir == "" || len(profile.Providers[0].AuthSecrets) == 0:
		return fmt.Errorf("--protect-auth currently requires Claude's native auth declarations")
	case run.Admin:
		return fmt.Errorf("--protect-auth is incompatible with --admin")
	case run.AllowAllNetwork:
		return fmt.Errorf("--protect-auth requires restricted networking")
	case run.Backend == backend.NameQEMU:
		return fmt.Errorf("--protect-auth requires Docker or Podman")
	case auth.AuthScope == model.AuthScopeProject:
		return fmt.Errorf("--protect-auth does not support --auth-scope=project")
	case build.Devcontainer || build.BaseImage != "":
		return fmt.Errorf("--protect-auth does not support devcontainers or custom base images")
	}
	return nil
}

func (r *Runtime) prepareProtectedAuth(stores storeSet, mounts *mountAccumulator, mapping *SecretMapping, mode *backend.SessionMode) (*backend.NativeAuthSpec, func(), error) {
	realDir, err := hoststore.ResolveDir(r.host.Home, stores.Auth.Key, stores.Auth.Kind)
	if err != nil {
		return nil, nil, err
	}
	configDir, err := hoststore.ResolveDir(r.host.Home, stores.Config.Key, stores.Config.Kind)
	if err != nil {
		return nil, nil, err
	}
	var view placeholderstore.View
	var operations []placeholderstore.NativeOperation
	err = hoststore.WithModeHolders(r.host.Home, configDir, func(holders *[]hoststore.ModeHolder) error {
		idle := true
		for _, holder := range *holders {
			idle = idle && holder.ID == mode.HolderID
		}
		return hoststore.WithLock(r.host.Home, realDir, func() error {
			current, err := placeholderstore.LoadBindings(realDir)
			if err != nil {
				return err
			}
			var others []placeholderstore.Bindings
			identities, err := os.ReadDir(config.HostStoreAuthTreeDir(r.host.Home, r.profile.Name))
			if err != nil {
				return err
			}
			for _, identity := range identities {
				dir := filepath.Join(config.HostStoreAuthTreeDir(r.host.Home, r.profile.Name), identity.Name())
				if dir == realDir {
					continue
				}
				if !identity.IsDir() {
					return fmt.Errorf("cannot inspect native auth identity %s", identity.Name())
				}
				bindings, err := placeholderstore.LoadBindings(dir)
				if err != nil {
					return err
				}
				others = append(others, bindings)
			}
			operations, err = placeholderstore.ClassifyNative(configDir, r.containerHome+"/"+model.ContainerAuthDir, r.profile, current, others, idle)
			if err != nil {
				return err
			}
			views := filepath.Join(config.HostProjectToolDir(r.host.Home, r.project.Hash, r.profile.Name), "auth-views")
			view, err = placeholderstore.Project(realDir, views, mode.HolderID, r.profile, mapping.Entries)
			return err
		})
	})
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(view.Dir) }
	data, err := json.Marshal(exchange.Config{Provider: r.profile.Providers[0], Secrets: r.profile.Secrets})
	if err != nil {
		return nil, cleanup, err
	}
	mounts.AddMount(backend.Mount{Type: backend.MountTypeBind, Source: view.Dir, ContainerPath: r.containerHome + "/" + model.ContainerAuthDir})
	r.addAuthEnv(mounts)
	var verbs []string
	for _, operation := range operations {
		verbs = append(verbs, operation.File+":"+operation.Verb+":"+operation.Digest)
	}
	mounts.AddEnv(model.EnvNativeAuthOps, strings.Join(verbs, ","))
	// OAuth releases always read through the gateway's native store. Only the
	// stable API-key aliases need extra instances of the existing env rule.
	known := make(map[string]bool)
	for _, entry := range mapping.Entries {
		known[entry.Placeholder] = true
	}
	for _, release := range view.Releases {
		if known[release.Placeholder] {
			continue
		}
		for _, binding := range view.Bindings.Bindings {
			if binding.Kind == "apiKey" && binding.Placeholder == release.Placeholder {
				mapping.Entries = append(mapping.Entries, release)
				known[release.Placeholder] = true
			}
		}
	}
	return &backend.NativeAuthSpec{Store: *stores.Auth, Config: data}, cleanup, nil
}

func (r *Runtime) nativeAuthResetRequested() bool {
	if !r.auth.ResetAuth || !r.run.Persist || r.auth.AuthScope == model.AuthScopeProject {
		return false
	}
	for _, provider := range r.profile.Providers {
		if len(provider.AuthSecrets) > 0 {
			return true
		}
	}
	return false
}

// Reset is independent of the following start: even a refused opposite-mode
// start must invalidate the selected identity under the gateway's same lock.
func (r *Runtime) resetNativeAuth() error {
	if !r.nativeAuthResetRequested() {
		return nil
	}
	if r.backend == nil || r.backend.Storage() == nil {
		return fmt.Errorf("runtime backend is not configured")
	}
	storage := r.backend.Storage()
	ctx := context.Background()
	key := backend.StoreKey{Owner: r.profile.Name, Suffix: r.auth.AuthName}
	if err := storage.Ensure(ctx, key, backend.StoreKindAuth, ""); err != nil {
		return err
	}
	return r.withStoreLock(&backend.StoreRef{Key: key, Kind: backend.StoreKindAuth}, func() error {
		for _, file := range append(r.profile.ProviderAuthFiles(), placeholderstore.BindingsFile) {
			if err := storage.RemovePath(ctx, key, backend.StoreKindAuth, file); err != nil {
				return err
			}
		}
		return nil
	})
}
