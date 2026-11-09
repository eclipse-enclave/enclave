// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestProjectChecksCredentialsAcrossTheWholeView(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, escaped := range []bool{false, true} {
			for _, direction := range []string{"oauth-to-config", "api-to-oauth", "env-to-config"} {
				t.Run(direction+"/reverse="+strconv.FormatBool(reverse)+"/escaped="+strconv.FormatBool(escaped), func(t *testing.T) {
					real, views := t.TempDir(), t.TempDir()
					profile := claudeProfile()
					if reverse {
						slices.Reverse(profile.Providers[0].AuthSecrets)
					}
					access, refresh, api, env := accessToken, refreshToken, "synthetic-native-api-credential", "synthetic-env-credential"
					if escaped {
						access += "<escaped"
						api += "<escaped"
						env += "<escaped"
					}
					oauth := map[string]any{"claudeAiOauth": map[string]any{"accessToken": access, "refreshToken": refresh}}
					config := map[string]any{}
					switch direction {
					case "oauth-to-config":
						config["oauthAccount"] = map[string]any{"displayName": access}
					case "api-to-oauth":
						config["primaryApiKey"] = api
						oauth["claudeAiOauth"].(map[string]any)["subscriptionType"] = api
					case "env-to-config":
						config["oauthAccount"] = map[string]any{"displayName": env}
					}
					for name, doc := range map[string]any{".credentials.json": oauth, "config.json": config} {
						data, err := json.Marshal(doc)
						if err != nil {
							t.Fatal(err)
						}
						nativeFile(t, real, name, string(data))
					}
					configured := []model.SecretReleaseEntry{{SecretID: "api", Placeholder: "ENCLAVE_SECRET_configured-env", Value: env}}
					if _, err := Project(real, views, "cross-file", profile, configured); err == nil || !strings.Contains(err.Error(), "credential value remains") {
						t.Fatalf("cross-file credential not refused: %v", err)
					}
					entries, err := os.ReadDir(views)
					if err != nil || len(entries) != 0 {
						t.Fatal("unsafe projection left a view or staging directory")
					}
					if _, err := os.Stat(filepath.Join(real, BindingsFile)); !os.IsNotExist(err) {
						t.Fatal("unsafe projection published bindings")
					}
				})
			}
		}
	}
}
