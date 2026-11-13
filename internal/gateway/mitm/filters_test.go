// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFiltersBuildsEngines(t *testing.T) {
	set, err := LoadFilters(writeFilterBundle(t, "readonly", nil), NewFilterClient())
	if err != nil {
		t.Fatalf("LoadFilters() error = %v", err)
	}
	engines := set.Engines()
	if len(engines) != 1 || engines[0].Filter() != "gws" || engines[0].Preset() != "readonly" {
		t.Fatalf("engines = %v, want gws with preset readonly", engines)
	}
	if set.For(gmailHost, "") == nil || set.For(ungovernedHost, "") != nil {
		t.Fatal("the loaded set governs the wrong hosts")
	}
}

// Any problem with the directory fails the whole load: the gateway never runs
// with part of the session's filters. Errors never quote credential material.
func TestLoadFiltersFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, dir string){
		"missing credential": func(t *testing.T, dir string) {
			mustRemove(t, filepath.Join(dir, "gws.credential.json"))
		},
		"missing policy": func(t *testing.T, dir string) {
			mustRemove(t, filepath.Join(dir, "gws.policy.json"))
		},
		"unexpected file": func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "notes.txt"), "x")
		},
		"invalid name": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "gws.policy.json"), filepath.Join(dir, "GWS.policy.json"))
			mustRename(t, filepath.Join(dir, "gws.credential.json"), filepath.Join(dir, "GWS.credential.json"))
		},
		"policy of another filter": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "gws.policy.json"), filepath.Join(dir, "other.policy.json"))
			mustRename(t, filepath.Join(dir, "gws.credential.json"), filepath.Join(dir, "other.credential.json"))
		},
		"invalid policy": func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "gws.policy.json"), `{"schemaVersion":"1","kind":"filter-policy","filter":"gws"}`)
		},
		"invalid credential": func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "gws.credential.json"), `{"refreshToken":"`+filterRefreshToken+`",`)
		},
		"symlinked credential": func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "cred.json")
			mustRename(t, filepath.Join(dir, "gws.credential.json"), target)
			if err := os.Symlink(target, filepath.Join(dir, "gws.credential.json")); err != nil {
				t.Fatal(err)
			}
		},
		"subdirectory": func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, "x.policy.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"empty": func(t *testing.T, dir string) {
			mustRemove(t, filepath.Join(dir, "gws.policy.json"))
			mustRemove(t, filepath.Join(dir, "gws.credential.json"))
		},
		"missing directory": func(t *testing.T, dir string) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeFilterBundle(t, "readonly", nil)
			mutate(t, dir)
			_, err := LoadFilters(dir, NewFilterClient())
			if err == nil {
				t.Fatal("LoadFilters() accepted a broken filter directory")
			}
			assertNoCredentialMaterial(t, "error", err.Error())
		})
	}
}

func TestFilterClientNeverFollowsRedirects(t *testing.T) {
	client := NewFilterClient()
	if client.CheckRedirect == nil {
		t.Fatal("the filter client follows redirects")
	}
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() = %v, want ErrUseLastResponse", err)
	}
	if transport, ok := client.Transport.(*http.Transport); !ok || transport.Proxy != nil || transport.TLSClientConfig != nil {
		t.Fatal("the filter client must dial directly and verify TLS against the system roots")
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
