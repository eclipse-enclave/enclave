// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"enclave/internal/cli"
	"enclave/internal/filterhost"
	"enclave/internal/model"
	"enclave/internal/testutil"
)

// filterTestEnv installs the gws fixture with its OAuth endpoints on a fake
// server that approves every authorization request.
type filterTestEnv struct {
	env     filterCommandEnv
	out     *bytes.Buffer
	revoked []string
	mu      sync.Mutex
}

func newFilterTestEnv(t *testing.T) *filterTestEnv {
	t.Helper()
	te := &filterTestEnv{out: &bytes.Buffer{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=c&state="+q.Get("state"), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "refresh_token": "rt-" + r.PostForm.Get("client_id"),
			"scope": "https://www.googleapis.com/auth/drive.readonly https://www.googleapis.com/auth/gmail.readonly",
		})
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		te.mu.Lock()
		te.revoked = append(te.revoked, r.PostForm.Get("token"))
		te.mu.Unlock()
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	root := t.TempDir()
	paths := model.Paths{UserFilterExtensionsDir: filepath.Join(root, "extensions", "filters")}
	dir := testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	specPath := filepath.Join(dir, "spec.yaml")
	data, err := os.ReadFile(specPath) // #nosec G304 -- test fixture.
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer(
		"https://accounts.google.com/o/oauth2/auth", server.URL+"/auth",
		"https://oauth2.googleapis.com/token", server.URL+"/token",
		"https://oauth2.googleapis.com/revoke", server.URL+"/revoke",
	).Replace(string(data))
	if err := os.WriteFile(specPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	te.env = filterCommandEnv{
		paths:      paths,
		home:       filepath.Join(root, "home"),
		out:        te.out,
		httpClient: server.Client(),
		openBrowser: func(url string) error {
			go func() {
				resp, err := server.Client().Get(url) // #nosec G107 -- test URL.
				if err == nil {
					_ = resp.Body.Close()
				}
			}()
			return nil
		},
	}
	return te
}

func TestFilterLoginStoresClientAndGrant(t *testing.T) {
	te := newFilterTestEnv(t)
	ctx := context.Background()

	err := te.env.login(ctx, cli.FilterRequest{Name: "gws"})
	if err == nil || !strings.Contains(err.Error(), "--client-secret") || !strings.Contains(err.Error(), "Create a Desktop OAuth client") {
		t.Fatalf("login without a client = %v", err)
	}

	secretFile := filepath.Join(t.TempDir(), "client_secret.json")
	writeAppFile(t, secretFile, `{"installed":{"client_id":"client-a","client_secret":"secret-a"}}`, 0o600)
	if err := te.env.login(ctx, cli.FilterRequest{Name: "gws", ClientSecret: secretFile}); err != nil {
		t.Fatalf("login: %v", err)
	}
	store := filterhost.NewStore(te.env.home, "gws")
	g, err := store.Grant("readonly")
	if err != nil || g.ClientID != "client-a" || g.RefreshToken != "rt-client-a" {
		t.Fatalf("stored grant = %+v, %v", g, err)
	}
	if client, ok, _ := store.Client(); !ok || client.ClientID != "client-a" {
		t.Fatalf("client not stored: %v", client)
	}
	// The stored client serves the next login.
	if err := te.env.login(ctx, cli.FilterRequest{Name: "gws", Scopes: []string{"gmail.readonly"}, Grant: "mail"}); err != nil {
		t.Fatalf("second login: %v", err)
	}
	if _, err := store.Grant("mail"); err != nil {
		t.Fatalf("grant mail: %v", err)
	}
	if strings.Contains(te.out.String(), "secret-a") || strings.Contains(te.out.String(), "rt-client-a") {
		t.Fatalf("output leaks a secret:\n%s", te.out.String())
	}

	te.out.Reset()
	if err := te.env.grants(cli.FilterRequest{Name: "gws"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.out.String(), "mail") || !strings.Contains(te.out.String(), "readonly") || strings.Contains(te.out.String(), "rt-") {
		t.Fatalf("grants output = %q", te.out.String())
	}
}

func TestFilterLoginImport(t *testing.T) {
	te := newFilterTestEnv(t)
	file := filepath.Join(t.TempDir(), "authorized_user.json")
	writeAppFile(t, file, `{"client_id":"client-b","client_secret":"s","refresh_token":"rt-old"}`, 0o600)
	if err := te.env.login(context.Background(), cli.FilterRequest{Name: "gws", Import: file, Preset: "readonly"}); err != nil {
		t.Fatalf("import: %v", err)
	}
	g, err := filterhost.NewStore(te.env.home, "gws").Grant("readonly")
	if err != nil || g.ClientID != "client-b" || g.RefreshToken != "rt-client-b" || g.Source != filterhost.GrantSourceImport {
		t.Fatalf("imported grant = %+v, %v", g, err)
	}
}

func saveTestGrants(t *testing.T, store filterhost.Store, grants ...filterhost.Grant) {
	t.Helper()
	for _, g := range grants {
		if g.RefreshToken == "" {
			g.RefreshToken = "rt-" + g.ID
		}
		g.Scopes = []string{"s"}
		if err := store.SaveGrant(g); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFilterLogoutRevokes(t *testing.T) {
	te := newFilterTestEnv(t)
	store := filterhost.NewStore(te.env.home, "gws")
	saveTestGrants(t, store, filterhost.Grant{ID: "a", ClientID: "c1"}, filterhost.Grant{ID: "b", ClientID: "c2"})
	ctx := context.Background()
	if err := te.env.logout(ctx, cli.FilterRequest{Name: "gws"}); err == nil || !strings.Contains(err.Error(), "--grant <id> or --all") {
		t.Fatalf("ambiguous logout = %v", err)
	}
	if err := te.env.logout(ctx, cli.FilterRequest{Name: "gws", Grant: "a"}); err != nil {
		t.Fatalf("logout a: %v", err)
	}
	if err := te.env.logout(ctx, cli.FilterRequest{Name: "gws", All: true}); err != nil {
		t.Fatalf("logout --all: %v", err)
	}
	grants, _, _ := store.Grants()
	if len(grants) != 0 || strings.Join(te.revoked, ",") != "rt-a,rt-b" {
		t.Fatalf("grants left %v, revoked %v", grants, te.revoked)
	}
}

// Revoking a grant ends every grant of its OAuth client at Google, so logout
// keeps the token valid while another grant of the client stays.
func TestFilterLogoutSkipsRevocationForSharedClients(t *testing.T) {
	te := newFilterTestEnv(t)
	store := filterhost.NewStore(te.env.home, "gws")
	saveTestGrants(t, store, filterhost.Grant{ID: "a", ClientID: "c"}, filterhost.Grant{ID: "b", ClientID: "c"}, filterhost.Grant{ID: "d", ClientID: "c"})
	ctx := context.Background()
	_, stderr := captureOutput(t, func() {
		if err := te.env.logout(ctx, cli.FilterRequest{Name: "gws", Grant: "a"}); err != nil {
			t.Fatalf("logout a: %v", err)
		}
	})
	if len(te.revoked) != 0 || !strings.Contains(stderr, "Grant a shares its OAuth client with grant b, so it is deleted without revoking it") {
		t.Fatalf("revoked %v, stderr %q", te.revoked, stderr)
	}
	if _, err := store.Grant("a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("grant a was not deleted: %v", err)
	}
	// Nothing remains after --all, so both are revoked.
	if err := te.env.logout(ctx, cli.FilterRequest{Name: "gws", All: true}); err != nil {
		t.Fatalf("logout --all: %v", err)
	}
	if strings.Join(te.revoked, ",") != "rt-b,rt-d" {
		t.Fatalf("revoked %v", te.revoked)
	}
}

func TestFilterLogoutKeepsImportedGrantsValid(t *testing.T) {
	te := newFilterTestEnv(t)
	file := filepath.Join(t.TempDir(), "authorized_user.json")
	writeAppFile(t, file, `{"client_id":"client-b","client_secret":"s","refresh_token":"rt-old"}`, 0o600)
	ctx := context.Background()
	if err := te.env.login(ctx, cli.FilterRequest{Name: "gws", Import: file, Preset: "readonly"}); err != nil {
		t.Fatalf("import: %v", err)
	}
	store := filterhost.NewStore(te.env.home, "gws")
	// A login with the client the import came from.
	saveTestGrants(t, store, filterhost.Grant{ID: "mine", ClientID: "client-b"})

	te.out.Reset()
	if err := te.env.grants(cli.FilterRequest{Name: "gws"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.out.String(), "SOURCE") || !regexp.MustCompile(`(?m)^readonly\s+import\s`).MatchString(te.out.String()) ||
		!regexp.MustCompile(`(?m)^mine\s+login\s`).MatchString(te.out.String()) {
		t.Fatalf("grants output = %q", te.out.String())
	}

	stdout, stderr := captureOutput(t, func() {
		if err := te.env.logout(ctx, cli.FilterRequest{Name: "gws", All: true}); err != nil {
			t.Fatalf("logout --all: %v", err)
		}
	})
	if len(te.revoked) != 0 {
		t.Fatalf("revoked %v", te.revoked)
	}
	if !strings.Contains(stdout, "Grant readonly was imported, so it is deleted without revoking it") ||
		!strings.Contains(stderr, "Grant mine shares its OAuth client with grant readonly, so it is deleted without revoking it") {
		t.Fatalf("stdout %q, stderr %q", stdout, stderr)
	}
	if grants, _, _ := store.Grants(); len(grants) != 0 {
		t.Fatalf("grants left: %v", grants)
	}
}

func TestFilterLogoutWithoutUsableSpec(t *testing.T) {
	te := newFilterTestEnv(t)
	store := filterhost.NewStore(te.env.home, "gws")
	saveTestGrants(t, store, filterhost.Grant{ID: "a", ClientID: "c1"})
	ctx := context.Background()

	// --no-revoke deletes without asking the provider.
	if err := te.env.logout(ctx, cli.FilterRequest{Name: "gws", NoRevoke: true}); err != nil {
		t.Fatalf("logout --no-revoke: %v", err)
	}
	if grants, _, _ := store.Grants(); len(grants) != 0 || len(te.revoked) != 0 {
		t.Fatalf("grants %v, revoked %v", grants, te.revoked)
	}

	// A spec that does not load fails logout instead of skipping the
	// revocation.
	saveTestGrants(t, store, filterhost.Grant{ID: "a", ClientID: "c1"})
	specPath := filepath.Join(te.env.paths.UserFilterExtensionsDir, "gws", "spec.yaml")
	writeAppFile(t, specPath, "kind: [broken\n", 0o600)
	err := te.env.logout(ctx, cli.FilterRequest{Name: "gws"})
	if err == nil || !strings.Contains(err.Error(), "--no-revoke") || strings.Contains(err.Error(), "not installed") {
		t.Fatalf("logout with a broken spec = %v", err)
	}
	if _, err := store.Grant("a"); err != nil {
		t.Fatalf("a failed logout deleted the grant: %v", err)
	}
	if err := te.env.logout(ctx, cli.FilterRequest{Name: "gws", NoRevoke: true}); err != nil {
		t.Fatalf("logout --no-revoke with a broken spec: %v", err)
	}

	// A filter that is gone only has its grants deleted.
	saveTestGrants(t, store, filterhost.Grant{ID: "a", ClientID: "c1"})
	if err := os.RemoveAll(filepath.Dir(specPath)); err != nil {
		t.Fatal(err)
	}
	_, stderr := captureOutput(t, func() {
		if err := te.env.logout(ctx, cli.FilterRequest{Name: "gws"}); err != nil {
			t.Fatalf("logout of a removed filter: %v", err)
		}
	})
	if !strings.Contains(stderr, "Filter gws is not installed, so grant a is deleted without revoking it") || len(te.revoked) != 0 {
		t.Fatalf("stderr %q, revoked %v", stderr, te.revoked)
	}
	if grants, _, _ := store.Grants(); len(grants) != 0 {
		t.Fatalf("grants left: %v", grants)
	}

	if err := te.env.logout(ctx, cli.FilterRequest{Name: "../gws"}); err == nil || !strings.Contains(err.Error(), "filter name") {
		t.Fatalf("logout with an invalid name = %v", err)
	}
}

func TestFilterShowWarnsAboutGrantsThatDoNotLoad(t *testing.T) {
	te := newFilterTestEnv(t)
	store := filterhost.NewStore(te.env.home, "gws")
	writeAppFile(t, filepath.Join(store.GrantsDir(), "readonly.json"), `{"refreshToken":"rt-secret"`, 0o600)
	_, stderr := captureOutput(t, func() {
		if err := te.env.show(cli.FilterRequest{Name: "gws", Preset: "readonly"}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	if !strings.Contains(stderr, "Skipping a grant that does not load: grant readonly of filter gws: invalid JSON") || strings.Contains(stderr, "rt-secret") {
		t.Fatalf("stderr %q", stderr)
	}
	if !strings.Contains(te.out.String(), "grant: none") {
		t.Fatalf("show output = %q", te.out.String())
	}
}

func TestFilterLoginRejectsStaticAndUnknownFilters(t *testing.T) {
	te := newFilterTestEnv(t)
	if err := te.env.login(context.Background(), cli.FilterRequest{Name: "nope"}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("login(nope) = %v", err)
	}
	if err := te.env.show(cli.FilterRequest{Name: "gws", Preset: "readonly"}); err != nil {
		t.Fatalf("show: %v", err)
	}
}
