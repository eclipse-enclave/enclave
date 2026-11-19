// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCapabilityUsesLoadedConfigurationAndSession(t *testing.T) {
	s, _ := fixture(t, true)
	configPath := filepath.Join(t.TempDir(), "native.json")
	if err := os.WriteFile(configPath, mustJSON(s.config), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, digest, err := Load(context.Background(), configPath, s.dir, s.lockPath, s.session, nil)
	if err != nil || loaded == nil || digest == "" {
		t.Fatal("valid native exchange could not load")
	}
	runtime := t.TempDir()
	if CheckCapability(runtime, digest, s.session) == nil {
		t.Fatal("missing old-image capability accepted")
	}
	if err := WriteCapability(runtime, digest, s.session); err != nil {
		t.Fatal(err)
	}
	if err := CheckCapability(runtime, digest, s.session); err != nil {
		t.Fatal(err)
	}
	legacy := mustJSON(capability{Version: "native-auth-v1", ConfigDigest: digest, Session: s.session})
	if err := os.WriteFile(filepath.Join(runtime, CapabilityFile), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if CheckCapability(runtime, digest, s.session) == nil {
		t.Fatal("old UID-bypass capability epoch accepted")
	}
	if err := WriteCapability(runtime, digest, s.session); err != nil {
		t.Fatal(err)
	}
	if CheckCapability(runtime, digest, "different-session") == nil {
		t.Fatal("cross-session capability accepted")
	}
	if err := os.WriteFile(configPath, append(mustJSON(s.config), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	_, different, err := ReadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if different == digest || CheckCapability(runtime, different, s.session) == nil {
		t.Fatal("changed loaded config not detected")
	}
	for _, invalid := range []string{`{"provider":{}}`, `{"providers":[{}]}`} {
		if err := os.WriteFile(configPath, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(context.Background(), configPath, s.dir, s.lockPath, s.session, nil); err == nil {
			t.Fatal("invalid declaration advertised a capability")
		}
	}
}
