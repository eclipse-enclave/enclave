// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"errors"
	"os"
	"path/filepath"

	"enclave/internal/util"
)

const readinessFile = "oauth-ready.json"

type readiness struct {
	Schema         int    `json:"schema"`
	Adapter        string `json:"adapter"`
	Identity       string `json:"identity"`
	Session        string `json:"session"`
	Incarnation    string `json:"incarnation"`
	GitHubIdentity string `json:"githubIdentity,omitempty"`
}

func expectedReadiness(config Config) readiness {
	ready := readiness{Schema: 2, Adapter: "claude", Identity: config.Identity, Session: config.Session, Incarnation: config.Incarnation}
	if config.GitHub != nil {
		ready.GitHubIdentity = config.GitHub.Identity
	}
	return ready
}

// MarkReady attests initialized, concurrent-capable adapters in gateway-private
// session state. Schema 2 requires readers to durably fence shared grant commits.
// Call only after the MITM listeners are serving, before publishing proxy ready.
func (b *Broker) MarkReady() error {
	if b.github != nil {
		if err := b.github.locked(func() error {
			if !b.github.registry.ViewInitialized {
				return ErrState
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return b.locked(func() error {
		if !b.registry.ViewInitialized || (b.config.GitHub != nil) != (b.github != nil) {
			return ErrState
		}
		return durableJSON(filepath.Join(b.config.RuntimeDir, readinessFile), expectedReadiness(b.config))
	})
}

// ClearReadiness fences prior gateway attempts before the host starts a sidecar.
// RuntimeDir is private, admission-owned state; removal does not follow links.
func ClearReadiness(runtimeDir string) error {
	if err := privateDirectory(runtimeDir); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(runtimeDir, readinessFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return util.SyncDir(runtimeDir)
}

// VerifyReadiness is the host's protected-mode startup gate. A generic gateway
// readiness banner cannot prove that an older --no-rebuild image enabled custody.
func VerifyReadiness(config Config) error {
	if err := privateDirectory(config.RuntimeDir); err != nil {
		return err
	}
	var actual readiness
	if err := readPrivateJSON(filepath.Join(config.RuntimeDir, readinessFile), &actual); err != nil {
		return err
	}
	if actual != expectedReadiness(config) {
		return ErrState
	}
	return nil
}
