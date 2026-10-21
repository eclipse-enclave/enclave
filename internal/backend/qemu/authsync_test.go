// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"context"
	"os"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestAuthSyncSkipsPlaceholderFiles(t *testing.T) {
	for _, feature := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			t.Run(testAuthSyncName(feature, existing), func(t *testing.T) {
				b := New(Options{Host: model.Host{Home: t.TempDir()}})
				ctx := context.Background()
				configKey := backend.StoreKey{Owner: "tool", ProjectHash: "project"}
				authKey := backend.StoreKey{Owner: "tool"}
				kind := backend.StoreKindAuth
				sync := backend.FeatureAuthSync{Feature: "feature", ConfigDir: "feature-dir", AuthFiles: []string{"bad.json", "custom.json", "good.json"}}
				files := []string{"bad.json", "custom.json", "good.json"}
				prefix := ""
				if feature {
					prefix = "feature-dir/"
					kind = backend.StoreKindFeatureAuth
					authKey.Owner = "feature"
				}
				for file, data := range map[string]string{"bad.json": `{"key":"at-ENCLAVE_SECRET_previous"}`, "custom.json": `{"key":"vendor_custom_end"}`, "good.json": `{"key":"real-key"}`} {
					if err := b.storage.WriteFile(ctx, configKey, backend.StoreKindConfig, prefix+file, []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
					if existing && file != "good.json" {
						if err := b.storage.WriteFile(ctx, authKey, kind, file, []byte("existing credential"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
				var err error
				if feature {
					err = b.syncFeatureAuth(ctx, configKey, sync, "vendor_custom_end")
				} else {
					err = b.syncSharedAuth(ctx, configKey, authKey, files, "vendor_custom_end")
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, file := range files[:2] {
					data, err := b.storage.ReadFile(ctx, authKey, kind, file)
					if existing {
						if err != nil || string(data) != "existing credential" {
							t.Fatalf("%s changed: %s, %v", file, data, err)
						}
					} else if !os.IsNotExist(err) {
						t.Fatalf("%s copied: %s, %v", file, data, err)
					}
				}
				data, err := b.storage.ReadFile(ctx, authKey, kind, "good.json")
				if err != nil || string(data) != `{"key":"real-key"}` {
					t.Fatalf("healthy file not copied: %s, %v", data, err)
				}
			})
		}
	}
}

func testAuthSyncName(feature, existing bool) string {
	name := "tool"
	if feature {
		name = "feature"
	}
	if existing {
		name += "/existing-auth"
	} else {
		name += "/missing-auth"
	}
	return name
}
