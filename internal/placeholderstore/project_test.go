// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/model"
)

const accessToken = "sk-ant-oat01-synthetic-access-credential-never-publish"
const refreshToken = "sk-ant-ort01-synthetic-refresh-credential-never-publish"

func claudeProfile() model.Profile {
	return model.Profile{Providers: []model.ProviderConfig{{Name: "anthropic", AuthFiles: []string{".credentials.json", "config.json"}, AuthSecrets: []model.AuthFileSecret{
		{File: ".credentials.json", Codec: "claude-credentials", Fields: []model.AuthSecretField{
			{Pointer: "/claudeAiOauth/accessToken", Kind: "access", Release: "oauth", Placeholder: &model.SecretPlaceholderConfig{Prefix: "sk-ant-oat01-", Random: model.SecretPlaceholderRandom{Encoding: "base64url", Length: 86}}},
			{Pointer: "/claudeAiOauth/refreshToken", Kind: "refresh", Placeholder: &model.SecretPlaceholderConfig{Prefix: "sk-ant-ort01-", Random: model.SecretPlaceholderRandom{Encoding: "base64url", Length: 86}}},
		}},
		{File: "config.json", Codec: "claude-config", Fields: []model.AuthSecretField{{Pointer: "/primaryApiKey", Kind: "apiKey", Release: "api"}, {Pointer: "/anthropicApiKey", Kind: "apiKey", Release: "api"}}},
	}}}, Secrets: map[string]model.SecretConfig{"oauth": {Release: &model.SecretReleaseConfig{HTTP: &model.HTTPSecretReleaseConfig{Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer %s"}}}}}
}

func nativeFile(t *testing.T, dir, name, data string) {
	t.Helper()
	target := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func nativeOAuth(access, refresh string) string {
	data, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": access, "refreshToken": refresh, "expiresAt": 1234, "scopes": []string{"user:inference"}}})
	return string(data)
}

func TestProjectClaudeCreatesPrivateStableViews(t *testing.T) {
	real, views := t.TempDir(), t.TempDir()
	source := nativeOAuth(accessToken, refreshToken)
	nativeFile(t, real, ".credentials.json", source)
	nativeFile(t, real, ".credentials.json.bak", source)
	nativeFile(t, real, "unrelated", "do not copy")
	first, err := Project(real, views, "session-one", claudeProfile(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := os.ReadFile(filepath.Join(first.Dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{accessToken, refreshToken} {
		if bytes.Contains(projected, []byte(raw)) {
			t.Fatal("view contains native credential")
		}
	}
	var doc struct {
		OAuth struct {
			Access  string `json:"accessToken"`
			Refresh string `json:"refreshToken"`
			Expires int    `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(projected, &doc); err != nil {
		t.Fatal(err)
	}
	for _, token := range []struct{ value, prefix string }{{doc.OAuth.Access, "sk-ant-oat01-"}, {doc.OAuth.Refresh, "sk-ant-ort01-"}} {
		body := strings.TrimPrefix(token.value, token.prefix)
		if body == token.value || len(body) != 86 || !strings.Contains(body, model.SecretPlaceholderMarker) {
			t.Fatalf("invalid placeholder shape: %q", token.value)
		}
		for _, char := range body {
			if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", char) {
				t.Fatal("invalid placeholder alphabet")
			}
		}
	}
	if doc.OAuth.Expires != 1234 || len(first.Releases) != 1 || first.Releases[0].Value != accessToken || first.Releases[0].Placeholder != doc.OAuth.Access {
		t.Fatal("native metadata or access-only release lost")
	}
	if len(first.Bindings.Bindings) != 2 {
		t.Fatal("missing bindings")
	}
	bindings, err := os.ReadFile(filepath.Join(real, BindingsFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bindings, []byte(accessToken)) || bytes.Contains(bindings, []byte(refreshToken)) {
		t.Fatal("bindings contain credentials")
	}
	original, err := os.ReadFile(filepath.Join(real, ".credentials.json"))
	if err != nil || string(original) != source {
		t.Fatal("projection changed native file")
	}
	entries, err := os.ReadDir(first.Dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("view copied undeclared files")
	}
	info, err := os.Stat(first.Dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("view directory is not private")
	}
	info, err = os.Stat(filepath.Join(first.Dir, ".credentials.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("view file is not private")
	}
	nativeFile(t, real, ".credentials.json", nativeOAuth(accessToken+"-rotated", refreshToken+"-rotated"))
	second, err := Project(real, views, "session-two", claudeProfile(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Dir == first.Dir || !reflect.DeepEqual(first.Bindings, second.Bindings) || second.Releases[0].Value != accessToken+"-rotated" {
		t.Fatal("sibling projection lost stable binding or current credential")
	}
	firstAgain, err := os.ReadFile(filepath.Join(first.Dir, ".credentials.json"))
	if err != nil || !bytes.Equal(firstAgain, projected) {
		t.Fatal("sibling view was modified")
	}
}

func TestProjectRefusesAmbiguousNativeFilesWithoutPublishing(t *testing.T) {
	for name, data := range map[string]string{
		"duplicate":                           `{"claudeAiOauth":{"accessToken":"first","accessToken":"second"}}`,
		"wrong type":                          `{"claudeAiOauth":{"accessToken":123}}`,
		"trailing":                            `{} {}`,
		"placeholder in real store":           nativeOAuth("ENCLAVE_SECRET_not-a-real-grant", refreshToken),
		"raw copied into metadata":            `{"claudeAiOauth":{"accessToken":"same-raw-credential","refreshToken":"other-raw-credential","subscriptionType":"same-raw-credential"}}`,
		"JSON-escaped credential in metadata": `{"claudeAiOauth":{"accessToken":"escaped<credential","subscriptionType":"escaped<credential"}}`,
		"oversize":                            strings.Repeat("x", maxFileBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			real, views := t.TempDir(), t.TempDir()
			nativeFile(t, real, ".credentials.json", data)
			if _, err := Project(real, views, "session", claudeProfile(), nil); err == nil {
				t.Fatal("accepted unsafe native file")
			}
			entries, err := os.ReadDir(views)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed projection published a partial view")
			}
			if _, err := os.Stat(filepath.Join(real, BindingsFile)); !os.IsNotExist(err) {
				t.Fatal("failed projection published bindings")
			}
		})
	}
}

func TestProjectRejectsSymlinksAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"file symlink", "parent symlink", "in-root file symlink", "in-root parent symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			real, views := t.TempDir(), t.TempDir()
			profile := claudeProfile()
			switch kind {
			case "file symlink":
				outside := t.TempDir()
				nativeFile(t, outside, "auth", nativeOAuth(accessToken, refreshToken))
				if err := os.Symlink(filepath.Join(outside, "auth"), filepath.Join(real, ".credentials.json")); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				outside := t.TempDir()
				nativeFile(t, outside, ".credentials.json", nativeOAuth(accessToken, refreshToken))
				if err := os.Symlink(outside, filepath.Join(real, "parent")); err != nil {
					t.Fatal(err)
				}
				profile.Providers[0].AuthSecrets[0].File = "parent/.credentials.json"
			case "in-root file symlink":
				nativeFile(t, real, "inside-auth", nativeOAuth(accessToken, refreshToken))
				if err := os.Symlink("inside-auth", filepath.Join(real, ".credentials.json")); err != nil {
					t.Fatal(err)
				}
			case "in-root parent symlink":
				nativeFile(t, real, "inside/.credentials.json", nativeOAuth(accessToken, refreshToken))
				if err := os.Symlink("inside", filepath.Join(real, "parent")); err != nil {
					t.Fatal(err)
				}
				profile.Providers[0].AuthSecrets[0].File = "parent/.credentials.json"
			case "directory":
				if err := os.Mkdir(filepath.Join(real, ".credentials.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Project(real, views, "session", profile, nil); err == nil {
				t.Fatal("accepted unsafe native path")
			}
		})
	}
}

func TestProjectAPIKeyRequiresConfiguredRelease(t *testing.T) {
	real, views := t.TempDir(), t.TempDir()
	nativeFile(t, real, "config.json", `{"primaryApiKey":"raw-native-api-key","anthropicApiKey":"other-native-api-key","allowTelemetry":false,"forceLoginMethod":"console","hasCompletedOnboarding":true,"lastOnboardingVersion":"999.999.999","oauthAccount":{"emailAddress":"fixture@example.invalid"}}`)
	if _, err := Project(real, views, "no-env", claudeProfile(), nil); err == nil || !strings.Contains(err.Error(), "config.json") {
		t.Fatal("native API key without env credential must refuse, naming the file")
	}
	configured := []model.SecretReleaseEntry{{SecretID: "api", Placeholder: "ENCLAVE_SECRET_configured-env", Value: "configured-env-credential", Hosts: []string{"api.anthropic.com"}, Header: "x-api-key"}}
	view, err := Project(real, views, "configured", claudeProfile(), configured)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(view.Dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("raw-native")) || bytes.Contains(data, []byte("other-native")) || bytes.Count(data, []byte(configured[0].Placeholder)) != 2 || len(view.Releases) != 2 {
		t.Fatal("API fields did not use configured placeholder")
	}
	configured[0].Placeholder = "ENCLAVE_SECRET_new-session-env"
	configured[0].Value = "new-configured-credential"
	next, err := Project(real, views, "next", claudeProfile(), configured)
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range next.Releases {
		if release.Placeholder != "ENCLAVE_SECRET_configured-env" || release.Value != configured[0].Value {
			t.Fatal("existing native alias did not follow current env credential")
		}
	}
}

func TestProjectEmptyIdentityAndMetadataOnlyConfig(t *testing.T) {
	for _, data := range []string{"", `{"allowTelemetry":false,"hasCompletedOnboarding":true}`} {
		real, views := t.TempDir(), t.TempDir()
		if data != "" {
			nativeFile(t, real, "config.json", data)
		}
		view, err := Project(real, views, "empty", claudeProfile(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(view.Releases) != 0 || len(view.Bindings.Bindings) != 0 {
			t.Fatal("empty identity acquired credentials")
		}
		if data != "" {
			got, err := os.ReadFile(filepath.Join(view.Dir, "config.json"))
			if err != nil || string(got) != data {
				t.Fatal("metadata-only native config changed")
			}
		}
	}
}

func TestProjectRejectsCredentialsNestedInMetadata(t *testing.T) {
	for _, data := range []string{
		`{"oauthAccount":{"emailAddress":{"token":"undeclared-nested-credential"}}}`,
		`{"allowTelemetry":{"token":"undeclared-nested-credential"}}`,
	} {
		real, views := t.TempDir(), t.TempDir()
		nativeFile(t, real, "config.json", data)
		if _, err := Project(real, views, "unsafe-metadata", claudeProfile(), nil); err == nil {
			t.Fatal("copied undeclared credentials through metadata")
		}
	}
}

func TestResetDoesNotResurrectCredentialsAndFreshLoginProjects(t *testing.T) {
	real, views := t.TempDir(), t.TempDir()
	nativeFile(t, real, ".credentials.json", nativeOAuth(accessToken, refreshToken))
	first, err := Project(real, views, "before-reset", claudeProfile(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{".credentials.json", BindingsFile} {
		if err := os.Remove(filepath.Join(real, file)); err != nil {
			t.Fatal(err)
		}
	}
	empty, err := Project(real, views, "after-reset", claudeProfile(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Bindings.Bindings) != 0 || len(empty.Releases) != 0 {
		t.Fatal("reset resurrected native credentials")
	}
	nativeFile(t, real, ".credentials.json", nativeOAuth(accessToken+"-fresh-login", refreshToken+"-fresh-login"))
	fresh, err := Project(real, views, "fresh-login", claudeProfile(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Releases) != 1 || fresh.Releases[0].Value != accessToken+"-fresh-login" || fresh.Releases[0].Placeholder == first.Releases[0].Placeholder {
		t.Fatal("fresh login retained revoked bindings or lost credentials")
	}
}

func TestLoadBindingsRejectsAmbiguousMetadata(t *testing.T) {
	for _, data := range []string{
		`{"bindings":[]}`,
		`{"schema":1,"schema":1,"bindings":[]}`,
		`{"schema":1,"bindings":null}`,
		`{"schema":1,"bindings":[],"grants":[]}`,
		`{"schema":1,"bindings":[]} {}`,
		`{"schema":1,"bindings":[{"provider":"p","file":"a.json","pointer":"/access","kind":"unknown","placeholder":"ENCLAVE_SECRET_example"}]}`,
		`{"schema":1,"bindings":[{"provider":"p","file":"a.json","pointer":"/access","kind":"access","placeholder":"not-a-placeholder"}]}`,
		`{"schema":1,"bindings":[{"provider":"p","file":"a.json","pointer":"/access","kind":"access","shape":"unknown","placeholder":"ENCLAVE_SECRET_example"}]}`,
		`{"schema":1,"bindings":[{"provider":"p","file":"a.json","pointer":"/access","kind":"refresh","shape":"jwt","placeholder":"ENCLAVE_SECRET_example"}]}`,
		`{"schema":1,"bindings":[{"provider":"p","file":"a.json","pointer":"/access","kind":"access","shape":"jwt","placeholder":"prefix.ENCLAVE_SECRET_suffix"}]}`,
	} {
		real := t.TempDir()
		nativeFile(t, real, BindingsFile, data)
		if _, err := LoadBindings(real); err == nil {
			t.Fatal("accepted invalid binding metadata")
		}
	}
}

func TestProjectStripsUnknownKeysWithoutChangingNativeFiles(t *testing.T) {
	const unknown = "synthetic-unknown-credential-not-for-the-view"
	for _, file := range []string{".credentials.json", "config.json"} {
		t.Run(file, func(t *testing.T) {
			real, views := t.TempDir(), t.TempDir()
			nativeFile(t, real, ".credentials.json", nativeOAuth(accessToken, refreshToken))
			var doc map[string]any
			if file == ".credentials.json" {
				doc = map[string]any{
					"claudeAiOauth":   map[string]any{"accessToken": accessToken, "refreshToken": refreshToken, "newCredential": unknown},
					"futureClientKey": map[string]any{"token": unknown},
				}
			} else {
				doc = map[string]any{
					"oauthAccount":    map[string]any{"displayName": "fixture-user", "accessToken": unknown},
					"futureClientKey": map[string]any{"token": unknown},
				}
			}
			source, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			nativeFile(t, real, file, string(source))
			view, err := Project(real, views, "strip-unknown", claudeProfile(), nil)
			if err != nil {
				t.Fatal(err)
			}
			projected, err := os.ReadFile(filepath.Join(view.Dir, file))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(projected, []byte(unknown)) || bytes.Contains(projected, []byte("futureClientKey")) || bytes.Contains(projected, []byte("newCredential")) {
				t.Fatal("unknown credential-bearing keys reached the view")
			}
			if file == "config.json" && !bytes.Contains(projected, []byte("fixture-user")) {
				t.Fatal("known metadata was removed with unknown keys")
			}
			original, err := os.ReadFile(filepath.Join(real, file))
			if err != nil || !bytes.Equal(original, source) {
				t.Fatal("projection changed the canonical native file")
			}
		})
	}
}

func TestProviderMapWildcardKeysAndEscaping(t *testing.T) {
	file := model.AuthFileSecret{File: "auth.json", Codec: "provider-map", Fields: []model.AuthSecretField{{Pointer: "/*/key", Kind: "apiKey", Release: "api"}}}
	profile := model.Profile{Providers: []model.ProviderConfig{{Name: "map", AuthSecrets: []model.AuthFileSecret{file}}}}
	real := t.TempDir()
	nativeFile(t, real, file.File, `{"https://models.invalid/~slot":{"type":"api","key":"synthetic-first"},"second":{"type":"api_key","key":"synthetic-second"}}`)
	placeholder, err := model.NewSecretPlaceholder(nil)
	if err != nil {
		t.Fatal(err)
	}
	configured := []model.SecretReleaseEntry{{SecretID: "api", Placeholder: placeholder, Value: "synthetic-configured"}}
	view, err := Project(real, t.TempDir(), "wildcard", profile, configured)
	if err != nil || len(view.Bindings.Bindings) != 2 {
		t.Fatal("wildcard projection failed", err)
	}
	for _, binding := range view.Bindings.Bindings {
		parts, err := model.AuthPointerSegments(binding.Pointer)
		if err != nil || parts[0] != binding.Provider || parts[1] != "key" {
			t.Fatal("concrete escaped binding lost its namespace")
		}
	}
	data, err := os.ReadFile(filepath.Join(view.Dir, file.File))
	if err != nil || strings.Contains(string(data), "synthetic-first") || strings.Contains(string(data), "synthetic-second") {
		t.Fatal("wildcard leaf replacement leaked")
	}
	if ops, err := ClassifyNative(view.Dir, "/auth", profile, view.Bindings, nil, false); err != nil || len(ops) != 1 || ops[0].Verb != "keep" {
		t.Fatal("wildcard ownership proof failed", err)
	}
	profile.Providers[0].AuthSecrets[0].Fields = append(file.Fields, model.AuthSecretField{Pointer: "/second/key", Kind: "apiKey", Release: "api"})
	if _, err := Project(real, t.TempDir(), "overlap", profile, configured); err == nil {
		t.Fatal("overlapping wildcard declarations were accepted")
	}
}

func TestProviderMapErrorsDoNotEchoUntrustedKeys(t *testing.T) {
	real := t.TempDir()
	const raw = "synthetic-private-key"
	nativeFile(t, real, "auth.json", `{"`+raw+`":{"type":"api","key":"value"}}`)
	profile := model.Profile{Providers: []model.ProviderConfig{{AuthSecrets: []model.AuthFileSecret{{File: "auth.json", Codec: "provider-map", Fields: []model.AuthSecretField{{Pointer: "/known/key", Kind: "apiKey", Release: "api"}}}}}}}
	if _, err := Project(real, t.TempDir(), "unknown", profile, nil); err == nil || strings.Contains(err.Error(), raw) {
		t.Fatal("untrusted provider name echoed in error")
	}
}

func TestMarkedNativeAPIKeyStillParticipatesInContainment(t *testing.T) {
	real := t.TempDir()
	const raw = "synthetic-native-keyENCLAVE_SECRET_tail"
	doc := map[string]any{"claudeAiOauth": map[string]any{"accessToken": accessToken, "refreshToken": refreshToken, "subscriptionType": raw}}
	data, _ := json.Marshal(doc)
	nativeFile(t, real, ".credentials.json", string(data))
	nativeFile(t, real, "config.json", `{"primaryApiKey":"`+raw+`"}`)
	placeholder, err := model.NewSecretPlaceholder(nil)
	if err != nil {
		t.Fatal(err)
	}
	configured := []model.SecretReleaseEntry{{SecretID: "api", Placeholder: placeholder, Value: "synthetic-configured-key"}}
	if _, err := Project(real, t.TempDir(), "marked", claudeProfile(), configured); err == nil {
		t.Fatal("an embedded marker hid a raw API key copied into allowed metadata")
	}
	forged := Bindings{Schema: 1, Bindings: []Binding{{Provider: "anthropic", File: "config.json", Pointer: "/primaryApiKey", Kind: "apiKey", Placeholder: raw}}}
	if err := SaveBindings(real, forged); err != nil {
		t.Fatal(err)
	}
	if _, err := Project(real, t.TempDir(), "forged", claudeProfile(), configured); err == nil {
		t.Fatal("a forged marker binding hid a real native API value")
	}
}
