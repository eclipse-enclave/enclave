// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/apikeys"
	"enclave/internal/auth"
	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/model"
)

func builtinKeys(t *testing.T) []apikeys.Key {
	t.Helper()
	paths := builtinToolPaths(t)
	profiles, _, failed, err := loadAllProfiles(paths)
	if err != nil || len(failed) > 0 {
		t.Fatalf("load built-in profiles: failed = %v, err = %v", failed, err)
	}
	return apikeys.Catalog(profiles)
}

func TestBuiltinAPIKeys(t *testing.T) {
	keys := builtinKeys(t)
	want := map[string]struct {
		variable string
		tools    []string
	}{
		"anthropic":      {"ANTHROPIC_API_KEY", []string{"claude", "opencode", "pi", "theia", "theia-next"}},
		"openai":         {"OPENAI_API_KEY", []string{"codex", "opencode", "pi", "theia", "theia-next"}},
		"gemini":         {"GEMINI_API_KEY", []string{"opencode", "pi", "theia", "theia-next"}},
		"mistral":        {"MISTRAL_API_KEY", []string{"pi"}},
		"copilot-github": {"COPILOT_GITHUB_TOKEN", []string{"copilot"}},
	}
	if got := apikeys.Names(keys); len(got) != len(want) {
		t.Fatalf("keys = %v", got)
	}
	for name, w := range want {
		key, err := apikeys.Find(keys, name)
		if err != nil {
			t.Fatal(err)
		}
		tools := key.Tools()
		slices.Sort(tools)
		if key.Variable != w.variable || !slices.Equal(tools, w.tools) {
			t.Fatalf("%s: variable = %s, tools = %v", name, key.Variable, tools)
		}
	}
}

func TestResolveSecretsTarget(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "abc123"}
	keys := builtinKeys(t)
	openai, _ := apikeys.Find(keys, "openai")

	tests := []struct {
		name     string
		req      cli.SecretsRequest
		path     string
		variable string
		remove   []string
		tools    int
	}{
		{"global shared", cli.SecretsRequest{}, config.HostSecretsGlobalSharedFile(home), "OPENAI_API_KEY", []string{"CODEX_API_KEY", "OPENAI_API_KEY"}, 5},
		{"project shared", cli.SecretsRequest{Project: true}, config.HostSecretsProjectSharedFile(home, "abc123"), "OPENAI_API_KEY", nil, 5},
		{"global tool", cli.SecretsRequest{Tool: "codex"}, config.HostSecretsGlobalFile(home, "codex"), "OPENAI_API_KEY", []string{"CODEX_API_KEY", "OPENAI_API_KEY"}, 1},
		{"project tool", cli.SecretsRequest{Tool: "pi", Project: true}, config.HostSecretsProjectFile(home, "abc123", "pi"), "OPENAI_API_KEY", []string{"OPENAI_API_KEY"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := resolveSecretsTarget(home, project, openai, &tt.req, nil)
			if err != nil {
				t.Fatal(err)
			}
			if target.Path != tt.path || target.Set[0] != tt.variable || len(target.Uses) != tt.tools {
				t.Fatalf("target = %+v", target)
			}
			if tt.remove != nil {
				got := slices.Clone(target.Remove)
				slices.Sort(got)
				if !slices.Equal(got, tt.remove) {
					t.Fatalf("remove = %v, want %v", got, tt.remove)
				}
			}
		})
	}

	if _, err := resolveSecretsTarget(home, project, openai, &cli.SecretsRequest{Tool: "claude"}, nil); err == nil || !strings.Contains(err.Error(), "does not use the openai key") {
		t.Fatalf("claude with openai: err = %v", err)
	}
}

func TestResolveSecretsTargetWritesEveryToolsVariable(t *testing.T) {
	key := apikeys.Key{
		Name:     "openai",
		Variable: "OPENAI_API_KEY",
		EnvVars:  []string{"OPENAI_API_KEY", "CODEX_API_KEY"},
		Uses: []apikeys.Use{
			{Tool: "a", EnvVars: []string{"OPENAI_API_KEY"}},
			{Tool: "b", EnvVars: []string{"OPENAI_API_KEY"}},
			{Tool: "c", EnvVars: []string{"CODEX_API_KEY"}},
		},
	}
	target, err := resolveSecretsTarget(t.TempDir(), model.Project{Hash: "abc123"}, key, &cli.SecretsRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(target.Set, []string{"OPENAI_API_KEY", "CODEX_API_KEY"}) {
		t.Fatalf("set = %v, want both variables so tool c sees the key", target.Set)
	}
}

func TestDescribeAPIKeyFileFirst(t *testing.T) {
	home := t.TempDir()
	tokenFile := filepath.Join(home, "token")
	if err := os.WriteFile(tokenFile, []byte("file-token-0000000000005555\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	use := apikeys.Use{
		Tool: "tool", SecretID: "svc-api-key", EnvVars: []string{"SVC_API_KEY"},
		File: &model.SecretFileSource{Path: tokenFile}, Priority: model.SecretPriorityFileFirst,
	}
	layers := []auth.SecretsLayer{{Path: config.HostSecretsGlobalSharedFile(home), Values: map[string]string{"SVC_API_KEY": "env-token-00000000000006666"}}}
	row := describeAPIKey(home, model.Project{Hash: "abc123"}, "svc", use, func(string) string { return "" }, layers, nil)
	if row.Source != "file "+tokenFile || row.Value != "file...5555" {
		t.Fatalf("row = %+v", row)
	}
}

func TestDescribeAPIKeyReportsAliasConflict(t *testing.T) {
	home := t.TempDir()
	use := apikeys.Use{Tool: "codex", SecretID: "openai-api-key", EnvVars: []string{"CODEX_API_KEY", "OPENAI_API_KEY"}}
	layers := []auth.SecretsLayer{{Path: config.HostSecretsGlobalSharedFile(home), Values: map[string]string{"CODEX_API_KEY": "one", "OPENAI_API_KEY": "two"}}}
	row := describeAPIKey(home, model.Project{Hash: "abc123"}, "openai", use, func(string) string { return "" }, layers, nil)
	if row.Source != "error" {
		t.Fatalf("row = %+v, want the conflict sessions fail on", row)
	}
}

func TestSecretsSharedProjectLayerOrder(t *testing.T) {
	home := t.TempDir()
	paths := auth.SecretsLayerPaths(home, "abc123", "claude", model.SecretsScopeBoth)
	want := []string{
		config.HostSecretsGlobalSharedFile(home),
		config.HostSecretsGlobalFile(home, "claude"),
		config.HostSecretsProjectSharedFile(home, "abc123"),
		config.HostSecretsProjectFile(home, "abc123", "claude"),
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("layers = %v", paths)
	}
	if got := auth.SecretsLayerPaths(home, "abc123", "claude", model.SecretsScopeGlobal); len(got) != 2 {
		t.Fatalf("global scope layers = %v", got)
	}
	if got := auth.SecretsLayerPaths(home, "abc123", "claude", model.SecretsScopeProject); len(got) != 3 || got[1] != want[2] {
		t.Fatalf("project scope layers = %v", got)
	}
}

func TestValidateSecretValue(t *testing.T) {
	if got, err := validateSecretValue("  sk-ant-x\n"); err != nil || got != "sk-ant-x" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", " \n", "a\nb", "'quoted'", "x\""} {
		if _, err := validateSecretValue(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestReadPipedSecretRejectsOversizedInput(t *testing.T) {
	if got, err := readPipedSecret(strings.NewReader("sk-ant-x\n")); err != nil || got != "sk-ant-x" {
		t.Fatalf("got %q, %v", got, err)
	}
	long := strings.Repeat("a", maxSecretInput+1)
	if _, err := readPipedSecret(strings.NewReader(long)); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("oversized input: err = %v", err)
	}
}

func TestRemoveFromEnvStore(t *testing.T) {
	home := t.TempDir()
	dir := config.HostStoreEnvDir(home, "claude", "abc123")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("ANTHROPIC_API_KEY=old\nGH_TOKEN=keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A saved copy with another value (from the shell, say) is not touched.
	removed, err := removeFromEnvStore(home, "claude", "abc123", []string{"ANTHROPIC_API_KEY"}, map[string]bool{"other": true}, neverProvided)
	if err != nil || removed {
		t.Fatalf("non-matching value: removed = %v, err = %v", removed, err)
	}
	// A copy the project's own secrets file would restore is not touched.
	removed, err = removeFromEnvStore(home, "claude", "abc123", []string{"ANTHROPIC_API_KEY"}, map[string]bool{"old": true}, func() bool { return true })
	if err != nil || removed {
		t.Fatalf("still provided: removed = %v, err = %v", removed, err)
	}
	removed, err = removeFromEnvStore(home, "claude", "abc123", []string{"ANTHROPIC_API_KEY"}, map[string]bool{"old": true}, neverProvided)
	if err != nil || !removed {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
	values, err := readEnvStoreValues(home, "claude", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values["ANTHROPIC_API_KEY"]; ok || values["GH_TOKEN"] != "keep" {
		t.Fatalf("store = %v", values)
	}

	hashes, err := projectHashesWithEnvStore(home, "claude")
	if err != nil || len(hashes) != 1 || hashes[0] != "abc123" {
		t.Fatalf("hashes = %v, err = %v", hashes, err)
	}
	if removed, err := removeFromEnvStore(home, "claude", "missing", []string{"ANTHROPIC_API_KEY"}, map[string]bool{"old": true}, neverProvided); err != nil || removed {
		t.Fatalf("missing store: removed = %v, err = %v", removed, err)
	}
}

func TestDescribeAPIKeyPrecedence(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "abc123"}
	use := apikeys.Use{Tool: "codex", Provider: "openai", SecretID: "openai-api-key", EnvVars: []string{"CODEX_API_KEY", "OPENAI_API_KEY"}}
	layers := []auth.SecretsLayer{
		{Path: config.HostSecretsGlobalSharedFile(home), Values: map[string]string{"OPENAI_API_KEY": "sk-shared-0000000000001111"}},
		{Path: config.HostSecretsProjectSharedFile(home, "abc123"), Values: map[string]string{"OPENAI_API_KEY": "sk-proj-00000000000002222"}},
	}
	stored := map[string]string{"CODEX_API_KEY": "sk-saved-0000000000003333"}
	noEnv := func(string) string { return "" }
	shellEnv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "sk-shell-0000000000004444"
		}
		return ""
	}

	row := describeAPIKey(home, project, "openai", use, shellEnv, layers, stored)
	if row.Source != "shell" || row.Variable != "OPENAI_API_KEY" || row.Value != "sk-s...4444" {
		t.Fatalf("row = %+v", row)
	}
	row = describeAPIKey(home, project, "openai", use, noEnv, layers, stored)
	if row.Source != "project" || row.Value != "sk-p...2222" {
		t.Fatalf("row = %+v", row)
	}
	row = describeAPIKey(home, project, "openai", use, noEnv, layers[:1], stored)
	if row.Source != "global" {
		t.Fatalf("row = %+v", row)
	}
	row = describeAPIKey(home, project, "openai", use, noEnv, nil, stored)
	if row.Source != "saved" {
		t.Fatalf("row = %+v", row)
	}
	row = describeAPIKey(home, project, "openai", use, noEnv, nil, nil)
	if row.Source != "-" || row.Variable != "CODEX_API_KEY" {
		t.Fatalf("row = %+v", row)
	}
}

func TestSecretsLayerLabel(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "abc123"}
	tests := map[string]string{
		config.HostSecretsGlobalSharedFile(home):            "global",
		config.HostSecretsGlobalFile(home, "pi"):            "global/pi",
		config.HostSecretsProjectSharedFile(home, "abc123"): "project",
		config.HostSecretsProjectFile(home, "abc123", "pi"): "project/pi",
	}
	for path, want := range tests {
		if got := secretsLayerLabel(home, project, "pi", path); got != want {
			t.Fatalf("label(%s) = %q, want %q", path, got, want)
		}
	}
}

// builtinToolPaths points at the repository's own extensions tree and an
// empty user root.
func builtinToolPaths(t *testing.T) model.Paths {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	user := t.TempDir()
	return model.Paths{
		AppRoot:           root,
		ExtensionsDir:     filepath.Join(root, "extensions"),
		ToolsDir:          filepath.Join(root, "extensions", "tools"),
		FeaturesDir:       filepath.Join(root, "extensions", "features"),
		UserExtensionsDir: user,
		UserToolsDir:      filepath.Join(user, "tools"),
		UserFeaturesDir:   filepath.Join(user, "features"),
	}
}

func TestSecretsRowNote(t *testing.T) {
	use := apikeys.Use{Tool: "codex", RequiredPrefix: "at-"}
	resolved := secretsListRow{value: "sk-not-at"}
	tests := []struct {
		name       string
		row        secretsListRow
		opts       model.Options
		restricted bool
		want       string
	}{
		{"unset key", secretsListRow{}, model.Options{AuthOptions: model.AuthOptions{NoAPIKey: true}}, true, ""},
		{"no_api_key", resolved, model.Options{AuthOptions: model.AuthOptions{NoAPIKey: true}}, true, "withheld by no_api_key"},
		{"ephemeral", resolved, model.Options{RunOptions: model.RunOptions{Ephemeral: true}}, true, "withheld: ephemeral without pass_api_key"},
		{"ephemeral with pass", resolved, model.Options{RunOptions: model.RunOptions{Ephemeral: true}, AuthOptions: model.AuthOptions{PassAPIKey: true}}, false, ""},
		{"prefix restricted", resolved, model.Options{}, true, `refused under restricted networking: must start with "at-"`},
		{"prefix unrestricted", resolved, model.Options{}, false, ""},
		{"prefix allow-all", resolved, model.Options{RunOptions: model.RunOptions{AllowAllNetwork: true}}, true, ""},
		{"prefix ok", secretsListRow{value: "at-123"}, model.Options{}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := secretsRowNote(tt.row, use, tt.opts, tt.restricted); got != tt.want {
				t.Fatalf("note = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolNotesGroupsTools(t *testing.T) {
	var notes toolNotes
	notes.add("uses the shell", "claude")
	notes.add("uses global/pi", "pi")
	notes.add("uses the shell", "opencode")
	var got []string
	notes.each(func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) })
	want := []string{"claude, opencode: uses the shell", "pi: uses global/pi"}
	if !slices.Equal(got, want) {
		t.Fatalf("notes = %q, want %q", got, want)
	}
}

func TestSecretsInspectorReadsEachToolOnce(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "abc123"}
	path := config.HostSecretsGlobalSharedFile(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ANTHROPIC_API_KEY=first-value-000000001111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspector := newSecretsInspector(home, project, func(string) model.Options { return testOpts(model.SecretsScopeBoth, true) }, func(string) string { return "" })
	use := apikeys.Use{Tool: "pi", SecretID: "anthropic-api-key", EnvVars: []string{"ANTHROPIC_API_KEY"}}
	if row := inspector.describe("anthropic", use); row.Value != "firs...1111" {
		t.Fatalf("row = %+v", row)
	}
	if err := os.WriteFile(path, []byte("ANTHROPIC_API_KEY=second-value-00000002222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if row := inspector.describe("anthropic", use); row.Value != "firs...1111" {
		t.Fatalf("second describe re-read the files: %+v", row)
	}
}

func TestClaimedVariablesProtectOtherCredentials(t *testing.T) {
	profiles := []model.Profile{{
		Name: "tool",
		Secrets: map[string]model.SecretConfig{
			"openai-api-key": {EnvVars: []string{"OPENAI_API_KEY", "CODEX_API_KEY"}},
		},
	}}
	features := []model.Extension{{
		Name:    "proxy",
		Secrets: map[string]model.SecretConfig{"proxy-openai": {EnvVars: []string{"OPENAI_API_KEY"}}},
	}}
	key := apikeys.Key{
		Name: "openai", Variable: "OPENAI_API_KEY", EnvVars: []string{"OPENAI_API_KEY", "CODEX_API_KEY"},
		Uses: []apikeys.Use{{Tool: "tool", SecretID: "openai-api-key", EnvVars: []string{"OPENAI_API_KEY", "CODEX_API_KEY"}}},
	}
	claimed := claimedVariables(profiles, features, key)
	if claimed["OPENAI_API_KEY"] != "feature proxy (proxy-openai)" || claimed["CODEX_API_KEY"] != "" {
		t.Fatalf("claimed = %v", claimed)
	}
	// Only Codex reads CODEX_API_KEY, so the shared file can still serve it.
	key.Uses = append(key.Uses, apikeys.Use{Tool: "codex", SecretID: "openai-api-key", EnvVars: []string{"CODEX_API_KEY"}})
	target, err := resolveSecretsTarget(t.TempDir(), model.Project{Hash: "abc123"}, key, &cli.SecretsRequest{}, claimed)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(target.Remove, []string{"CODEX_API_KEY"}) || !slices.Equal(target.Set, []string{"CODEX_API_KEY"}) {
		t.Fatalf("set = %v, remove = %v; want the feature's OPENAI_API_KEY neither written nor removed", target.Set, target.Remove)
	}
	if target.Set[0] != "CODEX_API_KEY" || len(target.Skipped) != 1 {
		t.Fatalf("target = %+v", target)
	}
	if _, err := resolveSecretsTarget(t.TempDir(), model.Project{Hash: "abc123"}, key, &cli.SecretsRequest{Tool: "tool"}, map[string]string{"OPENAI_API_KEY": "x", "CODEX_API_KEY": "y"}); err == nil {
		t.Fatal("a key whose every variable is claimed must be refused")
	}
}

func TestProjectStillProvides(t *testing.T) {
	home := t.TempDir()
	use := apikeys.Use{Tool: "claude", EnvVars: []string{"ANTHROPIC_API_KEY"}}
	path := config.HostSecretsProjectFile(home, "abc123", "claude")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ANTHROPIC_API_KEY=same\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]bool{"same": true}
	if !projectStillProvides(home, "abc123", use, model.SecretsScopeBoth, values) {
		t.Fatal("project file still holds the value")
	}
	if projectStillProvides(home, "abc123", use, model.SecretsScopeGlobal, values) {
		t.Fatal("global scope does not read the project file")
	}
	if projectStillProvides(home, "other", use, model.SecretsScopeBoth, values) {
		t.Fatal("another project has no file")
	}
}

func TestSecretsInspectorUsesPerToolScope(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "abc123"}
	path := config.HostSecretsProjectFile(home, "abc123", "codex")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("OPENAI_API_KEY=project-value-00000001111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{"codex": model.SecretsScopeGlobal}
	inspector := newSecretsInspector(home, project, func(tool string) model.Options {
		if scope, ok := scopes[tool]; ok {
			return testOpts(scope, false)
		}
		return testOpts(model.SecretsScopeBoth, false)
	}, func(string) string { return "" })
	use := apikeys.Use{Tool: "codex", EnvVars: []string{"OPENAI_API_KEY"}}
	if row := inspector.describe("openai", use); row.Source != "-" {
		t.Fatalf("codex runs with global scope and must not see the project file: %+v", row)
	}
}

func TestResolveSecretsTargetRejectsTwoCredentialsInOneTool(t *testing.T) {
	key := apikeys.Key{Name: "foo", Variable: "FOO_API_KEY", EnvVars: []string{"FOO_API_KEY", "FOO_TOKEN"}, Uses: []apikeys.Use{
		{Tool: "x", SecretID: "foo-api-key", EnvVars: []string{"FOO_API_KEY"}},
		{Tool: "x", SecretID: "foo-token", EnvVars: []string{"FOO_TOKEN"}},
	}}
	_, err := resolveSecretsTarget(t.TempDir(), model.Project{Hash: "abc123"}, key, &cli.SecretsRequest{Tool: "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "several credentials") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrefixRefused(t *testing.T) {
	use := apikeys.Use{RequiredPrefix: "AIza"}
	cases := []struct {
		value      string
		opts       model.Options
		restricted bool
		want       bool
	}{
		{"bad", model.Options{}, true, true},
		{"AIza1", model.Options{}, true, false},
		{"bad", model.Options{}, false, false},
		{"bad", model.Options{RunOptions: model.RunOptions{AllowAllNetwork: true}}, true, false},
	}
	for _, c := range cases {
		if got := prefixRefused(use, c.value, c.opts, c.restricted); got != c.want {
			t.Errorf("prefixRefused(%q, %+v, %v) = %v", c.value, c.opts, c.restricted, got)
		}
	}
}

func TestSecretsInspectorSkipsEnvStoreWithoutPersist(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "abc123"}
	dir, err := hoststore.ResolveDir(home, backend.EnvStoreKey("claude", "abc123"), backend.StoreKindEnv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("ANTHROPIC_API_KEY=saved-value-0000111122\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	use := apikeys.Use{Tool: "claude", SecretID: "anthropic-api-key", EnvVars: []string{"ANTHROPIC_API_KEY"}}
	for _, persist := range []bool{true, false} {
		inspector := newSecretsInspector(home, project, func(string) model.Options {
			return testOpts(model.SecretsScopeBoth, persist)
		}, func(string) string { return "" })
		row := inspector.describe("anthropic", use)
		if want := map[bool]string{true: "saved", false: "-"}[persist]; row.Source != want {
			t.Fatalf("persist=%v: source = %q, want %q", persist, row.Source, want)
		}
	}
}

func testOpts(scope string, persist bool) model.Options {
	var opts model.Options
	opts.SecretsScope = scope
	opts.Persist = persist
	return opts
}

func neverProvided() bool { return false }
