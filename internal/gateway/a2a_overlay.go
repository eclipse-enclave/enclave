// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"fmt"
	"os"
	"strconv"

	"enclave/internal/peergroup"
)

// The engine calls of the overlay reload wait live in gateway.go, the only
// file of this package allowed to import internal/docker.

func validateA2AOverlayDir(overlayDir string) error {
	if overlayDir == "" {
		return nil
	}
	info, err := os.Stat(overlayDir)
	if err != nil {
		return fmt.Errorf("gateway A2A overlay dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("gateway A2A overlay dir is not a directory: %s", overlayDir)
	}
	return nil
}

// OverlayLogOutcome reports whether gateway logs settle overlay generation:
// done with a nil error for the applied or cleared marker, done with an error
// for a failure marker, not done otherwise.
func OverlayLogOutcome(logs string, generation uint64) (done bool, err error) {
	gen := strconv.FormatUint(generation, 10)
	for _, marker := range []string{
		fmt.Sprintf(peergroup.OverlayValidationFailedMarker, gen),
		fmt.Sprintf(peergroup.OverlayApplyFailedMarker, gen),
		// An unparseable file has no generation; only the host writes it, so it is ours.
		fmt.Sprintf(peergroup.OverlayValidationFailedMarker, "unknown"),
	} {
		if HasLogLine(logs, marker) {
			return true, fmt.Errorf("gateway A2A overlay failed (%s)", marker)
		}
	}
	for _, marker := range []string{
		fmt.Sprintf(peergroup.OverlayAppliedMarker, gen),
		fmt.Sprintf(peergroup.OverlayClearedMarker, gen),
	} {
		if HasLogLine(logs, marker) {
			return true, nil
		}
	}
	return false, nil
}
