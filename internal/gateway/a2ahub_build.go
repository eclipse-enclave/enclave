// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// A2AHubImage is the local tag of the A2A hub image; it does not depend on the profile.
const A2AHubImage = model.AppName + "-a2a-hub:" + model.GatewayImageTagLatest

// A2AHubLabelHash is the image label holding the hub build hash; kept outside the enclave.a2a. container label namespace.
const A2AHubLabelHash = model.AppName + ".a2ahub.hash"

const (
	a2aHubDockerfileRel = "Dockerfile.a2ahub"
	a2aHubEntrypointRel = "a2ahub-entrypoint.sh"
)

//go:embed a2ahub_build_inputs.txt
var a2aHubBuildInputsManifest string

var a2aHubBuildInputs = mustParseGatewayProxyBuildInputs(a2aHubBuildInputsManifest)

var (
	a2aHubImageInspect = docker.ImageInspect
	a2aHubImageExists  = docker.ImageExists
	a2aHubImageBuild   = docker.Build
)

// A2AHubImageConfig selects the asset root and rebuild policy for EnsureA2AHubImage.
type A2AHubImageConfig struct {
	Paths        model.Paths
	Home         string
	ForceRebuild bool
	NoRebuild    bool
}

// EnsureA2AHubImage builds the hub image when its inputs changed and returns the image reference.
func EnsureA2AHubImage(ctx context.Context, cfg A2AHubImageConfig) (string, error) {
	if cfg.NoRebuild {
		logx.Warnf("Skipping A2A hub image build (--no-rebuild or %s=1).", model.EnvNoRebuild)
		ok, err := a2aHubImageExists(ctx, A2AHubImage)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("A2A hub image %q does not exist locally and builds are disabled (--no-rebuild or %s); re-enable builds to create it", A2AHubImage, model.EnvNoRebuild)
		}
		return A2AHubImage, nil
	}
	appRoot := cfg.Paths.AppRoot
	needs, _, err := a2aHubNeedsRebuild(ctx, appRoot)
	if err != nil {
		return "", err
	}
	if !cfg.ForceRebuild && !needs {
		return A2AHubImage, nil
	}
	resolve := func() (bool, string, error) { return a2aHubNeedsRebuild(ctx, appRoot) }
	execute := func(buildHash string) error { return buildA2AHubImage(ctx, appRoot, buildHash) }
	if err := coordinateGatewayImageBuild(cfg.Home, A2AHubImage, cfg.ForceRebuild, resolve, execute); err != nil {
		return "", err
	}
	return A2AHubImage, nil
}

func a2aHubBuildHash(appRoot string) (string, error) {
	var combined strings.Builder
	for _, relPath := range a2aHubBuildPaths() {
		fullPath := filepath.Join(appRoot, filepath.FromSlash(relPath))
		if err := appendGatewayProxyBuildInputHash(&combined, appRoot, fullPath); err != nil {
			return "", fmt.Errorf("hash A2A hub build input %s: %w", relPath, err)
		}
	}
	return util.HashString(combined.String()), nil
}

func a2aHubBuildPaths() []string {
	return append([]string{a2aHubDockerfileRel, a2aHubEntrypointRel}, a2aHubBuildInputs...)
}

func a2aHubNeedsRebuild(ctx context.Context, appRoot string) (bool, string, error) {
	buildHash, err := a2aHubBuildHash(appRoot)
	if err != nil {
		return false, "", err
	}
	inspect, err := a2aHubImageInspect(ctx, A2AHubImage)
	if err != nil {
		return true, buildHash, nil
	}
	stored := ""
	if inspect.Config != nil {
		stored = inspect.Config.Labels[A2AHubLabelHash]
	}
	return stored != buildHash, buildHash, nil
}

// prepareA2AHubContext stages only the hub inputs so the build never sees the rest of the app root.
func prepareA2AHubContext(appRoot string) (string, func(), error) {
	stagingDir, err := os.MkdirTemp("", "enclave-a2a-hub-context-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(stagingDir) } // #nosec G301 -- stagingDir is created via os.MkdirTemp.
	for _, relPath := range a2aHubBuildPaths() {
		src := filepath.Join(appRoot, filepath.FromSlash(relPath))
		dst := filepath.Join(stagingDir, filepath.FromSlash(relPath))
		if err := util.CopyTree(src, dst, copyGatewayFile); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("copy A2A hub build input %s: %w", relPath, err)
		}
	}
	return stagingDir, cleanup, nil
}

func buildA2AHubImage(ctx context.Context, appRoot string, buildHash string) error {
	logx.Infof("Building A2A hub image.")
	contextDir, cleanup, err := prepareA2AHubContext(appRoot)
	if err != nil {
		return err
	}
	defer cleanup()

	req := docker.BuildRequest{
		ContextDir: contextDir,
		Dockerfile: filepath.Join(contextDir, a2aHubDockerfileRel),
		Tags:       []string{A2AHubImage},
		Labels:     map[string]string{A2AHubLabelHash: buildHash},
	}
	var output bytes.Buffer
	if err := a2aHubImageBuild(ctx, req, &output); err != nil {
		// Same BuildKit DNS fallback as the gateway build.
		req.NetworkMode = "host"
		var retryOutput bytes.Buffer
		if retryErr := a2aHubImageBuild(ctx, req, &retryOutput); retryErr != nil {
			return fmt.Errorf("failed to build A2A hub image: %w (retry with host build network failed: %v)%s",
				err, retryErr, buildOutputTail(retryOutput.String(), gatewayBuildErrorLines))
		}
		logx.Warnf("A2A hub build failed on default build network; retry with host build network succeeded")
	}
	logx.Successf("A2A hub image built")
	return nil
}
