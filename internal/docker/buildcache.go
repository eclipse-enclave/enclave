// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
)

// BuildCachePrune removes build cache entries. When all is true, both
// referenced and unreferenced entries are removed.
func BuildCachePrune(ctx context.Context, all bool) (*BuildCachePruneReport, error) {
	args := []string{"builder", "prune", "--force"}
	if all {
		args = append(args, "--all")
	}
	out, err := capture(ctx, args...)
	if err != nil {
		return nil, err
	}
	return &BuildCachePruneReport{SpaceReclaimed: parseReclaimedSpace(out)}, nil
}

// BuildCacheUsage returns the total and reclaimable bytes of the Docker build
// cache, the same figures shown by `docker system df`.
func BuildCacheUsage(ctx context.Context) (totalBytes uint64, reclaimableBytes uint64, err error) {
	out, err := capture(ctx, "system", "df", "--format", "{{json .}}")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range splitLines(out) {
		var row struct {
			Type        string `json:"Type"`
			Size        string `json:"Size"`
			Reclaimable string `json:"Reclaimable"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		if row.Type == "Build Cache" {
			return parseHumanBytes(row.Size), parseHumanBytes(row.Reclaimable), nil
		}
	}
	return 0, 0, nil
}
