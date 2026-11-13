// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/auth"
	"enclave/internal/filter"
	"enclave/internal/filter/spec"
	"enclave/internal/model"
	"enclave/internal/testutil"
)

func TestParseSelection(t *testing.T) {
	got, err := ParseSelection([]string{"gws:readonly", "+other", "-gone", " ", "third:p"})
	want := []Selection{{Name: "gws", Preset: "readonly"}, {Name: "other"}, {Name: "third", Preset: "p"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSelection = %v, %v", got, err)
	}
	if got, err := ParseSelection([]string{"none"}); err != nil || len(got) != 0 {
		t.Fatalf("ParseSelection(none) = %v, %v", got, err)
	}
	// -name removes the filter, whatever its preset and wherever it stands.
	for _, entries := range [][]string{{"gws", "-gws"}, {"-gws", "gws:readonly"}, {"+gws:edit", "-gws:readonly"}, {"none", "-gws"}} {
		if got, err := ParseSelection(entries); err != nil || len(got) != 0 {
			t.Errorf("ParseSelection(%v) = %v, %v, want nothing selected", entries, got, err)
		}
	}
	if got, err := ParseSelection([]string{"gws", "other", "-gws"}); err != nil || !reflect.DeepEqual(got, []Selection{{Name: "other"}}) {
		t.Fatalf("ParseSelection(gws,other,-gws) = %v, %v", got, err)
	}
	for _, entries := range [][]string{{"gws", "gws:x"}, {"gws", "-gws", "gws:x"}, {"none", "gws"}, {"Bad"}, {"gws:"}, {"-Bad"}} {
		if _, err := ParseSelection(entries); err == nil {
			t.Errorf("ParseSelection(%v) succeeded", entries)
		}
	}
}

func installStaticFilter(t *testing.T, paths model.Paths) {
	t.Helper()
	testutil.InstallStaticFilter(t, paths.UserFilterExtensionsDir)
}

func TestResolveStaticCredential(t *testing.T) {
	paths := testPaths(t)
	installStaticFilter(t, paths)
	home := t.TempDir()
	env := map[string]string{}
	var layers []auth.SecretsLayer
	var persisted map[string]string
	resolve := func() ([]SessionFilter, error) {
		return Resolve(ResolveRequest{Paths: paths, Home: home, Entries: []string{"tok"}, Secrets: auth.SecretSources{
			Getenv: func(name string) string { return env[name] }, Layers: layers, Persisted: persisted,
		}})
	}
	credentialValue := func(t *testing.T) string {
		t.Helper()
		filters, err := resolve()
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		bundle, err := filters[0].Bundle()
		if err != nil {
			t.Fatal(err)
		}
		cred, err := filter.ParseCredential(bundle.Credential)
		if err != nil || cred.Static == nil {
			t.Fatalf("credential = %v, %v", cred, err)
		}
		return cred.Static.Value
	}

	_, err := resolve()
	if err == nil || !strings.Contains(err.Error(), "filter tok: no static credential found (set TOK_HOST_TOKEN in the host environment or Enclave's secrets, or create ~/.config/tok/token.json)") {
		t.Fatalf("Resolve without a credential = %v", err)
	}

	// The env aliases resolve like credentials.sources. Lowest precedence
	// first: the env store, the secrets files, the host environment.
	persisted = map[string]string{"TOK_HOST_TOKEN": "from-env-store"}
	if got := credentialValue(t); got != "from-env-store" {
		t.Fatalf("credential = %q, want the env store value", got)
	}
	layers = []auth.SecretsLayer{
		{Path: "global.env", Values: map[string]string{"TOK_HOST_TOKEN": "from-global-secrets"}},
		{Path: "projects/x/tool.env", Values: map[string]string{"TOK_HOST_TOKEN": "from-project-secrets\n"}},
	}
	if got := credentialValue(t); got != "from-project-secrets" {
		t.Fatalf("credential = %q, want the highest secrets layer, trimmed", got)
	}
	env["TOK_HOST_TOKEN"] = "from-host-env"
	if got := credentialValue(t); got != "from-host-env" {
		t.Fatalf("credential = %q, want the host env value", got)
	}
	layers, persisted = nil, nil
	delete(env, "TOK_HOST_TOKEN")

	writeFile(t, filepath.Join(home, ".config", "tok", "token.json"), `{"token":"from-file-secret"}`)
	filters, err := resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	bundle, err := filters[0].Bundle()
	if err != nil {
		t.Fatal(err)
	}
	cred, err := filter.ParseCredential(bundle.Credential)
	if err != nil || cred.Static == nil || cred.Static.Value != "from-file-secret" {
		t.Fatalf("credential = %v, %v", cred, err)
	}
	if line := filters[0].StartupLine(); line != "filter tok: preset default, static credential" {
		t.Errorf("startup line = %q", line)
	}

	// env-first: the env alias wins over the file.
	env["TOK_HOST_TOKEN"] = "from-env-secret"
	filters, err = resolve()
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ = filters[0].Bundle()
	if cred, _ := filter.ParseCredential(bundle.Credential); cred.Static.Value != "from-env-secret" {
		t.Fatalf("env-first credential = %v", cred)
	}
	for _, text := range []string{fmt.Sprintf("%v", filters[0]), fmt.Sprintf("%+v", filters[0]), fmt.Sprintf("%#v", filters[0])} {
		if strings.Contains(text, "from-env-secret") {
			t.Fatalf("formatted session filter leaks the credential: %s", text)
		}
	}
	summary, err := filters[0].SummaryJSON()
	if err != nil || strings.Contains(string(summary), "from-env-secret") || strings.Contains(string(summary), filters[0].Placeholder) {
		t.Fatalf("summary = %s, %v", summary, err)
	}
	if env := filters[0].Env(); env["TOK_TOKEN"] != filters[0].Placeholder || len(env) != 1 {
		t.Fatalf("env = %v", env)
	}
}

func TestResolvePlaceholderPerSession(t *testing.T) {
	paths := testPaths(t)
	installStaticFilter(t, paths)
	secrets := auth.SecretSources{Getenv: func(string) string { return "secret-value" }}
	first, err := Resolve(ResolveRequest{Paths: paths, Home: t.TempDir(), Entries: []string{"tok"}, Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(ResolveRequest{Paths: paths, Home: t.TempDir(), Entries: []string{"tok"}, Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Placeholder == second[0].Placeholder || !strings.HasPrefix(first[0].Placeholder, model.SecretPlaceholderMarker) {
		t.Fatalf("placeholders %q and %q", first[0].Placeholder, second[0].Placeholder)
	}
	bundle, _ := first[0].Bundle()
	var pol filter.Policy
	if err := json.Unmarshal(bundle.Policy, &pol); err != nil || pol.Placeholder != first[0].Placeholder {
		t.Fatalf("policy placeholder = %q, %v", pol.Placeholder, err)
	}
	if !reflect.DeepEqual(bundle.Hosts, pol.Hosts) || len(bundle.Hosts) == 0 {
		t.Fatalf("bundle hosts = %v, want the policy hosts %v", bundle.Hosts, pol.Hosts)
	}
	if strings.Contains(string(bundle.Policy), "secret-value") {
		t.Fatal("the policy carries the credential")
	}
}

func TestResolveReportsGrantsThatDoNotLoad(t *testing.T) {
	paths := testPaths(t)
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	home := t.TempDir()
	writeFile(t, filepath.Join(NewStore(home, "gws").GrantsDir(), "readonly.json"), `{"refreshToken":"rt-secret"`)
	var problems []string
	_, err := Resolve(ResolveRequest{Paths: paths, Home: home, Entries: []string{"gws"},
		OnGrantProblem: func(problem string) { problems = append(problems, problem) }})
	if err == nil || !strings.Contains(err.Error(), "no grant covers preset readonly") {
		t.Fatalf("Resolve = %v, want no grant", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "grant readonly of filter gws: invalid JSON") || strings.Contains(problems[0], "rt-secret") {
		t.Fatalf("problems = %q", problems)
	}
}

func TestHostSetsOverlap(t *testing.T) {
	for _, tc := range []struct {
		a, b    string
		release bool
		want    bool
	}{
		{"gmail.googleapis.com", "gmail.googleapis.com", false, true},
		{"gmail.googleapis.com", "www.googleapis.com", false, false},
		{"api.example.com", "*.example.com", false, true},
		{"example.com", "*.example.com", false, true},
		{"example.com", "api.example.com", false, true},
		{"*.example.com", "*.a.example.com", false, true},
		{"*.a.example.com", "*.b.example.com", false, false},
		{"api.example.com", "xapi.example.com", false, false},
		{"gmail.googleapis.com", "googleapis.com", true, true},
		{"googleapis.com", "gmail.googleapis.com", true, true},
		{"*.googleapis.com", "googleapis.com", true, true},
		{"*.googleapis.com", "gmail.googleapis.com", true, true},
		{"api.example.com", "example.org", true, false},
	} {
		other := filterHostSet(tc.b)
		if tc.release {
			other = releaseHostSet(tc.b, false)
		}
		if got := setsOverlap(filterHostSet(tc.a), other); got != tc.want {
			t.Errorf("overlap(%q, %q, release=%v) = %v, want %v", tc.a, tc.b, tc.release, got, tc.want)
		}
	}
	if setsOverlap(filterHostSet("gmail.googleapis.com"), releaseHostSet("googleapis.com", true)) {
		t.Error("an exact release host covers its subdomains")
	}
	if !setsOverlap(filterHostSet("gmail.googleapis.com"), releaseHostSet("x.gmail.googleapis.com", true)) {
		t.Error("an exact release host below a governed host does not collide")
	}
	if !setsOverlap(filterHostSet("*.example.com"), releaseHostSet("example.com", true)) {
		t.Error("an exact release host on the apex of a wildcard does not collide")
	}
}

func TestCheckCollisions(t *testing.T) {
	mk := func(name string, hosts []string, env []string) SessionFilter {
		return SessionFilter{Filter: &spec.Filter{Name: name, Hosts: hosts, Credential: spec.Credential{Env: env}}}
	}
	a := mk("a", []string{"api.example.com"}, []string{"A_TOKEN"})
	b := mk("b", []string{"*.example.com"}, []string{"B_TOKEN"})
	c := mk("c", []string{"other.example.org"}, []string{"A_TOKEN"})
	if err := CheckCollisions([]SessionFilter{a, b}, SessionClaims{}); err == nil || !strings.Contains(err.Error(), "filters a and b both govern api.example.com and *.example.com") {
		t.Fatalf("host collision = %v", err)
	}
	if err := CheckCollisions([]SessionFilter{a, c}, SessionClaims{}); err == nil || !strings.Contains(err.Error(), "which filter a also sets") {
		t.Fatalf("env collision = %v", err)
	}
	if err := CheckCollisions([]SessionFilter{a}, SessionClaims{Env: map[string]string{"A_TOKEN": "feature x"}}); err == nil {
		t.Fatal("claimed env passed")
	}
	if err := CheckCollisions([]SessionFilter{a, mk("d", []string{"x.example.org"}, []string{"D"})}, SessionClaims{
		ReleaseHosts: map[string][]string{"tool t": {"github.com"}},
		Env:          map[string]string{"GH_TOKEN": "tool t"},
	}); err != nil {
		t.Fatalf("no collision = %v", err)
	}
}
