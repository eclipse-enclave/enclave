// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
	"enclave/internal/placeholderstore"
)

func nativeAuthFixture(t *testing.T) (model.Profile, string, string, string, string, string) {
	t.Helper()
	home, configDir, authDir := entrypointAuthDirs(t)
	real, views := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(real, claudeCredentialsFile), `{"claudeAiOauth":{"accessToken":"synthetic-native-oauth-access","refreshToken":"synthetic-native-oauth-refresh","expiresAt":300}}`)
	profile := model.Profile{
		Name: "claude",
		Providers: []model.ProviderConfig{{
			Name:      "anthropic",
			AuthFiles: []string{claudeCredentialsFile},
			AuthSecrets: []model.AuthFileSecret{{
				File:  claudeCredentialsFile,
				Codec: "claude-credentials",
				Fields: []model.AuthSecretField{
					{Pointer: "/claudeAiOauth/accessToken", Kind: "access", Release: "oauth"},
					{Pointer: "/claudeAiOauth/refreshToken", Kind: "refresh"},
				},
			}},
		}},
		Secrets: map[string]model.SecretConfig{
			"oauth": {Release: &model.SecretReleaseConfig{HTTP: &model.HTTPSecretReleaseConfig{
				Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer %s",
			}}},
		},
	}
	return profile, real, views, home, configDir, authDir
}

func nativeEntrypoint(t *testing.T, home, configDir, authDir, ops string, extraEnv ...string) ([]byte, error) {
	t.Helper()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "entrypoint.sh"), "true")
	cmd.Env = []string{"PATH=" + fakeJQDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home, "PROJECT_DIR=" + project, "TOOL=claude", "ENCLAVE_TOOLS_DIR=" + filepath.Join("..", "..", "extensions", "tools"), model.EnvToolConfigDir + "=" + configDir, model.EnvAuthDir + "=" + authDir, model.EnvAuthFiles + "=" + claudeCredentialsFile, model.EnvNativeAuthOps + "=" + ops, "ENCLAVE_AUTH_RECONCILE_LIB=" + filepath.Join("..", "..", "runtime-assets", "auth-reconcile.sh")}
	cmd.Env = append(cmd.Env, extraEnv...)
	return cmd.CombinedOutput()
}

func TestNativeAuthEntrypointExecutesClassifiedVerbs(t *testing.T) {
	for _, kind := range []string{"create", "keep link", "keep regular", "link idle", "changed regular", "raced create", "foreign link", "unknown verb"} {
		t.Run(kind, func(t *testing.T) {
			profile, real, views, home, configDir, authDir := nativeAuthFixture(t)
			first, err := placeholderstore.Project(real, views, "original", profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			sanitized, err := os.ReadFile(filepath.Join(first.Dir, claudeCredentialsFile))
			if err != nil {
				t.Fatal(err)
			}
			// Emulate the agent's view bind with only the sanitized synthetic fixture.
			writeFile(t, filepath.Join(authDir, claudeCredentialsFile), string(sanitized))
			native := filepath.Join(configDir, claudeCredentialsFile)
			switch kind {
			case "keep link", "foreign link":
				if err := os.Symlink(filepath.Join(authDir, claudeCredentialsFile), native); err != nil {
					t.Fatal(err)
				}
			case "keep regular", "changed regular", "link idle":
				writeFile(t, native, string(sanitized))
			}
			next, err := placeholderstore.Project(real, views, "next", profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			operations, err := placeholderstore.ClassifyNative(configDir, authDir, profile, next.Bindings, nil, kind == "link idle")
			if err != nil || len(operations) != 1 {
				t.Fatalf("classification: %v, %v", operations, err)
			}
			operation := operations[0]
			ops := operation.File + ":" + operation.Verb + ":" + operation.Digest
			switch kind {
			case "changed regular", "raced create":
				writeFile(t, native, `{"claudeAiOauth":{"accessToken":"raw-after-classification"}}`)
			case "foreign link":
				if err := os.Remove(native); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/foreign", native); err != nil {
					t.Fatal(err)
				}
			case "unknown verb":
				ops = claudeCredentialsFile + ":unexpected:"
			}
			out, err := nativeEntrypoint(t, home, configDir, authDir, ops)
			refuse := kind == "changed regular" || kind == "raced create" || kind == "foreign link" || kind == "unknown verb"
			if refuse {
				if err == nil {
					t.Fatal("entrypoint accepted changed or unclassified native path")
				}
				return
			}
			if err != nil {
				t.Fatalf("entrypoint: %v\n%s", err, out)
			}
			if kind == "keep regular" {
				got, err := os.ReadFile(native)
				if err != nil || !bytes.Equal(got, sanitized) {
					t.Fatal("entrypoint replaced sibling's regular placeholder file")
				}
				info, err := os.Lstat(native)
				if err != nil || !info.Mode().IsRegular() {
					t.Fatal("keep regular was relinked")
				}
			} else {
				assertSymlinkTarget(t, native, filepath.Join(authDir, claudeCredentialsFile))
			}
			canonical, err := os.ReadFile(filepath.Join(real, claudeCredentialsFile))
			if err != nil || strings.Contains(string(canonical), model.SecretPlaceholderMarker) {
				t.Fatal("entrypoint wrote placeholder into real store")
			}
		})
	}
}

func TestNativeAuthCreateRejectsDirectoryAppearingAtLink(t *testing.T) {
	realLn, err := exec.LookPath("ln")
	if err != nil {
		t.Fatal(err)
	}
	realMkdir, err := exec.LookPath("mkdir")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"directory", "symlink-to-directory"} {
		t.Run(kind, func(t *testing.T) {
			home, configDir, authDir := entrypointAuthDirs(t)
			native := filepath.Join(configDir, claudeCredentialsFile)
			foreign, shim := t.TempDir(), t.TempDir()
			script := `#!/bin/sh
for argument do destination=$argument; done
if [ "$destination" = "$ENCLAVE_RACE_NATIVE" ] && [ ! -e "$destination" ] && [ ! -L "$destination" ]; then
    case "$ENCLAVE_RACE_KIND" in
        directory) "$ENCLAVE_REAL_MKDIR" "$destination" || exit 98 ;;
        symlink-to-directory) "$ENCLAVE_REAL_LN" -s "$ENCLAVE_RACE_FOREIGN" "$destination" || exit 98 ;;
    esac
fi
exec "$ENCLAVE_REAL_LN" "$@"
`
			if err := os.WriteFile(filepath.Join(shim, "ln"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
			_, err := nativeEntrypoint(t, home, configDir, authDir, claudeCredentialsFile+":create:", "ENCLAVE_RACE_NATIVE="+native, "ENCLAVE_RACE_FOREIGN="+foreign, "ENCLAVE_RACE_KIND="+kind, "ENCLAVE_REAL_LN="+realLn, "ENCLAVE_REAL_MKDIR="+realMkdir)
			if err == nil {
				t.Fatal("entrypoint accepted a directory appearing immediately before link creation")
			}
			if _, err := os.Lstat(native); err != nil {
				t.Fatal("fixture did not interleave its directory at the destination")
			}
			entries, err := os.ReadDir(native)
			if err != nil || len(entries) != 0 {
				t.Fatal("link creation descended into the raced directory")
			}
		})
	}
}
