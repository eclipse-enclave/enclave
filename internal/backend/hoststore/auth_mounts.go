// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/util"
)

// ValidateAuthMounts rejects protected grants and host-only control state from
// workload host paths, including parents and aliases. views must contain only
// native handle views belonging to the caller's verified session reservation.
// Callers hold auth mount admission until the workload becomes visible, or
// register a QEMU lifetime lease before releasing that lock.
func ValidateAuthMounts(home string, sources, views []string) ([]string, error) {
	if home == "" {
		return slices.Clone(sources), nil
	}
	protected, err := HasProtectedIdentities(home)
	if err != nil {
		return nil, err
	}
	lockRoot, err := CanonicalAuthPath(filepath.Dir(config.HostLockPath(home, "auth-mount-admission.lock")))
	if err != nil {
		return nil, err
	}
	sessionRoot, err := CanonicalAuthPath(filepath.Join(config.HostStateRootDir(home), "oauth-sessions"))
	if err != nil {
		return nil, err
	}
	authRoot, err := CanonicalAuthPath(config.HostStoreAuthRootDir(home))
	if err != nil {
		return nil, err
	}
	featureRoot, err := CanonicalAuthPath(config.HostStoreFeatureAuthRootDir(home))
	if err != nil {
		return nil, err
	}
	checked := make([]string, len(sources))
	for i, original := range sources {
		source, err := CanonicalAuthPath(original)
		if err != nil {
			return nil, fmt.Errorf("cannot verify workload auth mount %s: %w", original, err)
		}
		allowedView := slices.ContainsFunc(views, func(view string) bool { return util.PathWithin(view, source) })
		if pathsOverlap(source, lockRoot) || (pathsOverlap(source, sessionRoot) && !allowedView) {
			return nil, errors.New("workload mount overlaps host-only authentication control state")
		}
		if protected && pathsOverlap(source, featureRoot) {
			if util.PathWithin(source, featureRoot) {
				return nil, errors.New("workload mount contains protected feature identities")
			}
			rel, err := filepath.Rel(featureRoot, source)
			if err != nil {
				return nil, err
			}
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) == 1 {
				return nil, errors.New("workload mount may contain protected feature identities")
			}
			if len(parts) >= 2 && parts[1] == "auth" {
				dir, err := ResolveDir(home, backend.StoreKey{Owner: parts[0]}, backend.StoreKindFeatureAuth)
				if err != nil {
					return nil, err
				}
				if err := RequireUnprotectedAuth(home, dir); err != nil {
					return nil, err
				}
			}
		}
		if protected && pathsOverlap(source, authRoot) {
			if util.PathWithin(source, authRoot) {
				return nil, errors.New("workload mount contains protected authentication identities")
			}
			rel, err := filepath.Rel(authRoot, source)
			if err != nil {
				return nil, err
			}
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) == 1 || (len(parts) == 2 && parts[1] == "auth") {
				return nil, errors.New("workload mount may contain protected authentication identities")
			}
			if len(parts) >= 3 && parts[1] == "auth" {
				dir, err := ResolveDir(home, backend.StoreKey{Owner: parts[0], Suffix: parts[2]}, backend.StoreKindAuth)
				if err != nil {
					return nil, err
				}
				if err := RequireUnprotectedAuth(home, dir); err != nil {
					return nil, err
				}
			}
		}
		// Pass the checked source to the engine, not an alias that could be
		// retargeted between this check and container creation.
		checked[i] = source
	}
	return checked, nil
}

func pathsOverlap(first, second string) bool {
	return util.PathWithin(first, second) || util.PathWithin(second, first)
}

// CanonicalAuthPath resolves existing ancestors, retaining absent descendants
// for disposable sources while pinning aliases before admission.
func CanonicalAuthPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("auth mount source is not an absolute host path")
	}
	var missing []string
	current := filepath.Clean(path)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
