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
	"strings"
	"testing"
)

func TestNativeViewInitializationAndReloadPreserveClientState(t *testing.T) {
	b := fixtureBroker(t)
	view := fixtureViewDirectory(t)
	if err := b.InitializeView(view); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(view, ".credentials.json")
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fresh identity acquired a native credential file")
	}
	if _, err := os.Lstat(filepath.Join(view, ".claude.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fresh identity was marked as already onboarded")
	}
	enroll(t, b)
	// The real client writes its projected token response after enrollment.
	data, err := b.NativeView()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(b.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.InitializeView(view); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("reload rewrote client credentials")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.InitializeView(view); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reload reversed client-local logout")
	}
}

func TestNewSessionNativeViewContainsOnlyFreshHandles(t *testing.T) {
	b := fixtureBroker(t)
	first := enroll(t, b)
	config := b.config
	config.RuntimeDir = fixtureViewDirectory(t)
	config.Incarnation = "second"
	next, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	view := fixtureViewDirectory(t)
	if err := next.InitializeView(view); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(view, ".credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{fixtureAccess, fixtureRefresh, first.Access, first.Refresh} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("native view contains provider credentials or prior-session handles")
		}
	}
	var native struct {
		OAuth struct {
			Access  string `json:"accessToken"`
			Refresh string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	if native.OAuth.Access != next.registry.Access || native.OAuth.Refresh != next.registry.Refresh {
		t.Fatal("native view does not contain current-session handles")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("native view is not private")
	}
	configData, err := os.ReadFile(filepath.Join(view, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var accountConfig struct {
		Theme      string            `json:"theme"`
		Onboarding bool              `json:"hasCompletedOnboarding"`
		Account    map[string]string `json:"oauthAccount"`
	}
	if err := json.Unmarshal(configData, &accountConfig); err != nil {
		t.Fatal(err)
	}
	token := fixtureToken()
	if accountConfig.Theme != "dark" || !accountConfig.Onboarding || len(accountConfig.Account) != 3 || accountConfig.Account["accountUuid"] != token.Account.UUID || accountConfig.Account["emailAddress"] != token.Account.Email || accountConfig.Account["organizationUuid"] != token.Organization.UUID {
		t.Fatal("native onboarding/account projection does not match trusted metadata")
	}
}

func TestNativeViewRefusesExistingFilesAndSymlinks(t *testing.T) {
	for _, name := range []string{".credentials.json", ".claude.json", ".config.json"} {
		for _, symlink := range []bool{false, true} {
			t.Run(name+"/"+map[bool]string{false: "file", true: "symlink"}[symlink], func(t *testing.T) {
				b := fixtureBroker(t)
				enroll(t, b)
				view := fixtureViewDirectory(t)
				target := filepath.Join(t.TempDir(), "must-not-touch")
				if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(view, name)
				if symlink {
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := b.InitializeView(view); !errors.Is(err, ErrState) {
					t.Fatalf("untrusted view accepted: %v", err)
				}
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "unchanged" {
					t.Fatal("view write escaped the native directory")
				}
			})
		}
	}
}

func TestUncertainGrantStartsWithoutNativeCredentialsForReauthentication(t *testing.T) {
	b := fixtureBroker(t)
	enroll(t, b)
	grant, err := b.readGrant()
	if err != nil {
		t.Fatal(err)
	}
	grant.Status = "uncertain"
	if err := b.writeGrant(grant); err != nil {
		t.Fatal(err)
	}
	view := fixtureViewDirectory(t)
	if err := b.InitializeView(view); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(view, ".credentials.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("uncertain grant projected credentials")
	}
}

func fixtureViewDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
