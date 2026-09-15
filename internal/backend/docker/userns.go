// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"fmt"
	"os"
	"time"

	dockercmd "enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// userNamespaceNoticeDelay is how long the keep-id preparation may run before
// the user is told what is happening. With the image's mapped layer already in
// place rootless podman finishes well under a second.
const userNamespaceNoticeDelay = 2 * time.Second

// prepareImageUserNamespace makes rootless podman materialize the keep-id copy
// of the image before anything else starts. Without idmapped-mount support,
// podman chowns the whole image into a per-mapping layer the first time an
// image runs with --userns=keep-id: minutes for multi-gigabyte images, during
// which every other podman command blocks and nothing is printed. A throwaway
// `create` moves that wait ahead of the gateway start, so an interrupt here
// leaves nothing behind, and surfaces a notice when it takes noticeably long.
func (b *Backend) prepareImageUserNamespace(ctx context.Context, image string) error {
	hostConfig := &dockercmd.HostConfig{}
	applyHostUserNamespace(hostConfig)
	if hostConfig.UserNS == "" {
		return nil
	}
	name := fmt.Sprintf("%s-userns-warmup-%d-%d", model.AppName, os.Getpid(), time.Now().UnixNano())
	done := make(chan error, 1)
	go func() {
		done <- createAndRemove(ctx, image, hostConfig, name)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(userNamespaceNoticeDelay):
	}
	logx.Infof("Preparing %s for the rootless user namespace; the first run after an image build copies the image and can take several minutes.", image)
	if err := <-done; err != nil {
		return err
	}
	logx.Successf("Image prepared for the rootless user namespace")
	return nil
}

func createAndRemove(ctx context.Context, image string, hostConfig *dockercmd.HostConfig, name string) error {
	config := &dockercmd.ContainerConfig{Image: image, Entrypoint: []string{"true"}}
	_, createErr := dockercmd.ContainerCreate(ctx, config, hostConfig, name)
	// The container may exist even when create failed (an interrupt killing
	// the CLI mid-way), so the removal runs detached from ctx unconditionally.
	removeErr := dockercmd.ContainerRemove(context.WithoutCancel(ctx), name, true, true)
	if createErr != nil {
		return fmt.Errorf("prepare image for the rootless user namespace: %w", createErr)
	}
	if removeErr != nil && !dockercmd.IsNotFound(removeErr) {
		return fmt.Errorf("remove user namespace warm-up container %s: %w", name, removeErr)
	}
	return nil
}
