// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"enclave/internal/logx"
	"enclave/internal/reviewtarget"
)

// reviewTargetTimeout bounds review-target resolution: PR targets reach the
// network through gh, which can stall indefinitely without a deadline. Generous
// enough for `gh pr diff` on large pull requests.
const reviewTargetTimeout = 60 * time.Second

// runReviewTarget resolves a review target against the current repository and
// prints the result as JSON. It needs only git and (for pull requests) gh, so
// it bypasses the image/home setup that dispatchCommand performs.
func runReviewTarget(dir, target string) int {
	t, err := reviewtarget.ParseTarget(target)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), reviewTargetTimeout)
	defer cancel()
	res, err := reviewtarget.Resolve(ctx, dir, t)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	fmt.Println(string(data))
	return 0
}
