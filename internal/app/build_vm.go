// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"enclave/internal/backend"
	backendvm "enclave/internal/backend/vm"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// ensureVMBase resolves the prewarmed base image a vm session's disk is
// created on and provisions it when it does not exist yet. A base is built
// once per key and never rebuilt in place: project disks are overlays on it.
// A project that already has a disk keeps the base that disk was created on,
// whatever a new disk would get.
func ensureVMBase(input *CommandInput, opts model.Options, buildCfg buildConfig, host model.Host, project model.Project) (buildConfig, int) {
	if buildCfg.Devcontainer != nil {
		logx.Errorf("vm backend does not support devcontainer builds")
		return buildConfig{}, 1
	}
	if strings.TrimSpace(opts.BaseImage) != "" {
		logx.Errorf("vm backend does not support --base-image")
		return buildConfig{}, 1
	}
	// --image-name points at a prebuilt base directory, used as it is.
	if opts.ImageNameSet {
		logx.Infof("Using prebuilt vm base image: %s", buildCfg.ImageName)
		return requireVMBase(buildCfg)
	}
	// Only the structural hash, computed without a container engine: a disk
	// keeps the base it was created on, so an elapsed agent-update interval
	// must not turn into a new base.
	selection, err := resolveRuntimeImageSelection(input.Ctx.Paths, opts.BuildOptions, opts.Tool)
	if err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	combinedHash, err := runtimeImageCombinedHash(input.Ctx.Paths, buildCfg, selection)
	if err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	recipeHash, err := runtimeAssetDirHash(input.Ctx.Paths.AppRoot, "vm/base")
	if err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	key := backendvm.BaseKey(combinedHash, recipeHash, host)

	// An existing disk decides the base. Requiring the current one first
	// would provision, or with --no-rebuild demand, an image this project
	// will not use.
	if diskBase, diskKey, ok := backendvm.DiskBase(host.Home, project.Hash, opts.Tool); ok {
		buildCfg.ImageName = diskBase
		logx.Infof("Using vm base image: %s", diskBase)
		if diskKey != key {
			logx.Noticef("The vm disk of this project is on an earlier base image and keeps it; remove %s to start with a new disk on the current base.", config.HostProjectVMDir(host.Home, project.Hash, opts.Tool))
		}
		return buildCfg, 0
	}

	baseDir := backendvm.BaseDir(host.Home, sanitizeBundlePart(opts.Tool), key)
	buildCfg.ImageName = baseDir
	logx.Infof("Using vm base image: %s", baseDir)

	release, err := config.AcquireImageBuildLock(host.Home, baseDir)
	if err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	defer release()
	if _, err := backendvm.ReadBaseStamp(baseDir); err == nil {
		if opts.ForceRebuild {
			logx.Warnf("Ignoring --rebuild: project disks are created on %s, so it is never rebuilt in place.", baseDir)
		}
		return buildCfg, 0
	} else if !errors.Is(err, os.ErrNotExist) {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	if opts.NoRebuild {
		logx.Warnf("Skipping vm base image build (--no-rebuild or %s=1).", model.EnvNoRebuild)
		return requireVMBase(buildCfg)
	}
	if err := buildVMBase(input.Ctx.Paths, host, selection, backendvm.BaseSpec{Dir: baseDir, Key: key, Tool: opts.Tool}); err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	return buildCfg, 0
}

func requireVMBase(buildCfg buildConfig) (buildConfig, int) {
	if _, err := backendvm.ReadBaseStamp(buildCfg.ImageName); err != nil {
		logx.Errorf("vm base image %q is not usable: %v", buildCfg.ImageName, err)
		return buildConfig{}, 1
	}
	return buildCfg, 0
}

// buildVMBase provisions a base from the build context of the selection its
// key was computed for, so nothing reaches the guest that the key ignores.
func buildVMBase(paths model.Paths, host model.Host, selection runtimeImageSelection, spec backendvm.BaseSpec) error {
	contextDir, cleanup, err := prepareBuildContext(paths, selection)
	if err != nil {
		return err
	}
	defer cleanup()
	spec.ContextDir = contextDir
	// Provisioning takes minutes; an interrupt has to end it through the
	// backend, which stops the guest and removes what it staged.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return backendvm.New(backendvm.Options{Host: host, Paths: paths}).BuildBase(ctx, spec)
}

func isVMBackend(opts model.Options) bool {
	return strings.TrimSpace(opts.Backend) == backend.NameVM
}
