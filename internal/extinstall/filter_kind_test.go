// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"enclave/internal/filterhost"
	"enclave/internal/model"
	"enclave/internal/testutil"
)

// gwsFixtureFiles reads the filter engine's gws test fixture as repository
// files under filters/gws/.
func gwsFixtureFiles(t *testing.T) map[string]string {
	t.Helper()
	root := testutil.GWSFilterFixtureDir(t)
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path) // #nosec G304 -- test fixture.
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		files["filters/gws/"+filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("read gws fixture: %v", err)
	}
	return files
}

func filterRequest(op Op, names ...string) Request {
	return Request{Kind: model.KindFilter, Op: op, Source: "acme/kits", Names: names, Yes: true}
}

const filterHostCommandRel = "filters/gws/commands/host/gws-status"

func TestAddFilterExtension(t *testing.T) {
	files := gwsFixtureFiles(t)
	files["filters/gws/install.sh"] = "#!/bin/sh\necho never runs\n"
	files[filterHostCommandRel] = "#!/bin/sh\necho status\n"
	fetcher := newFakeFetcher(t, "a1b2c3d4", files)
	markExecutable(t, fetcher, filterHostCommandRel)
	env, out := testEnv(t, fetcher, "")

	results, err := Add(context.Background(), env, filterRequest(OpAdd))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Name != "gws" || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s, want gws installed", fmtResults(results))
	}
	if !slices.Equal(results[0].HostCommands, []string{"gws-status"}) {
		t.Errorf("HostCommands = %v, want [gws-status]", results[0].HostCommands)
	}
	if !slices.ContainsFunc(results[0].Warnings, func(w string) bool { return strings.Contains(w, "install.sh is ignored") }) {
		t.Errorf("warnings %v do not report install.sh as ignored", results[0].Warnings)
	}
	installed := filepath.Join(env.Paths.UserFilterExtensionsDir, "gws")
	origin, err := readOrigin(installed)
	if err != nil || origin == nil || origin.Kind != string(model.KindFilter) || origin.Commit != "a1b2c3d4" {
		t.Fatalf("readOrigin = %+v, %v", origin, err)
	}

	narration := out.String()
	for _, want := range []string{
		"governed hosts", "gmail.googleapis.com",
		"oauth2, injected as authorization: Bearer <credential>",
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/drive.metadata.readonly",
		"GOOGLE_WORKSPACE_CLI_TOKEN",
		"token endpoint    oauth2.googleapis.com (receives the refresh token",
		"auth endpoint     accounts.google.com (opened in your browser",
		"revoke endpoint   oauth2.googleapis.com (receives the refresh token at logout)",
		"confined domains  googleapis.com (names below that no filter governs are",
		"discovery (public)", "mail.send",
		"readonly (default)",
		"enclave filters login gws",
		"enclave --filters gws",
	} {
		if !strings.Contains(narration, want) {
			t.Errorf("narration does not contain %q:\n%s", want, narration)
		}
	}
	if strings.Contains(narration, "rebuilds the image") {
		t.Errorf("filter install promised a rebuild:\n%s", narration)
	}
	// The host command row stays the first one after the source.
	hostRow := strings.Index(narration, "host commands")
	hostsRow := strings.Index(narration, "governed hosts")
	if hostRow < 0 || hostRow > hostsRow {
		t.Errorf("host commands row is not ahead of the filter rows:\n%s", narration)
	}

	inventory, err := Inventory(env.Paths, model.KindFilter, nil, InventoryProvenance)
	if err != nil || inventory["gws"].Origin == nil {
		t.Fatalf("Inventory = %+v, %v", inventory, err)
	}

	// Without grants, there is nothing to log out of.
	removed, err := Remove(context.Background(), env, Request{Kind: model.KindFilter, Op: OpRemove, Names: []string{"gws"}, Yes: true})
	if err != nil || len(removed) != 1 || removed[0].Action != ActionRemoved {
		t.Fatalf("Remove = %s, %v", fmtResults(removed), err)
	}
	if strings.Contains(out.String(), "filters logout") {
		t.Errorf("remove pointed at logout without grants:\n%s", out.String())
	}

	// With a grant, remove points at logout.
	if _, err := Add(context.Background(), env, filterRequest(OpAdd)); err != nil {
		t.Fatalf("Add again: %v", err)
	}
	if err := filterhost.NewStore(env.Home, "gws").SaveGrant(filterhost.Grant{SchemaVersion: filterhost.GrantSchemaVersion, ID: "readonly", Filter: "gws",
		ClientID: "c", RefreshToken: "r", Scopes: []string{"s"}}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if _, err := Remove(context.Background(), env, Request{Kind: model.KindFilter, Op: OpRemove, Names: []string{"gws"}, Yes: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !strings.Contains(out.String(), "grants left unrevoked, delete them with: enclave filters logout gws --all") {
		t.Errorf("remove did not point at logout:\n%s", out.String())
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("filter directory still exists: %v", err)
	}
}

// Logout can revoke grants only while the filter is installed, so remove
// says so before it deletes the spec.
func TestRemoveFilterWithGrantsPointsAtLogoutFirst(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", gwsFixtureFiles(t))
	env, out := testEnv(t, fetcher, "n\n")
	if _, err := Add(context.Background(), env, filterRequest(OpAdd)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := filterhost.NewStore(env.Home, "gws").SaveGrant(filterhost.Grant{ID: "readonly", ClientID: "c", RefreshToken: "r", Scopes: []string{"s"}}); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(env.Paths.UserFilterExtensionsDir, "gws")

	out.Reset()
	req := Request{Kind: model.KindFilter, Op: OpRemove, Names: []string{"gws"}, Interactive: true}
	results, err := Remove(context.Background(), env, req)
	if err != nil || !HasFailure(results) {
		t.Fatalf("declined Remove = %s, %v", fmtResults(results), err)
	}
	prompt := "gws has stored grants. Revoke them first with: enclave filters logout gws --all\n" + bodyIndent + "Remove " + installed + "?"
	if !strings.Contains(out.String(), prompt) {
		t.Errorf("prompt does not point at logout first:\n%s", out.String())
	}
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("declined removal deleted the filter: %v", err)
	}

	results, err = Remove(context.Background(), env, Request{Kind: model.KindFilter, Op: OpRemove, Names: []string{"gws"}, Yes: true})
	if err != nil || len(results) != 1 || results[0].Action != ActionRemoved {
		t.Fatalf("Remove = %s, %v", fmtResults(results), err)
	}
	if want := "grants left unrevoked, delete them with: enclave filters logout gws --all"; !slices.Contains(results[0].Warnings, want) {
		t.Errorf("warnings = %v, want %q", results[0].Warnings, want)
	}
}

func TestAddFilterRejectsInvalidSpec(t *testing.T) {
	files := gwsFixtureFiles(t)
	files["filters/gws/spec.yaml"] = strings.Replace(files["filters/gws/spec.yaml"], "hosts:\n", "hosts: []\nunknownHosts:\n", 1)
	env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")

	results, err := Add(context.Background(), env, filterRequest(OpAdd))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionFailed || !strings.Contains(results[0].Error, "invalid spec.yaml") {
		t.Fatalf("results = %s, want an invalid spec failure", fmtResults(results))
	}
}

func TestAddFilterRejectsReservedEnv(t *testing.T) {
	files := gwsFixtureFiles(t)
	files["filters/gws/spec.yaml"] = strings.Replace(files["filters/gws/spec.yaml"],
		"env: [GOOGLE_WORKSPACE_CLI_TOKEN]", "env: [ENCLAVE_DNS_GATEWAY]", 1)
	env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")

	results, err := Add(context.Background(), env, filterRequest(OpAdd))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionFailed || !strings.Contains(results[0].Error, "reserved ENCLAVE_ prefix") {
		t.Fatalf("results = %s, want the reserved prefix failure", fmtResults(results))
	}
}

func TestAddFilterRejectsReservedStaticEnv(t *testing.T) {
	files := staticFilterFiles()
	files["filters/tok/spec.yaml"] = strings.Replace(files["filters/tok/spec.yaml"],
		"env: [TOK_HOST_TOKEN]", "env: [ENCLAVE_FOO]", 1)
	env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")

	results, err := Add(context.Background(), env, filterRequest(OpAdd))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionFailed || !strings.Contains(results[0].Error, "static: env: ENCLAVE_FOO uses the reserved ENCLAVE_ prefix") {
		t.Fatalf("results = %s, want the reserved prefix failure", fmtResults(results))
	}
}

func TestAddFilterAsFeatureNamesTheFilterVerb(t *testing.T) {
	env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", gwsFixtureFiles(t)), "")
	_, err := Add(context.Background(), env, addRequest())
	if err == nil || !strings.Contains(err.Error(), "`enclave filters add`") {
		t.Fatalf("Add as feature = %v, want a hint at enclave filters add", err)
	}
}

func TestUpdateFilterReportsCapabilityDiff(t *testing.T) {
	files := gwsFixtureFiles(t)
	env, out := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")
	if _, err := Add(context.Background(), env, filterRequest(OpAdd)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	files["filters/gws/spec.yaml"] = strings.Replace(files["filters/gws/spec.yaml"],
		"  - oauth2.googleapis.com\n", "  - oauth2.googleapis.com\n  - people.googleapis.com\n", 1)
	env.Fetcher = newFakeFetcher(t, "e5f6a7b8", files)
	out.Reset()

	results, err := Update(context.Background(), env, filterRequest(OpUpdate))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("results = %s, want gws updated", fmtResults(results))
	}
	if !strings.Contains(out.String(), "adds governed host people.googleapis.com") {
		t.Fatalf("update diff does not name the new host:\n%s", out.String())
	}
	const hostWarning = "the credential is now sent to governed host people.googleapis.com"
	if !slices.Contains(results[0].Warnings, hostWarning) {
		t.Fatalf("warnings = %q, want %q", results[0].Warnings, hostWarning)
	}
	if strings.Contains(out.String(), "rebuilds the image") {
		t.Fatalf("filter update promised a rebuild:\n%s", out.String())
	}
}

// An update that drops a confined domain reopens the ungoverned names below
// it, so it is a warning.
func TestUpdateFilterWarnsWhenItStopsConfining(t *testing.T) {
	files := gwsFixtureFiles(t)
	env, out := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")
	if _, err := Add(context.Background(), env, filterRequest(OpAdd)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.Contains(files["filters/gws/spec.yaml"], "confine: [googleapis.com]\n") {
		t.Fatal("fixture does not confine googleapis.com")
	}
	files["filters/gws/spec.yaml"] = strings.Replace(files["filters/gws/spec.yaml"], "confine: [googleapis.com]\n", "", 1)
	env.Fetcher = newFakeFetcher(t, "e5f6a7b8", files)
	out.Reset()

	results, err := Update(context.Background(), env, filterRequest(OpUpdate))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("results = %s, want gws updated", fmtResults(results))
	}
	const warning = "names below googleapis.com that no filter governs are no longer blocked"
	if !slices.Contains(results[0].Warnings, warning) {
		t.Fatalf("warnings = %q, want %q", results[0].Warnings, warning)
	}
	if !strings.Contains(out.String(), "confined domain googleapis.com") {
		t.Fatalf("update diff does not name the dropped domain:\n%s", out.String())
	}
}

// A malicious update could redirect the refresh token. The changed endpoint
// host is a warning ahead of the diff and in the JSON result, and leads the
// diff.
func TestUpdateFilterFlagsCredentialEndpointChanges(t *testing.T) {
	files := gwsFixtureFiles(t)
	env, out := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")
	if _, err := Add(context.Background(), env, filterRequest(OpAdd)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	spec := files["filters/gws/spec.yaml"]
	spec = strings.Replace(spec, "tokenUrl: https://oauth2.googleapis.com/token", "tokenUrl: https://tokens.evil.example/token", 1)
	spec = strings.Replace(spec, "revokeUrl: https://oauth2.googleapis.com/revoke", "revokeUrl: https://oauth2.googleapis.com/revoke2", 1)
	spec = strings.Replace(spec, "  - oauth2.googleapis.com\n", "  - oauth2.googleapis.com\n  - people.googleapis.com\n", 1)
	files["filters/gws/spec.yaml"] = spec
	env.Fetcher = newFakeFetcher(t, "e5f6a7b8", files)
	out.Reset()

	results, err := Update(context.Background(), env, filterRequest(OpUpdate))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("results = %s, want gws updated", fmtResults(results))
	}
	const moved = "token endpoint moves from oauth2.googleapis.com to tokens.evil.example (receives the refresh token and client secret)"
	if !slices.Contains(results[0].Warnings, moved) {
		t.Fatalf("warnings = %q, want %q", results[0].Warnings, moved)
	}
	if slices.ContainsFunc(results[0].Warnings, func(w string) bool { return strings.Contains(w, "revoke") }) {
		t.Fatalf("a path change on the same host is a warning: %q", results[0].Warnings)
	}
	narration := out.String()
	warning := strings.Index(narration, moved)
	movedLine := strings.Index(narration, "moves token endpoint from oauth2.googleapis.com to tokens.evil.example")
	hostLine := strings.Index(narration, "adds governed host people.googleapis.com")
	if warning < 0 || movedLine < 0 || hostLine < 0 || warning > movedLine || movedLine > hostLine {
		t.Fatalf("the endpoint change is not a warning and the first diff line:\n%s", narration)
	}
	if !strings.Contains(narration, `changes revoke endpoint from "https://oauth2.googleapis.com/revoke" to "https://oauth2.googleapis.com/revoke2"`) {
		t.Fatalf("the revoke URL change is missing from the diff:\n%s", narration)
	}
}

// An existing grant keeps covering a preset an update widens, so every change
// to an existing preset's grants is a warning. A new preset is a plain row.
func TestUpdateFilterFlagsPresetGrantChanges(t *testing.T) {
	files := gwsFixtureFiles(t)
	env, out := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")
	if _, err := Add(context.Background(), env, filterRequest(OpAdd)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	const send = "filters/gws/presets/send-eclipsesource.yaml"
	files[send] = strings.NewReplacer(
		`recipients: ["@eclipsesource.com"]`, `recipients: "*"`,
		"mail.sent: 20", "mail.sent: 100",
	).Replace(files[send])
	files["filters/gws/presets/extra.yaml"] = "allow:\n  mail.send: { recipients: \"*\" }\n"
	env.Fetcher = newFakeFetcher(t, "e5f6a7b8", files)
	out.Reset()

	results, err := Update(context.Background(), env, filterRequest(OpUpdate))
	if err != nil || len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("Update = %s, %v", fmtResults(results), err)
	}
	want := []string{
		`preset send-eclipsesource: changes mail.send recipients from ["@eclipsesource.com"] to "*"`,
		"preset send-eclipsesource: changes quota mail.sent from 20 to 100",
	}
	for _, w := range want {
		if !slices.Contains(results[0].Warnings, w) {
			t.Errorf("warnings = %q, want %q", results[0].Warnings, w)
		}
		if !strings.Contains(out.String(), "  "+w) {
			t.Errorf("update diff does not contain %q:\n%s", w, out.String())
		}
	}
	if slices.ContainsFunc(results[0].Warnings, func(w string) bool { return strings.Contains(w, "extra") }) {
		t.Errorf("a new preset is a warning: %q", results[0].Warnings)
	}
	if !strings.Contains(out.String(), "adds preset extra") {
		t.Errorf("update diff does not name the new preset:\n%s", out.String())
	}
	for _, unchanged := range []string{"preset readonly:", "preset edit-all:", "preset folder-edit:"} {
		if strings.Contains(out.String(), unchanged) {
			t.Errorf("update diff reports unchanged %q:\n%s", unchanged, out.String())
		}
	}
}

func TestDiffPresetGrants(t *testing.T) {
	before := map[string]presetGrants{
		"p": {
			Capabilities: map[string]map[string]string{
				"read":  {},
				"send":  {"to": `["@a.example"]`, "cc": `"*"`},
				"trash": {},
			},
			Quotas: map[string]int64{"sent": 20, "reads": 5},
		},
		"gone": {Capabilities: map[string]map[string]string{"read": {}}},
	}
	after := map[string]presetGrants{
		"p": {
			Capabilities: map[string]map[string]string{
				"read":  {},
				"send":  {"to": `"*"`, "bcc": `["x"]`},
				"files": {"resources": `{ids: ["a"]}`, "parents": `"*"`},
			},
			Quotas: map[string]int64{"sent": 100, "writes": 1},
		},
		"new": {Capabilities: map[string]map[string]string{"read": {}}},
	}
	got := diffPresetGrants(before, after)
	want := []string{
		`preset p: enables files with parents "*", resources {ids: ["a"]}`,
		`preset p: adds send bcc ["x"]`,
		`preset p: changes send to from ["@a.example"] to "*"`,
		`preset p: drops send cc "*"`,
		"preset p: disables trash",
		"preset p: changes quota sent from 20 to 100",
		"preset p: adds quota writes 1",
		"preset p: drops quota reads 5",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("diffPresetGrants =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if same := diffPresetGrants(before, before); len(same) != 0 {
		t.Fatalf("diffPresetGrants of equal grants = %q", same)
	}
}

func staticFilterFiles() map[string]string {
	files := map[string]string{}
	for rel, content := range testutil.StaticFilterFiles() {
		files["filters/tok/"+rel] = content
	}
	return files
}

// The static credential sources are part of the summary, and an update that
// reads another host file or env var is flagged.
func TestFilterStaticCredentialSourcesInSummaryAndUpdate(t *testing.T) {
	files := staticFilterFiles()
	env, out := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")
	if _, err := Add(context.Background(), env, filterRequest(OpAdd)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, want := range []string{
		"credential env", "TOK_HOST_TOKEN (host env or Enclave secrets",
		"credential file", "~/.config/tok/token.json (json:token) (host file",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary does not contain %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "token endpoint") {
		t.Errorf("a static filter shows an OAuth endpoint:\n%s", out.String())
	}

	files["filters/tok/spec.yaml"] = strings.NewReplacer(
		"env: [TOK_HOST_TOKEN]", "env: [TOK_HOST_TOKEN, AWS_SECRET_ACCESS_KEY]",
		`{ path: ~/.config/tok/token.json, parser: "json:token" }`, "{ path: ~/.ssh/id_ed25519 }",
	).Replace(files["filters/tok/spec.yaml"])
	env.Fetcher = newFakeFetcher(t, "e5f6a7b8", files)
	out.Reset()
	results, err := Update(context.Background(), env, filterRequest(OpUpdate))
	if err != nil || len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("Update = %s, %v", fmtResults(results), err)
	}
	for _, want := range []string{
		"the credential is now read from host env var AWS_SECRET_ACCESS_KEY",
		"the credential is now read from host file ~/.ssh/id_ed25519",
	} {
		if !slices.Contains(results[0].Warnings, want) {
			t.Errorf("warnings = %q, want %q", results[0].Warnings, want)
		}
	}
	for _, want := range []string{
		"adds credential env var AWS_SECRET_ACCESS_KEY",
		`changes credential file from "~/.config/tok/token.json (json:token)" to "~/.ssh/id_ed25519"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("update diff does not contain %q:\n%s", want, out.String())
		}
	}
}

// A local path source goes through the real git fetcher, as `enclave filters
// add ./repo` does.
func TestAddFilterFromLocalGitRepository(t *testing.T) {
	repo := fixtureRepo(t, true, gwsFixtureFiles(t))
	fetcher, err := NewGitFetcher(false)
	if err != nil {
		t.Skipf("git fetcher unavailable: %v", err)
	}
	env, _ := testEnv(t, fetcher, "")

	results, err := Add(context.Background(), env, Request{Kind: model.KindFilter, Op: OpAdd, Source: repo, Yes: true})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s, want gws installed", fmtResults(results))
	}
	installed := filepath.Join(env.Paths.UserFilterExtensionsDir, "gws")
	for _, rel := range []string{"spec.yaml", "presets/readonly.yaml", model.ExtensionSourceFilename} {
		if _, err := os.Stat(filepath.Join(installed, rel)); err != nil {
			t.Fatalf("%s missing after install: %v", rel, err)
		}
	}
}

// TestFilterCapabilitiesParticipateInRenderAndDiff is the completeness guard
// for filterCapabilities, which the generic sweep sees as one pointer field.
func TestFilterCapabilitiesParticipateInRenderAndDiff(t *testing.T) {
	cases := map[string]struct {
		set        func(f *filterCapabilities)
		wantRender string
		wantDiff   string
		before     func(f *filterCapabilities)
	}{
		"Hosts":          {func(f *filterCapabilities) { f.Hosts = []string{"api.example.com"} }, "api.example.com", "adds governed host api.example.com", nil},
		"Confine":        {func(f *filterCapabilities) { f.Confine = []string{"example.com"} }, "example.com (names below that no filter governs", "adds confined domain example.com", nil},
		"CredentialType": {func(f *filterCapabilities) { f.CredentialType = "static"; f.Credential = "static, injected as x" }, "static, injected as x", "adds credential", nil},
		"Credential":     {func(f *filterCapabilities) { f.Credential = "oauth2, injected as y" }, "oauth2, injected as y", "adds credential", nil},
		"Scopes":         {func(f *filterCapabilities) { f.Scopes = []string{"scope.a"} }, "scope.a", "adds oauth scope scope.a", nil},
		"Env":            {func(f *filterCapabilities) { f.Env = []string{"API_TOKEN"} }, "API_TOKEN", "adds placeholder env var API_TOKEN", nil},
		"AuthURL":        {func(f *filterCapabilities) { f.AuthURL = "https://login.example.com/auth" }, "auth endpoint", "adds auth endpoint login.example.com", nil},
		"TokenURL":       {func(f *filterCapabilities) { f.TokenURL = "https://token.example.com/t" }, "token.example.com (receives the refresh token", "adds token endpoint token.example.com", nil},
		"RevokeURL":      {func(f *filterCapabilities) { f.RevokeURL = "https://revoke.example.com/r" }, "revoke.example.com", "adds revoke endpoint revoke.example.com", nil},
		"StaticEnv":      {func(f *filterCapabilities) { f.StaticEnv = []string{"HOST_TOKEN"} }, "HOST_TOKEN (host env or Enclave secrets", "adds credential env var HOST_TOKEN", nil},
		"StaticFile":     {func(f *filterCapabilities) { f.StaticFile = "~/.token (json:token)" }, "~/.token (json:token) (host file", "adds credential file \"~/.token (json:token)\"", nil},
		"Capabilities":   {func(f *filterCapabilities) { f.Capabilities = []string{"mail.read"} }, "mail.read", "adds capability mail.read", nil},
		"Operations":     {func(f *filterCapabilities) { f.Operations = []string{"op.get -> mail.read"} }, "", "adds operation op.get -> mail.read", nil},
		"Presets":        {func(f *filterCapabilities) { f.Presets = []string{"readonly"} }, "readonly", "adds preset readonly", nil},
		"PresetGrants": {func(f *filterCapabilities) {
			f.PresetGrants = map[string]presetGrants{"readonly": {Capabilities: map[string]map[string]string{"mail.read": {}}}}
		}, "", "preset readonly: enables mail.read", func(f *filterCapabilities) {
			f.PresetGrants = map[string]presetGrants{"readonly": {}}
		}},
	}
	typ := reflect.TypeOf(filterCapabilities{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		tc, ok := cases[name]
		if !ok {
			t.Fatalf("filterCapabilities field %q has no case", name)
		}
		t.Run(name, func(t *testing.T) {
			after := capabilities{Filter: &filterCapabilities{}}
			tc.set(after.Filter)
			var buf bytes.Buffer
			after.render(&buf, Style{}, "")
			if tc.wantRender != "" && !strings.Contains(buf.String(), tc.wantRender) {
				t.Errorf("render does not contain %q:\n%s", tc.wantRender, buf.String())
			}
			before := capabilities{Filter: &filterCapabilities{}}
			if tc.before != nil {
				tc.before(before.Filter)
			}
			changes := strings.Join(diffCapabilities(before, after), "\n")
			if !strings.Contains(changes, tc.wantDiff) {
				t.Errorf("diff does not contain %q: %s", tc.wantDiff, changes)
			}
		})
	}
}
