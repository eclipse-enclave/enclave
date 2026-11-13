// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"enclave/internal/auth"
	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/extinstall"
	"enclave/internal/filter"
	"enclave/internal/filterhost"
	"enclave/internal/model"
	"enclave/internal/network"
	"enclave/internal/runtime"
	"enclave/internal/testutil"
)

const sessionFilterRefreshToken = "rt-session-filter-secret"

func sessionFilterTestInput(t *testing.T) sessionFilterInput {
	t.Helper()
	root := t.TempDir()
	paths := model.Paths{UserFilterExtensionsDir: filepath.Join(root, "extensions", "filters")}
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	home := filepath.Join(root, "home")
	if err := filterhost.NewStore(home, "gws").SaveGrant(filterhost.Grant{
		ID: "readonly", ClientID: "c", RefreshToken: sessionFilterRefreshToken,
		Scopes: []string{"https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/drive.readonly"},
	}); err != nil {
		t.Fatal(err)
	}
	return sessionFilterInput{
		paths:   paths,
		home:    home,
		opts:    model.Options{RunOptions: model.RunOptions{Backend: backend.NameDocker, Filters: []string{"gws"}}},
		profile: model.Profile{Name: "tool"},
		secrets: auth.SecretSources{Getenv: func(string) string { return "" }},
	}
}

func TestResolveSessionFilters(t *testing.T) {
	in := sessionFilterTestInput(t)
	filters, err := resolveSessionFilters(in)
	if err != nil {
		t.Fatalf("resolveSessionFilters: %v", err)
	}
	if len(filters) != 1 || filters[0].Label() != "gws:readonly" || filters[0].Grant != "readonly" {
		t.Fatalf("filters = %v", filters)
	}
	if line := filters[0].StartupLine(); line != "filter gws: preset readonly, grant readonly" {
		t.Errorf("startup line = %q", line)
	}

	in.opts.Filters = nil
	if filters, err := resolveSessionFilters(in); err != nil || filters != nil {
		t.Fatalf("no selection = %v, %v", filters, err)
	}
	in.opts.Filters = []string{"none"}
	if filters, err := resolveSessionFilters(in); err != nil || filters != nil {
		t.Fatalf("none = %v, %v", filters, err)
	}
}

func TestResolveSessionFiltersWarnsAboutGrantsThatDoNotLoad(t *testing.T) {
	in := sessionFilterTestInput(t)
	writeAppFile(t, filepath.Join(filterhost.NewStore(in.home, "gws").GrantsDir(), "mail.json"), `{"refreshToken":"rt-secret"`, 0o600)
	_, stderr := captureOutput(t, func() {
		if _, err := resolveSessionFilters(in); err != nil {
			t.Fatalf("resolveSessionFilters: %v", err)
		}
	})
	if !strings.Contains(stderr, "Skipping a grant that does not load: grant mail of filter gws: invalid JSON") || strings.Contains(stderr, "rt-secret") {
		t.Fatalf("stderr %q", stderr)
	}
}

func TestResolveSessionFiltersFailures(t *testing.T) {
	secondFilter := func(t *testing.T, in sessionFilterInput) {
		dir := testutil.InstallGWSFilter(t, t.TempDir())
		data, err := os.ReadFile(filepath.Join(dir, "spec.yaml")) // #nosec G304 -- test fixture.
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Replace(string(data), "name: gws", "name: gws2", 1)
		text = strings.Replace(text, "env: [GOOGLE_WORKSPACE_CLI_TOKEN]", "env: [OTHER_TOKEN]", 1)
		target := filepath.Join(in.paths.UserFilterExtensionsDir, "gws2")
		if err := os.Rename(dir, target); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "spec.yaml"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := filterhost.NewStore(in.home, "gws2").SaveGrant(filterhost.Grant{
			ID: "all", ClientID: "c", RefreshToken: "r",
			Scopes: []string{"https://www.googleapis.com/auth/gmail.modify", "https://www.googleapis.com/auth/drive"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, in *sessionFilterInput)
		want string
	}{
		{"missing grant", func(_ *testing.T, in *sessionFilterInput) { in.opts.Filters = []string{"gws:send-eclipsesource"} },
			"filter gws: no grant covers preset send-eclipsesource (run: enclave filters login gws --preset send-eclipsesource)"},
		{"unknown filter", func(_ *testing.T, in *sessionFilterInput) { in.opts.Filters = []string{"nope"} },
			"unknown filter nope (installed: gws)"},
		{"unknown preset", func(_ *testing.T, in *sessionFilterInput) { in.opts.Filters = []string{"gws:nope"} },
			`filter gws has no preset "nope" (available: edit-all, folder-edit, readonly, send-eclipsesource)`},
		{"selected twice", func(_ *testing.T, in *sessionFilterInput) { in.opts.Filters = []string{"gws", "+gws:edit-all"} },
			"filter gws is selected twice"},
		{"host collision", func(t *testing.T, in *sessionFilterInput) {
			secondFilter(t, *in)
			in.opts.Filters = []string{"gws", "gws2"}
		}, "filters gws and gws2 both govern gmail.googleapis.com"},
		{"release host collision", func(_ *testing.T, in *sessionFilterInput) {
			in.features = []model.Extension{{Name: "gcloud", Secrets: map[string]model.SecretConfig{"tok": {
				EnvVars: []string{"GCLOUD_TOKEN"},
				Release: &model.SecretReleaseConfig{HTTP: &model.HTTPSecretReleaseConfig{Hosts: []string{"googleapis.com"}, Header: "authorization"}},
			}}}}
		}, "where feature gcloud releases a credential"},
		{"pass_env collision", func(_ *testing.T, in *sessionFilterInput) {
			in.opts.PassEnv = []string{"GOOGLE_WORKSPACE_CLI_TOKEN"}
		}, "filter gws sets env GOOGLE_WORKSPACE_CLI_TOKEN, which pass_env also sets"},
		{"tool credential env collision", func(_ *testing.T, in *sessionFilterInput) {
			in.profile.Secrets = map[string]model.SecretConfig{"gws": {EnvVars: []string{"GOOGLE_WORKSPACE_CLI_TOKEN"}}}
		}, "which tool tool also sets"},
		{"feature env collision", func(_ *testing.T, in *sessionFilterInput) {
			in.features = []model.Extension{{Name: "gws-cli", EnvVariables: map[string]string{"GOOGLE_WORKSPACE_CLI_TOKEN": "x"}}}
		}, "which feature gws-cli also sets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := sessionFilterTestInput(t)
			tc.edit(t, &in)
			_, err := resolveSessionFilters(in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("resolveSessionFilters = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), sessionFilterRefreshToken) {
				t.Fatalf("error leaks the refresh token: %v", err)
			}
		})
	}
}

func TestPrepareSessionFiltersRecordsProvenance(t *testing.T) {
	in := sessionFilterTestInput(t)
	input := configTestInput(t, in.paths, config.DefaultOptions(), nil)
	input.Action = "run"
	host, err := input.Ctx.Host()
	if err != nil {
		t.Fatal(err)
	}
	if err := filterhost.NewStore(host.Home, "gws").SaveGrant(filterhost.Grant{
		ID: "readonly", ClientID: "c", RefreshToken: sessionFilterRefreshToken,
		Scopes: []string{"https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/drive.readonly"},
	}); err != nil {
		t.Fatal(err)
	}
	opts := in.opts
	provenance := map[string]any{}
	out := captureStdout(t, func() {
		filters, err := prepareSessionFilters(input, opts, model.Project{Hash: "p1"}, in.profile, nil, provenance)
		if err != nil || len(filters) != 1 {
			t.Errorf("prepareSessionFilters = %v, %v", filters, err)
		}
	})
	if !strings.Contains(out, "filter gws: preset readonly, grant readonly") {
		t.Errorf("startup output = %q", out)
	}
	labels, _ := provenance["filters"].([]string)
	if !slices.Equal(labels, []string{"gws:readonly"}) {
		t.Fatalf("provenance filters = %#v", provenance["filters"])
	}
	encoded, _ := json.Marshal(provenance)
	if strings.Contains(string(encoded), sessionFilterRefreshToken) || strings.Contains(string(encoded), "ENCLAVE_SECRET_") {
		t.Fatalf("provenance carries secret material: %s", encoded)
	}
}

func TestNetworkPreviewShowsFilterHosts(t *testing.T) {
	input := networkPreviewTestInput(t)
	input.Ctx.Paths.UserFilterExtensionsDir = filepath.Join(t.TempDir(), "filters")
	testutil.InstallGWSFilter(t, input.Ctx.Paths.UserFilterExtensionsDir)
	input.Options.Filters = []string{"gws:edit-all"}

	_, out, doc := runNetworkPrintJSON(t, input)
	filters, _ := doc["filters"].([]any)
	if len(filters) != 1 {
		t.Fatalf("filters = %v\n%s", doc["filters"], out)
	}
	entry, _ := filters[0].(map[string]any)
	if entry["name"] != "gws" || entry["preset"] != "edit-all" || !slices.Contains(entry["hosts"].([]any), any("docs.googleapis.com")) ||
		!reflect.DeepEqual(entry["confine"], []any{"googleapis.com"}) {
		t.Fatalf("filter entry = %v", entry)
	}
	if !slices.Contains(doc["domains"].([]any), any("docs.googleapis.com")) {
		t.Fatalf("domains %v lack the governed host", doc["domains"])
	}

	input.NetworkPrintJSON = false
	text := captureStdout(t, func() { runNetworkPrint(input) })
	if !strings.Contains(text, "#   filter gws:edit-all: gmail.googleapis.com") || !strings.Contains(text, "(confines googleapis.com)") ||
		!strings.Contains(text, "server=/docs.googleapis.com/") || !strings.Contains(text, "address=/googleapis.com/") {
		t.Fatalf("network print:\n%s", text)
	}
	text = captureStdout(t, func() { runNetworkDiff(input) })
	if !strings.Contains(text, "Filter hosts added:") || !strings.Contains(text, "+ filter gws:edit-all") {
		t.Fatalf("network diff:\n%s", text)
	}

	input.Options.Filters = []string{"nope"}
	if code, _, doc := runNetworkPrintJSON(t, input); code != 1 || !strings.Contains(doc["error"].(string), "unknown filter nope") {
		t.Fatalf("network print --json with an unknown filter = %d %v", code, doc["error"])
	}
}

func TestFilterListMarksSelectedFilters(t *testing.T) {
	root := t.TempDir()
	paths := model.Paths{UserFilterExtensionsDir: filepath.Join(root, "filters")}
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	testutil.InstallStaticFilter(t, paths.UserFilterExtensionsDir)
	input := configTestInput(t, paths, model.Options{RunOptions: model.RunOptions{Filters: []string{"gws:readonly"}}}, nil)
	input.ExtRequest = &extinstall.Request{Kind: model.KindFilter, JSON: true}

	out := captureStdout(t, func() {
		if code := runFilterExtensions(input); code != 0 {
			t.Errorf("exit code = %d", code)
		}
	})
	var doc struct {
		Kind       string `json:"kind"`
		Extensions []struct {
			Name           string   `json:"name"`
			Enabled        bool     `json:"enabled"`
			Source         string   `json:"source"`
			GovernedHosts  []string `json:"governedHosts"`
			Confined       []string `json:"confinedDomains"`
			CredentialType string   `json:"credentialType"`
			DefaultPreset  string   `json:"defaultPreset"`
			Presets        []string `json:"presets"`
			// Kept raw so a missing or extra key fails the comparison.
			CredentialEndpoints json.RawMessage `json:"credentialEndpoints"`
			CredentialSources   json.RawMessage `json:"credentialSources"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if doc.Kind != "filter" || len(doc.Extensions) != 2 {
		t.Fatalf("envelope = %+v", doc)
	}
	ext := doc.Extensions[0]
	if ext.Name != "gws" || !ext.Enabled || ext.Source != "user" || ext.CredentialType != "oauth2" || ext.DefaultPreset != "readonly" ||
		!slices.Contains(ext.GovernedHosts, "gmail.googleapis.com") || !slices.Equal(ext.Confined, []string{"googleapis.com"}) || !slices.Contains(ext.Presets, "edit-all") {
		t.Fatalf("entry = %+v", ext)
	}
	compact := func(raw json.RawMessage) string {
		if raw == nil {
			return ""
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	wantEndpoints := `{"authUrl":"https://accounts.google.com/o/oauth2/auth","tokenUrl":"https://oauth2.googleapis.com/token","revokeUrl":"https://oauth2.googleapis.com/revoke"}`
	if compact(ext.CredentialEndpoints) != wantEndpoints || ext.CredentialSources != nil {
		t.Fatalf("gws credential fields = %s, %s, want %s and none", ext.CredentialEndpoints, ext.CredentialSources, wantEndpoints)
	}
	static := doc.Extensions[1]
	wantSources := `{"env":["TOK_HOST_TOKEN"],"file":{"path":"~/.config/tok/token.json","parser":"json:token"},"priority":"env-first"}`
	if static.Name != "tok" || static.Enabled || static.CredentialType != "static" || compact(static.CredentialSources) != wantSources || static.CredentialEndpoints != nil || static.Confined != nil {
		t.Fatalf("tok entry = %+v, want credentialSources %s", static, wantSources)
	}

	input.ExtRequest = nil
	input.Options.Filters = nil
	text := captureStdout(t, func() { runFilterExtensions(input) })
	if !strings.Contains(text, "  gws") || strings.Contains(text, "✓") {
		t.Fatalf("listing = %q", text)
	}
}

func TestConfigJSONReportsResolvedFilters(t *testing.T) {
	root := t.TempDir()
	paths := model.Paths{UserFilterExtensionsDir: filepath.Join(root, "filters")}
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	opts := config.DefaultOptions()
	opts.Filters = []string{"+gws"}
	doc := runConfigJSON(t, configTestInput(t, paths, opts, nil))
	option := configJSONOptionField(t, doc, "filters")
	if value, _ := option["value"].([]any); len(value) != 1 || value[0] != "gws:readonly" {
		t.Fatalf("filters option = %v", option)
	}
	if directives, _ := option["directives"].([]any); len(directives) != 1 || directives[0] != "+gws" {
		t.Fatalf("filters directives = %v", option["directives"])
	}
}

// A static filter credential resolves from the same host sources as
// credentials.sources: Enclave's secrets files and the env store besides the
// host environment and the file source.
func TestStaticFilterCredentialFromEnclaveSecrets(t *testing.T) {
	in := sessionFilterTestInput(t)
	testutil.InstallStaticFilter(t, in.paths.UserFilterExtensionsDir)
	project := model.Project{Hash: "abc123abc123"}
	in.profile = model.Profile{Name: "claude"}
	in.opts.Filters = []string{"tok"}
	in.opts.SecretsScope = model.SecretsScopeBoth
	in.opts.Persist = true
	writeAppFile(t, config.HostSecretsProjectFile(in.home, project.Hash, "claude"), "TOK_HOST_TOKEN=from-project-secrets\n", 0o600)
	storeDir, err := hoststore.DirFor(in.home, runtime.EnvStoreKey("claude", project.Hash), backend.StoreKindEnv)
	if err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, filepath.Join(storeDir, "env"), "TOK_HOST_TOKEN=from-env-store\nOTHER=stored\n", 0o600)

	credential := func(t *testing.T, opts model.Options) (string, error) {
		t.Helper()
		in.opts = opts
		in.secrets = sessionSecretSources(model.Host{Home: in.home}, project, in.profile, opts)
		in.secrets.Getenv = func(string) string { return "" }
		filters, err := resolveSessionFilters(in)
		if err != nil {
			return "", err
		}
		bundle, err := filters[0].Bundle()
		if err != nil {
			t.Fatal(err)
		}
		cred, err := filter.ParseCredential(bundle.Credential)
		if err != nil {
			t.Fatal(err)
		}
		return cred.Static.Value, nil
	}
	if got, err := credential(t, in.opts); err != nil || got != "from-project-secrets" {
		t.Fatalf("credential = %q, %v, want the project secrets value", got, err)
	}

	writeAppFile(t, config.HostSecretsProjectFile(in.home, project.Hash, "claude"), "UNRELATED=1\n", 0o600)
	if got, err := credential(t, in.opts); err != nil || got != "from-env-store" {
		t.Fatalf("credential = %q, %v, want the env store value", got, err)
	}
	for name, edit := range map[string]func(*model.Options){
		"ephemeral":  func(o *model.Options) { o.Persist = false },
		"reset auth": func(o *model.Options) { o.ResetAuth = true },
	} {
		opts := in.opts
		edit(&opts)
		if _, err := credential(t, opts); err == nil || !strings.Contains(err.Error(), "no static credential found") {
			t.Errorf("%s: the env store was read: %v", name, err)
		}
		in.opts.Persist, in.opts.ResetAuth = true, false
	}
}

// Run option validation names filters that cannot be enforced first, in one
// error, before the qemu coercion warns and before the gateway checks ask
// for --allow-all-network.
func TestValidateOptionsReportsFilterConflictsFirst(t *testing.T) {
	t.Setenv(model.EnvAllowRoot, "")
	restricted := func() (string, error) { return model.NetworkModeRestricted, nil }
	unrestricted := func() (string, error) { return model.NetworkModeUnrestricted, nil }
	for _, tc := range []struct {
		name   string
		edit   func(*model.Options, *model.OptionSources)
		policy func() (string, error)
		want   string
	}{
		{"qemu", func(o *model.Options, _ *model.OptionSources) { o.Backend = backend.NameQEMU }, restricted,
			"request filters need the docker or podman backend (the qemu backend cannot enforce them)"},
		{"disable gateway", func(o *model.Options, s *model.OptionSources) {
			o.DisableGateway, s.DisableGateway = true, model.SourceCLI
		}, restricted, "request filters need the network gateway in restricted mode: remove --disable-gateway"},
		{"both flags", func(o *model.Options, s *model.OptionSources) {
			o.DisableGateway, s.DisableGateway = true, model.SourceCLI
			o.AllowAllNetwork, s.AllowAllNetwork = true, model.SourceCLI
		}, restricted, "request filters need the network gateway in restricted mode: remove --disable-gateway and --allow-all-network"},
		{"config keys", func(o *model.Options, s *model.OptionSources) {
			o.DisableGateway, s.DisableGateway = true, model.SourceProject
			o.AllowAllNetwork, s.AllowAllNetwork = true, model.SourceGlobal
		}, restricted, "request filters need the network gateway in restricted mode: remove disable_gateway in config and allow_all_network in config"},
		{"unrestricted policy", func(*model.Options, *model.OptionSources) {}, unrestricted,
			"request filters need the network gateway in restricted mode: set the network policy mode to restricted (see `enclave network status`)"},
		{"policy error", func(*model.Options, *model.OptionSources) {}, func() (string, error) { return "", errors.New("bad policy") },
			"resolve network mode for request filters: bad policy"},
		{"admin", func(o *model.Options, _ *model.OptionSources) { o.Admin = true }, restricted,
			"request filters need a session without root access: remove --admin"},
		{"allow root", func(o *model.Options, s *model.OptionSources) {
			o.AllowRoot, s.AllowRoot = true, model.SourceCLI
		}, restricted, "request filters need a session without root access: remove --allow-root"},
		{"runtime uid remap", func(o *model.Options, s *model.OptionSources) {
			o.RuntimeUIDRemap, s.RuntimeUIDRemap = true, model.SourceCLI
		}, restricted, "request filters need a session without root access: remove --runtime-uid-remap"},
		{"everything", func(o *model.Options, s *model.OptionSources) {
			o.AllowAllNetwork, s.AllowAllNetwork = true, model.SourceCLI
			o.Admin = true
			o.AllowRoot, s.AllowRoot = true, model.SourceCLI
		}, unrestricted, "request filters need the network gateway in restricted mode and a session without root access: " +
			"remove --allow-all-network, --admin, and --allow-root and set the network policy mode to restricted (see `enclave network status`)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := model.Options{
				RunOptions:   model.RunOptions{Backend: backend.NameDocker, Filters: []string{"gws"}},
				BuildOptions: model.BuildOptions{ImageName: "test", Features: []string{}},
			}
			sources := model.DefaultOptionSources()
			tc.edit(&opts, &sources)
			_, _, warnings, err := ValidateOptions(opts, sources, ValidationContext{Action: "run", PolicyNetworkMode: tc.policy})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("ValidateOptions error = %v, want %q", err, tc.want)
			}
			for _, warning := range warnings {
				if strings.Contains(warning, "qemu") {
					t.Fatalf("warned before the filter error: %q", warning)
				}
			}
		})
	}
	// Without filters, the options keep their usual checks.
	opts := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameDocker, DisableGateway: true, Filters: []string{"none"}},
		BuildOptions: model.BuildOptions{ImageName: "test", Features: []string{}},
	}
	if _, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run", PolicyNetworkMode: restricted}); err == nil ||
		!strings.Contains(err.Error(), "requires unrestricted network mode") {
		t.Fatalf("ValidateOptions error = %v", err)
	}
	// Removed filters select nothing, so the options do not conflict.
	opts.Filters, opts.Admin, opts.DisableGateway = []string{"gws", "-gws"}, true, false
	if _, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run", PolicyNetworkMode: unrestricted}); err != nil {
		t.Fatalf("ValidateOptions with gws removed = %v", err)
	}
}

func TestCheckFilterRunOptionsBackend(t *testing.T) {
	opts := model.Options{RunOptions: model.RunOptions{Backend: backend.NameDocker, Filters: []string{"gws"}}}
	noFilters := func() (backend.Capabilities, error) {
		return backend.Capabilities{RestrictedNetwork: true, SecretHTTPRelease: true}, nil
	}
	if err := checkFilterRunOptions(opts, model.DefaultOptionSources(), noFilters, nil); err == nil ||
		err.Error() != "request filters need the docker or podman backend (the docker backend cannot enforce them)" {
		t.Fatalf("checkFilterRunOptions = %v", err)
	}
	opts.Backend = "lxc"
	if err := checkFilterRunOptions(opts, model.DefaultOptionSources(), func() (backend.Capabilities, error) { return backendCapabilities(opts) }, nil); err == nil ||
		!strings.Contains(err.Error(), `unsupported backend "lxc"`) {
		t.Fatalf("checkFilterRunOptions = %v", err)
	}
}

// ENCLAVE_ALLOW_ROOT folds into --allow-root before validation, so the error
// names both origins when both are set.
func TestCheckFilterRunOptionsNamesAllowRootEnv(t *testing.T) {
	t.Setenv(model.EnvAllowRoot, "1")
	opts := model.Options{RunOptions: model.RunOptions{Backend: backend.NameDocker, Filters: []string{"gws"}, AllowRoot: true}}
	sources := model.DefaultOptionSources()
	capabilities := func() (backend.Capabilities, error) { return backendCapabilities(opts) }
	want := "request filters need a session without root access: remove ENCLAVE_ALLOW_ROOT in the environment"
	if err := checkFilterRunOptions(opts, sources, capabilities, nil); err == nil || err.Error() != want {
		t.Fatalf("checkFilterRunOptions = %v, want %q", err, want)
	}
	sources.AllowRoot = model.SourceCLI
	want = "request filters need a session without root access: remove --allow-root and ENCLAVE_ALLOW_ROOT in the environment"
	if err := checkFilterRunOptions(opts, sources, capabilities, nil); err == nil || err.Error() != want {
		t.Fatalf("checkFilterRunOptions = %v, want %q", err, want)
	}
}

// A policy file in unrestricted mode fails every preview of a run with
// filters, as the run itself would fail.
func TestNetworkPreviewRejectsFiltersInUnrestrictedPolicy(t *testing.T) {
	input := networkPreviewTestInput(t)
	input.Ctx.Paths.UserFilterExtensionsDir = filepath.Join(t.TempDir(), "filters")
	testutil.InstallGWSFilter(t, input.Ctx.Paths.UserFilterExtensionsDir)
	input.Options.Filters = []string{"gws"}
	host, err := input.Ctx.Host()
	if err != nil {
		t.Fatal(err)
	}
	if err := network.SavePolicy(network.GlobalPolicyPath(host.Home), network.Policy{Mode: model.NetworkModeUnrestricted}); err != nil {
		t.Fatal(err)
	}

	code, out, doc := runNetworkPrintJSON(t, input)
	want := "request filters need the network gateway in restricted mode: set the network policy mode to restricted (see `enclave network status`)"
	if code != 1 || doc["error"] != want || doc["mode"] != nil {
		t.Fatalf("network print --json = %d\n%s\nwant the error %q", code, out, want)
	}
	if filters, _ := doc["filters"].([]any); len(filters) != 0 {
		t.Fatalf("filters = %v", doc["filters"])
	}

	var status string
	stdout, stderr := captureOutput(t, func() { code = runNetworkStatus(input) })
	status = stdout + stderr
	if strings.Contains(status, "Filter hosts") || !strings.Contains(status, "Cannot preview the session policy") {
		t.Fatalf("network status = %d\n%s", code, status)
	}
}

// -name removes a filter like -name removes a feature, also within one
// value, and an unknown -name only warns.
func TestFilterRemovals(t *testing.T) {
	root := t.TempDir()
	paths := model.Paths{UserFilterExtensionsDir: filepath.Join(root, "filters")}
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	opts := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameDocker, Filters: []string{"gws", "-gws", "-nope"}},
		BuildOptions: model.BuildOptions{ImageName: "test", Features: []string{}},
	}
	opts.Origins = model.ListOrigins{"filters": {
		"gws":   {Source: model.SourceCLI},
		"-gws":  {Source: model.SourceCLI},
		"-nope": {Source: model.SourceCLI},
	}}
	opts.DroppedRemovals = []model.DroppedRemoval{
		{Option: "filters", Name: "gone", Origin: model.EntryOrigin{Source: model.SourceProject, Scope: model.ScopeProject, Path: "/p/config.json"}},
		{Option: "filters", Name: "gws", Origin: model.EntryOrigin{Source: model.SourceProject, Scope: model.ScopeProject, Path: "/p/config.json"}},
		{Option: "features", Name: "gone", Origin: model.EntryOrigin{Source: model.SourceProject, Scope: model.ScopeProject, Path: "/p/config.json"}},
	}
	if filtersSelected(opts) {
		t.Fatalf("gws,-gws selects a filter")
	}
	_, _, warnings, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Paths: paths, Action: "run"})
	if err != nil {
		t.Fatalf("ValidateOptions = %v", err)
	}
	want := []string{
		"features in /p/config.json: ignoring removal of unknown features: gone; run 'enclave features list' to see available features",
		"--filters: ignoring removal of unknown filters: nope",
		"filters in /p/config.json: ignoring removal of unknown filters: gone",
	}
	if !slices.Equal(warnings, want) {
		t.Fatalf("warnings = %q, want %q", warnings, want)
	}
}

// filters list --json reports the effective selection like features list:
// the directives, the layer they come from, and the tool.
func TestFilterListJSONSelection(t *testing.T) {
	root := t.TempDir()
	paths := model.Paths{UserFilterExtensionsDir: filepath.Join(root, "extensions", "filters")}
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	for _, tc := range []struct {
		name    string
		opts    model.Options
		sources model.OptionSources
		source  string
		enabled bool
	}{
		{"cli", model.Options{RunOptions: model.RunOptions{Filters: []string{"gws:readonly"}, Tool: "claude"}},
			model.OptionSources{RunOptionSources: model.RunOptionSources{Filters: model.SourceCLI}}, "cli", true},
		{"tool override removes", model.Options{RunOptions: model.RunOptions{Filters: []string{}, Tool: "codex"}},
			model.OptionSources{RunOptionSources: model.RunOptionSources{Filters: model.SourceToolOverride}}, "tool_override", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &CommandInput{Ctx: NewAppContext(paths, t.TempDir()), Options: tc.opts, Sources: tc.sources,
				ExtRequest: &extinstall.Request{Kind: model.KindFilter, JSON: true}}
			out := captureStdout(t, func() {
				if code := runFilterExtensions(input); code != 0 {
					t.Errorf("exit code = %d", code)
				}
			})
			var decoded struct {
				Selection struct {
					Directives []string `json:"directives"`
					Source     string   `json:"source"`
					Tool       *string  `json:"tool"`
				} `json:"selection"`
				Extensions []struct {
					Name    string `json:"name"`
					Enabled bool   `json:"enabled"`
				} `json:"extensions"`
			}
			if err := json.Unmarshal([]byte(out), &decoded); err != nil {
				t.Fatalf("unmarshal: %v\n%s", err, out)
			}
			if decoded.Selection.Source != tc.source || decoded.Selection.Tool == nil || *decoded.Selection.Tool != tc.opts.Tool ||
				len(decoded.Selection.Directives) != len(tc.opts.Filters) {
				t.Fatalf("selection = %+v", decoded.Selection)
			}
			if len(decoded.Extensions) != 1 || decoded.Extensions[0].Name != "gws" || decoded.Extensions[0].Enabled != tc.enabled {
				t.Fatalf("extensions = %+v", decoded.Extensions)
			}
		})
	}
}
