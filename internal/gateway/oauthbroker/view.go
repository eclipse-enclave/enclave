// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// InitializeView creates the native credential file before the workload starts.
// Its gateway-private marker makes proxy reloads preserve local logout and client
// updates. No workload-written file is consulted for grant state or ownership.
func (b *Broker) InitializeView(dir string) error {
	if b.github != nil {
		if err := b.github.initializeView(); err != nil {
			return err
		}
	}
	if !filepath.IsAbs(dir) || privateDirectory(dir) != nil {
		return ErrState
	}
	return b.locked(func() error {
		if b.registry.ViewInitialized {
			return nil
		}
		g, err := b.readGrant()
		if err != nil {
			return err
		}
		data, err := b.nativeView(g)
		if err != nil && !errors.Is(err, ErrUncertain) {
			return err
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return ErrState
		}
		defer func() { _ = root.Close() }()
		for _, name := range []string{".credentials.json", ".claude.json", ".config.json"} {
			if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return ErrState
			}
		}
		if len(data) != 0 {
			config, err := nativeAccountConfig(g)
			if err != nil {
				return err
			}
			if err := writeInitialViewFile(root, dir, ".credentials.json", data); err != nil {
				return err
			}
			if err := writeInitialViewFile(root, dir, ".claude.json", config); err != nil {
				return err
			}
			directory, err := root.Open(".")
			if err != nil {
				return ErrState
			}
			err = directory.Sync()
			_ = directory.Close()
			if err != nil {
				return ErrState
			}
		}
		initialized := b.registry
		initialized.ViewInitialized = true
		if err := b.write(filepath.Join(b.config.RuntimeDir, registryFile), initialized); err != nil {
			b.failed = true
			return ErrState
		}
		b.registry = initialized
		return nil
	})
}

func nativeAccountConfig(g *grant) ([]byte, error) {
	if g == nil || g.Status != "ready" || g.Token.Account == nil || g.Token.Organization == nil {
		return nil, ErrState
	}
	return json.Marshal(map[string]any{
		"theme":                  "dark",
		"hasCompletedOnboarding": true,
		"oauthAccount": map[string]string{
			"accountUuid":      g.Token.Account.UUID,
			"emailAddress":     g.Token.Account.Email,
			"organizationUuid": g.Token.Organization.UUID,
		},
	})
}

func writeInitialViewFile(root *os.Root, dir, name string, data []byte) error {
	// Exclusive creation refuses files and symlinks planted in the workload
	// view. The workload is not launched until the proxy reports readiness.
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrState
	}
	defer func() { _ = file.Close() }()
	if err := inheritFileOwner(file, dir); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return ErrState
	}
	if err := file.Sync(); err != nil {
		return ErrState
	}
	if err := file.Close(); err != nil {
		return ErrState
	}
	return nil
}
