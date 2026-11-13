// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/config"
	"enclave/internal/testutil"
)

func testGrant(id string, scopes ...string) Grant {
	return Grant{ID: id, ClientID: "client", ClientSecret: "client-secret", RefreshToken: "rt-" + id, Scopes: scopes,
		CreatedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
}

func TestStoreWritesPrivateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	home := t.TempDir()
	store := NewStore(home, "gws")
	if err := store.SaveGrant(testGrant("readonly", "b", "a", "a")); err != nil {
		t.Fatalf("SaveGrant: %v", err)
	}
	if err := store.SaveClient(Client{ClientID: "client", ClientSecret: "client-secret"}); err != nil {
		t.Fatalf("SaveClient: %v", err)
	}
	for _, dir := range []string{config.HostFiltersStateDir(home), config.HostFilterStateDir(home, "gws"), store.GrantsDir()} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, %v; want 0700", dir, info.Mode().Perm(), err)
		}
	}
	for _, file := range []string{filepath.Join(store.GrantsDir(), "readonly.json"), store.ClientPath()} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, %v; want 0600", file, info.Mode().Perm(), err)
		}
	}
	if !strings.HasPrefix(store.GrantsDir(), config.HostStateRootDir(home)) {
		t.Errorf("grants dir %s is not under the state root", store.GrantsDir())
	}
	// No temp file is left behind by the atomic write.
	entries, _ := os.ReadDir(store.GrantsDir())
	if len(entries) != 1 {
		t.Errorf("grants dir holds %d entries, want 1", len(entries))
	}

	g, err := store.Grant("readonly")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if g.Filter != "gws" || g.SchemaVersion != GrantSchemaVersion || !slices.Equal(g.Scopes, []string{"a", "b"}) || g.RefreshToken != "rt-readonly" {
		t.Fatalf("grant = %#v %v", g, g.Scopes)
	}
	client, ok, err := store.Client()
	if err != nil || !ok || client.ClientID != "client" {
		t.Fatalf("Client = %v, %v, %v", client, ok, err)
	}
}

func TestGrantRedactsSecrets(t *testing.T) {
	g := testGrant("readonly", "a")
	for _, text := range []string{fmt.Sprintf("%v", g), fmt.Sprintf("%+v", g), fmt.Sprintf("%#v", g), fmt.Sprint(Client{ClientID: "c", ClientSecret: "client-secret"})} {
		if strings.Contains(text, "rt-readonly") || strings.Contains(text, "client-secret") {
			t.Errorf("formatted value leaks a secret: %s", text)
		}
	}
}

func TestStoreRejectsBadIDsAndFiles(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home, "gws")
	for _, id := range []string{"", "../x", "A", ".hidden"} {
		if err := store.SaveGrant(testGrant(id, "a")); err == nil {
			t.Errorf("SaveGrant(%q) succeeded", id)
		}
	}
	if err := store.SaveGrant(Grant{ID: "x", ClientID: "c", RefreshToken: "r"}); err == nil {
		t.Error("SaveGrant accepted a grant without scopes")
	}

	if err := os.MkdirAll(store.GrantsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.GrantsDir(), "broken.json"), []byte(`{"refreshToken":"rt-secret"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGrant(testGrant("good", "a")); err != nil {
		t.Fatal(err)
	}
	grants, problems, err := store.Grants()
	if err != nil || len(grants) != 1 || grants[0].ID != "good" || len(problems) != 1 {
		t.Fatalf("Grants = %v, %v, %v", grants, problems, err)
	}
	if strings.Contains(problems[0], "rt-secret") {
		t.Errorf("problem quotes the file: %s", problems[0])
	}
	if _, err := store.Grant("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Grant(missing) = %v", err)
	}
	if err := store.DeleteGrant("good"); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if err := store.DeleteGrant("good"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("second DeleteGrant = %v", err)
	}
}

func TestParseClientSecretShapes(t *testing.T) {
	for _, doc := range []string{
		`{"installed":{"client_id":"id-1","client_secret":"s-1","redirect_uris":["http://localhost"]}}`,
		`{"web":{"client_id":"id-1","client_secret":"s-1"}}`,
		`{"client_id":"id-1","client_secret":"s-1"}`,
	} {
		c, err := ParseClientSecret([]byte(doc))
		if err != nil || c.ClientID != "id-1" || c.ClientSecret != "s-1" {
			t.Errorf("ParseClientSecret(%s) = %v, %v", doc, c, err)
		}
	}
	for _, doc := range []string{`{}`, `not json`, `{"installed":{"client_secret":"s"}}`, `{"client_id":"a b"}`} {
		if _, err := ParseClientSecret([]byte(doc)); err == nil {
			t.Errorf("ParseClientSecret(%s) succeeded", doc)
		}
	}
}

func TestDefaultGrantID(t *testing.T) {
	if got := DefaultGrantID("readonly", []string{"a"}); got != "readonly" {
		t.Errorf("DefaultGrantID(preset) = %q", got)
	}
	a := DefaultGrantID("", []string{"b", "a"})
	b := DefaultGrantID("", []string{"a", "b", "a"})
	if a != b || !strings.HasPrefix(a, "custom-") || len(a) != len("custom-")+8 {
		t.Errorf("DefaultGrantID = %q, %q", a, b)
	}
	if err := ValidateGrantID(a); err != nil {
		t.Errorf("default id is invalid: %v", err)
	}
}

func TestSelectGrantPrefersFewestScopesThenNewest(t *testing.T) {
	paths := testPaths(t)
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	f, err := Load(paths, "gws")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	store := NewStore(home, "gws")
	prefix := "https://www.googleapis.com/auth/"
	broad := testGrant("broad", prefix+"gmail.modify", prefix+"drive", prefix+"cloud-platform")
	exact := testGrant("readonly", prefix+"gmail.readonly", prefix+"drive.readonly")
	newer := exact
	newer.ID = "readonly-2"
	newer.CreatedAt = exact.CreatedAt.Add(time.Hour)
	narrow := testGrant("mail", prefix+"gmail.readonly")
	for _, g := range []Grant{broad, exact, newer, narrow} {
		if err := store.SaveGrant(g); err != nil {
			t.Fatal(err)
		}
	}
	p, err := ResolvePreset(f, home, "")
	if err != nil {
		t.Fatal(err)
	}
	required := f.RequiredScopes(p)
	g, ok, _, err := SelectGrant(f, store, required)
	if err != nil || !ok || g.ID != "readonly-2" {
		t.Fatalf("SelectGrant = %v, %v, %v; want readonly-2", g.ID, ok, err)
	}
	// Covered through scopeImplies only.
	if err := store.DeleteGrant("readonly"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteGrant("readonly-2"); err != nil {
		t.Fatal(err)
	}
	g, ok, _, _ = SelectGrant(f, store, required)
	if !ok || g.ID != "broad" {
		t.Fatalf("SelectGrant = %v, %v; want broad through scopeImplies", g.ID, ok)
	}
	if err := store.DeleteGrant("broad"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _, _ := SelectGrant(f, store, required); ok {
		t.Fatal("SelectGrant found a grant that does not cover drive.readonly")
	}

	// A grant file that does not load is reported, without its content.
	writeFile(t, filepath.Join(store.GrantsDir(), "broken.json"), `{"refreshToken":"rt-secret"`)
	_, ok, problems, err := SelectGrant(f, store, required)
	if err != nil || ok || len(problems) != 1 || !strings.Contains(problems[0], "grant broken of filter gws: invalid JSON") ||
		strings.Contains(problems[0], "rt-secret") {
		t.Fatalf("SelectGrant with a broken grant = %v, %v, %v", ok, problems, err)
	}
}

func TestLoginScopes(t *testing.T) {
	paths := testPaths(t)
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	f, err := Load(paths, "gws")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	prefix := "https://www.googleapis.com/auth/"

	preset, scopes, err := LoginScopes(f, home, "", nil)
	if err != nil || preset != "readonly" || !slices.Equal(scopes, []string{prefix + "drive.readonly", prefix + "gmail.readonly"}) {
		t.Fatalf("LoginScopes(default) = %q, %v, %v", preset, scopes, err)
	}
	// folder-edit needs drive (which implies drive.readonly and drive.file)
	// plus the resolver's drive.metadata.readonly, which drive implies too.
	preset, scopes, err = LoginScopes(f, home, "folder-edit", nil)
	if err != nil || preset != "folder-edit" || !slices.Equal(scopes, []string{prefix + "drive", prefix + "gmail.modify"}) {
		t.Fatalf("LoginScopes(folder-edit) = %q, %v, %v", preset, scopes, err)
	}
	preset, scopes, err = LoginScopes(f, home, "", []string{"drive", "drive.readonly"})
	if err != nil || preset != "" || !slices.Equal(scopes, []string{prefix + "drive"}) {
		t.Fatalf("LoginScopes(scopes) = %q, %v, %v", preset, scopes, err)
	}
	if _, _, err := LoginScopes(f, home, "nope", nil); err == nil {
		t.Fatal("LoginScopes accepted an unknown preset")
	}

	// A user preset shadows the shipped one of the same name.
	userDir := UserPresetsDir(home, "gws")
	writeFile(t, filepath.Join(userDir, "readonly.yaml"), "allow:\n  mail.read: {}\n")
	_, scopes, err = LoginScopes(f, home, "readonly", nil)
	if err != nil || !slices.Equal(scopes, []string{prefix + "gmail.readonly"}) {
		t.Fatalf("LoginScopes(user readonly) = %v, %v", scopes, err)
	}
}
