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
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestHarvestKeepsCanonicalGrantAndCreatesMetadataFile(t *testing.T) {
	real, config := t.TempDir(), t.TempDir()
	profile := claudeProfile()
	nativeFile(t, real, ".credentials.json", nativeOAuth(accessToken, refreshToken))
	view, err := Project(real, t.TempDir(), "session", profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The gateway has advanced since this client's view was written.
	current := `{"claudeAiOauth":{"accessToken":"new-access-synthetic","refreshToken":"new-refresh-synthetic","expiresAt":99999,"scopes":["new-scope"],"serverOnly":{"keep":true}}}`
	nativeFile(t, real, ".credentials.json", current)
	data, err := os.ReadFile(filepath.Join(view.Dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	oauth := doc["claudeAiOauth"].(map[string]any)
	oauth["subscriptionType"], oauth["rateLimitTier"] = "client-tier", nil
	oauth["unknownClientField"] = "discard"
	changed, _ := json.Marshal(doc)
	nativeFile(t, view.Dir, ".credentials.json", string(changed))
	nativeFile(t, view.Dir, "config.json", `{"hasCompletedOnboarding":true,"oauthAccount":{"displayName":"view-name"},"unknownKey":"discard"}`)
	if err := Harvest(real, view.Dir, config, profile); err != nil {
		t.Fatal(err)
	}
	native, err := ReadNative(real, profile.Providers[0].AuthSecrets[0])
	if err != nil {
		t.Fatal(err)
	}
	got := native.Document["claudeAiOauth"].(map[string]any)
	if got["accessToken"] != "new-access-synthetic" || got["refreshToken"] != "new-refresh-synthetic" || got["expiresAt"] != json.Number("99999") || got["subscriptionType"] != "client-tier" || got["serverOnly"] == nil || got["unknownClientField"] != nil {
		t.Fatalf("bad merge: keys=%v", got)
	}
	if scopes := got["scopes"].([]any); len(scopes) != 1 || scopes[0] != "new-scope" {
		t.Fatal("stale client scopes replaced grant")
	}
	meta, err := ReadNative(real, profile.Providers[0].AuthSecrets[1])
	if err != nil {
		t.Fatal(err)
	}
	if meta.Document["hasCompletedOnboarding"] != true || len(meta.Values) != 0 || meta.Document["unknownKey"] != nil {
		t.Fatal("first-login config metadata was not safely created")
	}
}

func TestHarvestRejectsStaleRawAndSecretBearingSources(t *testing.T) {
	for _, kind := range []string{"reset", "relogin", "raw token", "raw reflected metadata", "placeholder metadata", "foreign native path", "malformed view"} {
		t.Run(kind, func(t *testing.T) {
			real, config := t.TempDir(), t.TempDir()
			profile := claudeProfile()
			original := nativeOAuth(accessToken, refreshToken)
			nativeFile(t, real, ".credentials.json", original)
			view, err := Project(real, t.TempDir(), "session", profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			nativeFile(t, view.Dir, "config.json", `{"oauthAccount":{"displayName":"client-name"}}`)
			switch kind {
			case "reset":
				if err := os.Remove(filepath.Join(real, BindingsFile)); err != nil {
					t.Fatal(err)
				}
			case "relogin":
				if err := os.Remove(filepath.Join(real, BindingsFile)); err != nil {
					t.Fatal(err)
				}
				if _, err := Project(real, t.TempDir(), "new-login", profile, nil); err != nil {
					t.Fatal(err)
				}
			case "raw token":
				nativeFile(t, view.Dir, ".credentials.json", original)
			case "raw reflected metadata":
				nativeFile(t, view.Dir, "config.json", `{"oauthAccount":{"displayName":"prefix-`+refreshToken+`-suffix"}}`)
			case "placeholder metadata":
				nativeFile(t, view.Dir, "config.json", `{"oauthAccount":{"displayName":"ENCLAVE_SECRET_leak"}}`)
			case "foreign native path":
				nativeFile(t, config, ".credentials.json", original)
				if err := os.Remove(filepath.Join(view.Dir, "config.json")); err != nil {
					t.Fatal(err)
				}
			case "malformed view":
				nativeFile(t, view.Dir, ".credentials.json", `{"claudeAiOauth":`)
			}
			err = Harvest(real, view.Dir, config, profile)
			if kind != "reset" && err == nil {
				t.Fatal("invalid source skipped without warning")
			}
			data, e := os.ReadFile(filepath.Join(real, ".credentials.json"))
			if e != nil || string(data) != original {
				t.Fatal("invalid source changed canonical grant")
			}
			if _, e := os.Stat(filepath.Join(real, "config.json")); !os.IsNotExist(e) {
				t.Fatal("invalid source created canonical metadata")
			}
		})
	}
}

func TestHarvestNativePathMetadataWinsWithoutMutation(t *testing.T) {
	real, config := t.TempDir(), t.TempDir()
	profile := claudeProfile()
	nativeFile(t, real, "config.json", `{"primaryApiKey":"native-api-synthetic","oauthAccount":{"displayName":"original","organizationUuid":"keep"}}`)
	configured := []model.SecretReleaseEntry{{SecretID: "api", Placeholder: "ENCLAVE_SECRET_api", Value: "configured-api-synthetic", Hosts: []string{"api.anthropic.com"}}}
	view, err := Project(real, t.TempDir(), "api-session", profile, configured)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(view.Dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	nativeFile(t, config, "config.json", strings.Replace(string(data), "original", "native-newer", 1))
	before, err := os.ReadFile(filepath.Join(config, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Harvest(real, view.Dir, config, profile); err != nil {
		t.Fatal(err)
	}
	native, err := ReadNative(real, profile.Providers[0].AuthSecrets[1])
	if err != nil {
		t.Fatal(err)
	}
	account := native.Document["oauthAccount"].(map[string]any)
	if native.Values["/primaryApiKey"] != "native-api-synthetic" || account["displayName"] != "native-newer" || account["organizationUuid"] != "keep" {
		t.Fatal("API-only harvest lost canonical key or native-path precedence")
	}
	after, err := os.ReadFile(filepath.Join(config, "config.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("harvest mutated shared native path")
	}
}
