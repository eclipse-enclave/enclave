// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"errors"
	"os"
	"path/filepath"
)

// Gateway container paths of the session-local overlay directory (proposal 8).
const (
	GatewayOverlayDir  = "/etc/enclave-a2a-overlay"
	GatewayOverlayPath = GatewayOverlayDir + "/" + OverlayFile
)

// Overlay is overlay.json, re-read by the gateway on start and on every reload.
type Overlay struct {
	SchemaVersion string `json:"schemaVersion"`
	// Generation increases on every write for this session; never reused.
	Generation uint64 `json:"generation"`
	// Active false clears the A2A rules and DNS record (leave).
	Active     bool   `json:"active"`
	Group      string `json:"group"`
	Member     string `json:"member"`
	HubAddress string `json:"hubAddress"`
	HubPort    int    `json:"hubPort"`
	// AdapterPort is 0 for the orchestrator: no INPUT rule.
	AdapterPort int    `json:"adapterPort"`
	DNSName     string `json:"dnsName"`
}

// Gateway log markers, one line each; %s is Overlay.Generation in decimal,
// or the literal "unknown" when the file cannot be parsed.
const (
	OverlayAppliedMarker          = "A2A overlay applied (generation=%s)"
	OverlayClearedMarker          = "A2A overlay cleared (generation=%s)"
	OverlayValidationFailedMarker = "A2A overlay validation failed (generation=%s)"
	OverlayApplyFailedMarker      = "A2A overlay apply failed (generation=%s)"
)

// WriteOverlay atomically replaces path (temp file and rename in the same directory).
func WriteOverlay(path string, o Overlay) error {
	if err := checkSchema(&o.SchemaVersion); err != nil {
		return err
	}
	if o.Generation == 0 {
		return errors.New("overlay generation must start at 1")
	}
	if err := os.MkdirAll(filepath.Dir(path), publicDirMode); err != nil {
		return err
	}
	return writeJSONAtomic(path, o, publicFileMode)
}
