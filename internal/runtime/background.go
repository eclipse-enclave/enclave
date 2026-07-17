// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"fmt"

	"enclave/internal/logx"
	"enclave/internal/theia"
)

// ExecuteBackground starts a detached container that runs the tool command as
// PID 1; it can be reattached later via `docker attach`.
func (r *Runtime) ExecuteBackground() (string, error) {
	ctx, err := r.prepareExecution()
	if err != nil {
		return "", err
	}
	// Do NOT defer ctx.Cleanup — the gateway must persist alongside the container.

	be := r.backend
	if be == nil {
		if ctx.Cleanup != nil {
			ctx.Cleanup()
		}
		return "", fmt.Errorf("runtime backend is not configured")
	}
	if _, err := be.Start(context.Background(), r.backendRequest(ctx, true, true)); err != nil {
		if ctx.Cleanup != nil {
			ctx.Cleanup()
		}
		return "", err
	}

	// Announce only after the container is confirmed started, so a failed bind
	// (e.g. a host-port conflict) never prints a URL that was never reachable.
	r.logPublishedPortURLs()

	r.runPostStart(ctx.ContainerName)

	return ctx.ContainerName, nil
}

// warnPostStartInteractive warns when a profile with a post_start IDE hook is
// launched in the foreground (where the IDE hook is intentionally skipped
// because the container's TTY is held by `sleep infinity`).
func (r *Runtime) warnPostStartInteractive() {
	if r.run.Background {
		return
	}
	if r.profile.PostStart == nil || r.profile.PostStart.OpenIDE == "" {
		return
	}
	logx.Warnf("tool %q opens %q on start, but you're running in the foreground; pass --background, or attach the IDE later with `enclave %s <container>`",
		r.profile.Name, r.profile.PostStart.OpenIDE, r.profile.PostStart.OpenIDE)
}

// runPostStart performs profile-declared side effects after the container is
// running. Failures here are logged but do not fail the session start: the
// container is up either way and the user can retry the side effect manually
// (e.g. via the GUI button or `enclave theia <name>`).
func (r *Runtime) runPostStart(containerName string) {
	if r.profile.PostStart == nil {
		return
	}
	ide := r.profile.PostStart.OpenIDE
	if ide == "" {
		return
	}
	variant := theia.Variant(ide)
	if !variant.Valid() {
		logx.Warnf("post_start.open_ide %q is not a supported IDE; skipping", ide)
		return
	}
	// Use the resolved per-session yolo value (r.yoloEnabled), not the
	// profile default, so toggling yolo off for this session also withholds
	// the always_allow IDE preferences.
	prefs, err := theia.LoadPreferences(r.host.Home, r.projectDir, r.yoloEnabled)
	if err != nil {
		logx.Warnf("load %s preferences: %v", variant, err)
		return
	}
	logPath := theia.LogPath(r.host.Home, containerName)
	if err := theia.Launch(variant, containerName, prefs, logPath); err != nil {
		logx.Warnf("launch %s: %v (you can retry via `enclave %s %s`)", variant, err, variant, containerName)
		return
	}
	logx.Infof("launched %s attached to %s (logs: %s)", variant, containerName, logPath)
}
