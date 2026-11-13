// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"enclave/internal/auth"
	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// SecretSourcesRequest names the host sources the declared credentials of a
// session resolve from.
type SecretSourcesRequest struct {
	Home        string
	ProjectHash string
	Tool        string
	Run         model.RunOptions
	Auth        model.AuthOptions
	// Storage holds the env store. Nil leaves the env store out.
	Storage backend.StoreManager
}

// ReadSecretSources reads the secrets files of the tool and project and, if
// the run reads it, the env store under its store lock. The startup check of
// static filter credentials and the session's credentials.sources both read
// through it. A source that cannot be read stays empty, with one warning per
// process.
func ReadSecretSources(req SecretSourcesRequest) auth.SecretSources {
	sources := auth.SecretSources{Home: req.Home, Persisted: map[string]string{}}
	layers, err := auth.ResolveSecretsLayers(req.Home, req.ProjectHash, req.Tool, req.Auth.SecretsScope)
	if err != nil {
		warnSecretSourceOnce(fmt.Sprintf("Failed to read layered secrets: %v", err))
	}
	sources.Layers = layers
	if !readsEnvStore(req.Run, req.Auth) || req.Storage == nil {
		return sources
	}
	key := EnvStoreKey(req.Tool, req.ProjectHash)
	if err := withStorageLock(req.Storage, key, backend.StoreKindEnv, func() error {
		if env, ok := readEnvStoreFile(req.Storage, key); ok {
			sources.Persisted = env
		}
		return nil
	}); err != nil {
		warnSecretSourceOnce(fmt.Sprintf("Failed to read persisted env store: %v", err))
	}
	return sources
}

// readsEnvStore reports whether a run resolves credentials from the env
// store: only a persistent run has one, and --reset-auth empties it.
func readsEnvStore(run model.RunOptions, authOpts model.AuthOptions) bool {
	return run.Persist && !authOpts.ResetAuth
}

// readEnvStoreFile reads and parses the "env" file of the env store key.
func readEnvStoreFile(storage backend.StoreManager, key backend.StoreKey) (map[string]string, bool) {
	data, err := storage.ReadFile(context.Background(), key, backend.StoreKindEnv, "env")
	if err != nil {
		return nil, false
	}
	env, err := util.ParseEnv(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	return env, true
}

// withStorageLock runs fn under the cross-process lock of one store, or
// directly when storage does not lock.
func withStorageLock(storage backend.StoreManager, key backend.StoreKey, kind backend.StoreKind, fn func() error) error {
	if locker, ok := storage.(backend.StoreLocker); ok {
		return locker.WithStoreLock(context.Background(), key, kind, fn)
	}
	return fn()
}

// reportedSecretSourceErrors keeps a run that reads the secret sources for
// its filters and again for the session from warning twice.
var reportedSecretSourceErrors sync.Map

func warnSecretSourceOnce(message string) {
	if _, seen := reportedSecretSourceErrors.LoadOrStore(message, struct{}{}); !seen {
		logx.Warnf("%s", message)
	}
}
