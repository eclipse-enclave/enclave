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
	"sort"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/theia"
)

// runTheia launches the host-installed Theia (or Theia-Next) IDE attached to
// a running enclave container. The container name may be passed as the
// single positional CLI argument; if omitted, the command auto-selects when
// exactly one managed container is running.
func runTheia(variant theia.Variant, projectDir string, opts model.Options) int {
	if !variant.Valid() {
		logx.Errorf("unsupported theia variant: %q", variant)
		return 1
	}
	if err := checkDocker(); err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	ctx := context.Background()

	be, err := newListingBackend(opts)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	requested := ""
	if len(opts.CmdArgs) > 0 {
		requested = strings.TrimSpace(opts.CmdArgs[0])
	}

	containerName, err := resolveTheiaContainer(ctx, be, requested)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Errorf("resolve home: %v", err)
		return 1
	}

	prefs, err := theia.LoadPreferences(home, projectDir, theia.ContainerYoloEnabled(ctx, be, containerName))
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	prefs = theia.MergeExternalAPI(prefs, opts.TheiaAPIPort, opts.TheiaAPIToken)

	logPath := theia.LogPath(home, containerName)
	if err := theia.Launch(variant, containerName, prefs, logPath); err != nil {
		logx.Errorf("launch %s: %v", variant, err)
		return 1
	}
	logx.Infof("launched %s attached to %s (logs: %s)", variant, containerName, logPath)
	return 0
}

func resolveTheiaContainer(ctx context.Context, be backend.Backend, requested string) (string, error) {
	sessions, err := be.List(ctx, backend.SessionFilter{RunningOnly: true})
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}

	names := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if s.Ref.Name != "" {
			names = append(names, s.Ref.Name)
		}
	}
	sort.Strings(names)

	if requested != "" {
		for _, name := range names {
			if name == requested {
				return name, nil
			}
		}
		return "", fmt.Errorf("no running enclave container named %q (use `enclave ps` to list)", requested)
	}

	switch len(names) {
	case 0:
		return "", fmt.Errorf("no running enclave containers (start one first)")
	case 1:
		return names[0], nil
	default:
		return "", fmt.Errorf("multiple running enclave containers; pass one explicitly:\n  %s",
			strings.Join(names, "\n  "))
	}
}
