// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"os"
	"path"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/model"
	"enclave/internal/placeholderstore"
)

// This seam is not connected to session startup. Exchange interception and a
// verified gateway capability are required before any protected workload runs.
type nativeAuthMountPlan struct {
	Agent    backend.Mount
	Gateway  []backend.Mount
	Env      []backend.EnvVar
	Releases []model.SecretReleaseEntry
}

type nativeAuthPlanInput struct {
	RealDir, ViewsDir, ConfigDir, LockPath, Session string
	OtherIdentities                                 []placeholderstore.Bindings
	Idle                                            bool
}

// prepareNativeAuthPlan requires the canonical auth/config locks and a complete
// holder and identity snapshot. It never changes the shared native config paths.
func (r *Runtime) prepareNativeAuthPlan(input nativeAuthPlanInput, configured []model.SecretReleaseEntry) (nativeAuthMountPlan, error) {
	var plan nativeAuthMountPlan
	if r.profile.Name != "claude" || len(r.profile.Providers) == 0 {
		return plan, fmt.Errorf("native auth projection currently supports Claude only")
	}
	if r.auth.AuthScope != model.AuthScopeShared {
		return plan, fmt.Errorf("native auth projection requires shared or ephemeral auth scope")
	}
	lock, err := os.Lstat(input.LockPath)
	if err != nil || !lock.Mode().IsRegular() {
		return plan, fmt.Errorf("native auth lock must be an existing regular file")
	}
	authDir := path.Join(r.containerHome, model.ContainerAuthDir)
	view, err := placeholderstore.Project(input.RealDir, input.ViewsDir, input.Session, r.profile, configured)
	if err != nil {
		return plan, err
	}
	operations, err := placeholderstore.ClassifyNative(input.ConfigDir, authDir, r.profile, view.Bindings, input.OtherIdentities, input.Idle)
	if err != nil {
		_ = os.RemoveAll(view.Dir)
		return plan, err
	}
	byFile := map[string]placeholderstore.NativeOperation{}
	for _, op := range operations {
		byFile[op.File] = op
	}
	var serialized []string
	files := r.profile.ProviderAuthFiles()
	for _, file := range files {
		op, ok := byFile[file]
		if !ok {
			_ = os.RemoveAll(view.Dir)
			return plan, fmt.Errorf("native auth file %s has no credential declaration", file)
		}
		serialized = append(serialized, op.File+":"+op.Verb+":"+op.Digest)
	}
	if len(files) == 0 {
		_ = os.RemoveAll(view.Dir)
		return plan, fmt.Errorf("native auth files are not declared")
	}
	plan.Agent = backend.Mount{Type: backend.MountTypeBind, Source: view.Dir, ContainerPath: authDir}
	plan.Gateway = []backend.Mount{
		{Type: backend.MountTypeBind, Source: input.RealDir, ContainerPath: "/auth/native"},
		{Type: backend.MountTypeBind, Source: input.LockPath, ContainerPath: "/auth/lock"},
		{Type: backend.MountTypeBind, Source: view.Dir, ContainerPath: "/auth/view", ReadOnly: true},
	}
	plan.Env = []backend.EnvVar{{Name: model.EnvAuthDir, Value: authDir}, {Name: model.EnvAuthFiles, Value: strings.Join(files, ",")}, {Name: model.EnvNativeAuthOps, Value: strings.Join(serialized, ",")}}
	if env := r.profile.ProviderSecurestorageDirEnv(); env != "" {
		plan.Env = append(plan.Env, backend.EnvVar{Name: env, Value: authDir})
	}
	plan.Releases = append(append([]model.SecretReleaseEntry(nil), configured...), view.Releases...)
	return plan, nil
}
