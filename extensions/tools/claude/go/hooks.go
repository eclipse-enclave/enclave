// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"enclave/internal/auth"
	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

func init() {
	auth.RegisterHooks("claude", claudeHooks{})
}

const onboardingMarker = "hasCompletedOnboarding"

var claudeConfigFiles = []string{"config.json", ".claude.json"}

// storeFileContains reports whether a store file exists and contains marker.
func storeFileContains(ctx auth.Context, store *backend.StoreRef, rel string, marker string) bool {
	data, err := ctx.Storage.ReadFile(context.Background(), store.Key, store.Kind, rel)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte(marker))
}

// writeClaudeConfig writes payload to both Claude config files in the store
// and restores agent ownership so the in-container user can read them.
func writeClaudeConfig(ctx auth.Context, store *backend.StoreRef, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c := context.Background()
	for _, rel := range claudeConfigFiles {
		if err := ctx.Storage.WriteFile(c, store.Key, store.Kind, rel, data, 0o600); err != nil {
			return err
		}
	}
	if owner := util.ChownSpec(ctx.Host.UID, ctx.Host.GID); owner != "" {
		return ctx.Storage.Ensure(c, store.Key, store.Kind, owner)
	}
	return nil
}

// ensureOnboardingComplete ensures the config store has hasCompletedOnboarding: true.
// This is needed when credentials exist in the auth store (e.g., from in-container OAuth)
// but no host OAuth JSON exists to export settings from.
func ensureOnboardingComplete(ctx auth.Context, store *backend.StoreRef) error {
	for _, rel := range claudeConfigFiles {
		if storeFileContains(ctx, store, rel, onboardingMarker) {
			return nil // Already has onboarding flag
		}
	}

	logx.Infof("Marking Claude onboarding complete (credentials found in auth store)")
	return writeClaudeConfig(ctx, store, map[string]any{
		"hasCompletedOnboarding": true,
		"lastOnboardingVersion":  "999.999.999",
	})
}

func readOAuthAccount(hostHome string, profile model.Profile) any {
	path := config.HostProfileOAuthJSONPath(hostHome, profile)
	if path == "" {
		return nil
	}
	// #nosec G304 -- path is derived from trusted host profile OAuth locations.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil
	}

	if value, ok := payload["oauthAccount"]; ok {
		return value
	}
	return nil
}

type claudeHooks struct{}

func (claudeHooks) OnAuthReady(ctx auth.Context) (bool, error) {
	if ctx.Storage == nil || ctx.AuthStorage == nil || ctx.ConfigStore == nil {
		return false, nil
	}

	if !ctx.StorageHasSession {
		return false, nil
	}
	if err := ensureOnboardingComplete(ctx, ctx.ConfigStore); err != nil {
		logx.Warnf("Failed to ensure onboarding complete: %v", err)
	}
	return true, nil
}

func (claudeHooks) AfterEnvInjected(ctx auth.Context, injected map[string]string) error {
	value := injected["ANTHROPIC_API_KEY"]
	if value == "" {
		return nil
	}
	if ctx.Storage == nil || ctx.AuthStorage == nil {
		return nil
	}
	if storeFileContains(ctx, ctx.AuthStorage, "config.json", onboardingMarker) {
		return nil
	}
	logx.Infof("Initializing Claude config files to bypass browser login")
	if err := ctx.Storage.RemovePath(context.Background(), ctx.AuthStorage.Key, ctx.AuthStorage.Kind, "statsig"); err != nil {
		logx.Debugf("Failed to clear statsig state: %v", err)
	}
	if err := writeClaudeConfig(ctx, ctx.AuthStorage, map[string]any{
		"anthropicApiKey":        value,
		"primaryApiKey":          value,
		"allowTelemetry":         false,
		"forceLoginMethod":       "console",
		"hasCompletedOnboarding": true,
		"lastOnboardingVersion":  "999.999.999",
		"oauthAccount":           readOAuthAccount(ctx.Host.Home, ctx.Profile),
	}); err != nil {
		return fmt.Errorf("failed to initialize Claude config: %w", err)
	}
	return nil
}

func (claudeHooks) FinalizeAuth(ctx auth.Context, _ model.AuthState) error {
	if ctx.Storage == nil || ctx.ConfigStore == nil {
		return nil
	}
	// The runtime ~/.claude.json symlink is created by the main entrypoint;
	// this hook only restores agent ownership of the config store contents.
	owner := util.ChownSpec(ctx.Host.UID, ctx.Host.GID)
	if owner == "" {
		return errors.New("invalid host uid/gid for claude config ownership")
	}
	return ctx.Storage.Ensure(context.Background(), ctx.ConfigStore.Key, ctx.ConfigStore.Kind, owner)
}
