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
	"syscall"

	"enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
)

var dockerRootFreeSpace = realDockerRootFreeSpace

var dockerBuildxAvailable = docker.BuildxAvailable

const (
	runtimeImageDockerStorageWarnBytes = 10 * 1024 * 1024 * 1024
	runtimeImageDockerStorageFailBytes = 2 * 1024 * 1024 * 1024
)

// checkToolInstalled verifies that the requested tool was installed in the
// image by reading the installed-tools manifest baked in at build time.
// Returns nil if the tool is installed, or an error with a helpful message.
func checkToolInstalled(imageName string, tool string, shell bool, admin bool) error {
	if shell || admin {
		return nil // Shell mode doesn't require a specific tool
	}
	if tool == "" {
		return nil
	}
	output, err := docker.RunCapture(context.Background(), &docker.ContainerConfig{
		Image:      imageName,
		Entrypoint: []string{"cat"},
		Cmd:        []string{"/home/" + model.ContainerUser + "/.installed-tools"},
	}, &docker.HostConfig{AutoRemove: true}, "")
	if err != nil {
		// Manifest doesn't exist - likely an older image, skip check
		return nil
	}
	installedTools := strings.Split(strings.TrimSpace(output), "\n")
	for _, t := range installedTools {
		if strings.TrimSpace(t) == tool {
			return nil
		}
	}
	return fmt.Errorf("tool %q is not installed in image %q.\n\nImages are per-tool, so this usually means the image was built for a different tool or is stale.\n\nTo fix, either:\n  - Rebuild for this tool: --tool %s --rebuild\n  - Use --tool with an installed tool: %s\n\nIf %q is a custom sandbox kit, it needs an install.sh that installs its entrypoint: enclave keeps its own base image, so a spec-only sandbox.image is not enough to provision the tool", tool, imageName, tool, strings.Join(installedTools, ", "), tool)
}

func checkRuntimeImageBuildPreflight(ctx context.Context) error {
	// The runtime Dockerfile uses BuildKit-only syntax (RUN --mount), and on
	// Docker >= 23 BuildKit builds require the buildx CLI plugin. Fail up
	// front with guidance instead of surfacing a mid-build syntax error.
	if !dockerBuildxAvailable(ctx) {
		return fmt.Errorf("docker buildx is unavailable, but building the sandbox image requires BuildKit. Install the Docker buildx plugin for your platform (packaged as docker-buildx or docker-buildx-plugin; included in Docker Desktop), then retry. See https://docs.docker.com/go/buildx/")
	}

	rootDir, freeBytes, err := dockerRootFreeSpace(ctx)
	if err != nil {
		logx.Warnf("Could not inspect Docker storage before image build: %v; continuing without the Docker storage guard", err)
		return nil
	}
	if freeBytes < runtimeImageDockerStorageFailBytes {
		return fmt.Errorf("docker storage is critically low: only %s free under %s; aborting before image build. Free space or prune Docker storage, then retry", formatBytes(freeBytes), rootDir)
	}
	if freeBytes < runtimeImageDockerStorageWarnBytes {
		logx.Warnf("Docker storage looks low: %s free under %s; the image build may fail if Docker needs to pull or expand additional layers", formatBytes(freeBytes), rootDir)
	}
	return nil
}

func realDockerRootFreeSpace(ctx context.Context) (string, uint64, error) {
	info, err := docker.Info(ctx)
	if err != nil {
		return "", 0, err
	}
	rootDir := strings.TrimSpace(info.DockerRootDir)
	if rootDir == "" {
		return "", 0, fmt.Errorf("DockerRootDir is unavailable")
	}
	freeBytes, err := availableBytesAtPath(rootDir)
	if err != nil {
		return rootDir, 0, err
	}
	return rootDir, freeBytes, nil
}

func availableBytesAtPath(path string) (uint64, error) {
	resolved, err := existingPathForStatfs(path)
	if err != nil {
		return 0, err
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(resolved, &stat); err != nil {
		return 0, fmt.Errorf("stat filesystem for %s: %w", resolved, err)
	}

	// Statfs reports the available-block count (Bavail) in units of Bsize on
	// both Linux and macOS. Bsize is the only block-size field common to every
	// target platform (Linux's Statfs_t also has Frsize; Darwin's does not), but
	// its type differs across platforms. Guard before the uint64 cast so a
	// signed negative block size cannot wrap.
	blockSize := stat.Bsize
	if blockSize <= 0 {
		return 0, fmt.Errorf("stat filesystem for %s: invalid block size", resolved)
	}
	return stat.Bavail * uint64(blockSize), nil
}

func existingPathForStatfs(path string) (string, error) {
	resolved := filepath.Clean(path)
	for {
		if _, err := os.Stat(resolved); err == nil {
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect %s: %w", resolved, err)
		}

		parent := filepath.Dir(resolved)
		if parent == resolved {
			return "", fmt.Errorf("no existing parent path found for %s", path)
		}
		resolved = parent
	}
}
