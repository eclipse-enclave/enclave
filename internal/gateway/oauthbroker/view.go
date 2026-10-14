// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"
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
		files, err := b.nativeFiles(g)
		if err != nil {
			return err
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return ErrState
		}
		defer func() { _ = root.Close() }()
		// The host provisions an empty view; anything else was planted.
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			return ErrState
		}
		if len(files) != 0 {
			for _, name := range slices.Sorted(maps.Keys(files)) {
				if err := writeInitialViewFile(root, dir, name, files[name]); err != nil {
					return err
				}
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

// nativeFiles renders the tool's credential files from committed grants. They
// hold handles and non-secret metadata only; an uncertain grant renders none,
// so the client asks for a new login.
func (b *Broker) nativeFiles(g *grant) (map[string][]byte, error) {
	files := map[string][]byte{}
	ready := b.anthropic && g != nil && g.Status == "ready"
	openai, err := b.openaiReady()
	if err != nil {
		return nil, err
	}
	switch b.config.Tool {
	case "claude":
		if !ready {
			return files, nil
		}
		credentials, err := b.nativeView(g)
		if err != nil {
			return nil, err
		}
		config, err := nativeAccountConfig(g)
		if err != nil {
			return nil, err
		}
		files[".credentials.json"], files[".claude.json"] = credentials, config
	case "codex":
		if openai != nil {
			tokens := map[string]any{"access_token": openai.Access, "refresh_token": openai.Refresh, "account_id": openai.AccountID}
			if openai.ID != "" {
				tokens["id_token"] = openai.ID
			}
			if err := addJSON(files, "auth.json", map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": tokens, "last_refresh": openai.CommittedAt.Format(time.RFC3339)}); err != nil {
				return nil, err
			}
		}
	case "opencode":
		if openai != nil {
			if err := addJSON(files, "auth.json", map[string]any{"openai": map[string]any{"type": "oauth", "access": openai.Access, "refresh": openai.Refresh, "expires": openai.ExpiresAt * 1000, "accountId": openai.AccountID}}); err != nil {
				return nil, err
			}
		}
	case "pi":
		entries := map[string]any{}
		if ready {
			entries["anthropic"] = map[string]any{"type": "oauth", "access": b.registry.Access, "refresh": b.registry.Refresh, "expires": g.ExpiresAt * 1000}
		}
		if openai != nil {
			entries["openai-codex"] = map[string]any{"type": "oauth", "access": openai.Access, "refresh": openai.Refresh, "expires": openai.ExpiresAt * 1000, "accountId": openai.AccountID}
		}
		if len(entries) != 0 {
			if err := addJSON(files, "auth.json", entries); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

func addJSON(files map[string][]byte, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrState
	}
	files[name] = data
	return nil
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
