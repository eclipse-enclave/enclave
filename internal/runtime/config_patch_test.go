// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

func TestAddToolSettingsTemplate(t *testing.T) {
	t.Parallel()

	r := newTemplateOverrideRuntime(t.TempDir(), model.Profile{
		Name:           "pi",
		SettingsFile:   "pi-settings.json",
		SettingsTarget: ".pi/settings.json",
	})
	acc := newMountAccumulator(nil, nil)
	r.addToolSettingsTemplate(acc)

	if len(acc.Mounts()) != 0 {
		t.Fatalf("expected no settings mount, got %d", len(acc.Mounts()))
	}
	if got, ok := lookupEnv(acc.Env(), model.EnvToolSettingsTemplate); !ok || got != "/usr/local/share/enclave/templates/pi-settings.json" {
		t.Fatalf("unexpected settings template env: %q, present=%v", got, ok)
	}
	if got, ok := lookupEnv(acc.Env(), model.EnvToolSettingsTarget); !ok || got != "/home/agent/.pi/settings.json" {
		t.Fatalf("unexpected settings target env: %q, present=%v", got, ok)
	}
}

func TestConfigPatchesLayerAcrossScopes(t *testing.T) {
	t.Parallel()

	r := newSettingsRuntime(t, "pi", `{
		"source":"built-in",
		"keep":true,
		"remove":"value",
		"array":[1,2],
		"nested":{"builtIn":true,"replace":"built-in"}
	}`)
	writeSettingsPatch(t, r, false, `{
		"source":"global-patch",
		"array":[3],
		"nested":{"global":true,"replace":"global"}
	}`)
	writeSettingsPatch(t, r, true, `{
		"source":"project-patch",
		"remove":null,
		"nested":{"project":true}
	}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	got := readGeneratedJSON(t, r, settingsRelativePath(t, r))
	want := map[string]any{
		"source": "project-patch",
		"keep":   true,
		"array":  []any{float64(3)},
		"nested": map[string]any{
			"builtIn": true,
			"global":  true,
			"project": true,
			"replace": "global",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected merged settings:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestProjectPatchMergesOntoGlobalFullConfig(t *testing.T) {
	t.Parallel()

	r := newSettingsRuntime(t, "pi", `{"builtIn":true}`)
	writeCanonicalSettings(t, r, false, `{"source":"global-full","global":true}`)
	writeSettingsPatch(t, r, true, `{"source":"project-patch","project":true}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	got := readGeneratedJSON(t, r, settingsRelativePath(t, r))
	want := map[string]any{"source": "project-patch", "global": true, "project": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected merged settings: got %#v, want %#v", got, want)
	}
}

func TestProjectFullConfigReplacesGlobalPatchResult(t *testing.T) {
	t.Parallel()

	r := newSettingsRuntime(t, "pi", `{"builtIn":true}`)
	writeSettingsPatch(t, r, false, `{"global":true}`)
	writeCanonicalSettings(t, r, true, `{"source":"project-full"}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	got := readGeneratedJSON(t, r, settingsRelativePath(t, r))
	want := map[string]any{"source": "project-full"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected settings: got %#v, want %#v", got, want)
	}
}

func TestGlobalPatchMergesOntoHostConfig(t *testing.T) {
	t.Parallel()

	r := newSettingsRuntime(t, "pi", `{"source":"built-in","builtIn":true}`)
	r.profile.HostConfigDir = ".pi"
	r.profile.PassthroughPaths = []string{"agent/settings.json"}
	r.run.HostConfig = model.HostConfigPassthrough
	writeRuntimeTestFile(t, filepath.Join(r.host.Home, ".pi", "agent", "settings.json"), `{"source":"host","host":true}`)
	writeSettingsPatch(t, r, false, `{"source":"global-patch","global":true}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	got := readGeneratedJSON(t, r, settingsRelativePath(t, r))
	want := map[string]any{"source": "global-patch", "host": true, "global": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected settings: got %#v, want %#v", got, want)
	}
}

func TestFullConfigAndPatchAtSameScopeFail(t *testing.T) {
	t.Parallel()

	for _, projectScope := range []bool{false, true} {
		projectScope := projectScope
		scope := "global"
		if projectScope {
			scope = "project"
		}
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			r := newSettingsRuntime(t, "pi", `{"source":"built-in"}`)
			writeCanonicalSettings(t, r, projectScope, `{"source":"full"}`)
			writeSettingsPatch(t, r, projectScope, `{"source":"patch"}`)

			err := r.prepareToolConfigSource()
			if err == nil || !strings.Contains(err.Error(), scope+" config override") || !strings.Contains(err.Error(), "is ambiguous") {
				t.Fatalf("expected %s conflict error, got %v", scope, err)
			}
		})
	}
}

func TestTOMLConfigPatchesLayerAcrossScopes(t *testing.T) {
	t.Parallel()

	r := newSettingsRuntime(t, "codex", "model = \"built-in\"\n[sandbox]\nmode = \"safe\"\n")
	writeSettingsPatch(t, r, false, "model = \"global\"\n[sandbox]\nglobal = true\n")
	writeSettingsPatch(t, r, true, "[sandbox]\nmode = \"project\"\nproject = true\n")

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	data, err := os.ReadFile(generatedConfigPath(r, settingsRelativePath(t, r)))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	var got map[string]any
	if _, err := toml.Decode(string(data), &got); err != nil {
		t.Fatalf("decode generated TOML: %v", err)
	}
	sandbox, ok := got["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("expected sandbox table, got %#v", got["sandbox"])
	}
	if got["model"] != "global" || sandbox["mode"] != "project" || sandbox["global"] != true || sandbox["project"] != true {
		t.Fatalf("unexpected generated TOML: %#v", got)
	}
}

func TestConfigPatchCanTargetNonSettingsFile(t *testing.T) {
	t.Parallel()

	r := newGenericConfigRuntime(t)
	writeBuiltInConfig(t, r, "preferences.json", `{"source":"built-in","nested":{"keep":true}}`)
	writeConfigPatch(t, r, false, "preferences.json", `{"source":"patch","nested":{"added":true}}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	got := readGeneratedJSON(t, r, "preferences.json")
	want := map[string]any{
		"source": "patch",
		"nested": map[string]any{"keep": true, "added": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected generic config patch result: got %#v, want %#v", got, want)
	}
}

func TestConfigPatchArtifactsAreIgnored(t *testing.T) {
	t.Parallel()

	r := newGenericConfigRuntime(t)
	writeBuiltInConfig(t, r, "preferences.json", `{"source":"built-in"}`)
	writeBuiltInConfig(t, r, ".preferences.json", `{"source":"built-in-hidden"}`)
	writeConfigPatch(t, r, false, "preferences.json", `{"source":"patch"}`)
	writeConfigPatch(t, r, false, ".preferences.json", `{"source":"hidden-patch"}`)
	patchDir := config.HostToolPatchesDir(r.host.Home, r.profile.Name)
	for _, name := range []string{".DS_Store", ".preferences.json.swp", ".#preferences.json", "preferences.json~", "#preferences.json#"} {
		writeRuntimeTestFile(t, filepath.Join(patchDir, name), "not a patch")
	}

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	got := readGeneratedJSON(t, r, "preferences.json")
	if got["source"] != "patch" {
		t.Fatalf("expected valid patch to apply, got %#v", got)
	}
	hidden := readGeneratedJSON(t, r, ".preferences.json")
	if hidden["source"] != "hidden-patch" {
		t.Fatalf("expected hidden JSON patch to apply, got %#v", hidden)
	}
}

func TestConfigPatchWithoutConfigDirFails(t *testing.T) {
	t.Parallel()

	r := newTemplateOverrideRuntime(t.TempDir(), model.Profile{Name: "tool"})
	writeConfigPatch(t, r, false, "settings.json", `{"source":"patch"}`)

	err := r.prepareToolConfigSource()
	if err == nil || !strings.Contains(err.Error(), "require config_dir") {
		t.Fatalf("expected missing config_dir error, got %v", err)
	}
}

func TestEmptyConfigPatchDirectoryIsIgnored(t *testing.T) {
	t.Parallel()

	r := newTemplateOverrideRuntime(t.TempDir(), model.Profile{Name: "tool"})
	emptyDir := filepath.Join(config.HostToolPatchesDir(r.host.Home, r.profile.Name), "nested")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("create empty patch directory: %v", err)
	}

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	if r.configSourceDir != "" {
		t.Fatalf("expected empty patch directory not to activate config source, got %q", r.configSourceDir)
	}
}

func TestInvalidJSONConfigPatchFails(t *testing.T) {
	t.Parallel()

	r := newSettingsRuntime(t, "pi", `{"source":"built-in"}`)
	writeSettingsPatch(t, r, false, `{"broken":`)

	err := r.prepareToolConfigSource()
	if err == nil || !strings.Contains(err.Error(), "read patch JSON") {
		t.Fatalf("expected invalid JSON patch error, got %v", err)
	}
}

func TestInvalidTOMLConfigPatchFails(t *testing.T) {
	t.Parallel()

	r := newSettingsRuntime(t, "codex", "model = \"built-in\"\n")
	writeSettingsPatch(t, r, false, "[broken\n")

	err := r.prepareToolConfigSource()
	if err == nil || !strings.Contains(err.Error(), "read patch TOML") {
		t.Fatalf("expected invalid TOML patch error, got %v", err)
	}
}

func TestConfigPatchWithoutLowerPrecedenceTargetFails(t *testing.T) {
	t.Parallel()

	r := newGenericConfigRuntime(t)
	writeConfigPatch(t, r, false, "missing.json", `{"source":"patch"}`)

	err := r.prepareToolConfigSource()
	if err == nil || !strings.Contains(err.Error(), "has no lower-precedence target") {
		t.Fatalf("expected missing target error, got %v", err)
	}
}

func TestUnsupportedConfigPatchFormatFails(t *testing.T) {
	t.Parallel()

	r := newGenericConfigRuntime(t)
	writeBuiltInConfig(t, r, "preferences.yaml", "source: built-in\n")
	writeConfigPatch(t, r, false, "preferences.yaml", "source: patch\n")

	err := r.prepareToolConfigSource()
	if err == nil || !strings.Contains(err.Error(), "unsupported config patch format") {
		t.Fatalf("expected unsupported format error, got %v", err)
	}
}

func TestSymlinkedConfigPatchDirectoryFailsClearly(t *testing.T) {
	t.Parallel()

	r := newGenericConfigRuntime(t)
	externalDir := t.TempDir()
	writeRuntimeTestFile(t, filepath.Join(externalDir, "preferences.json"), `{"source":"patch"}`)
	patchDir := config.HostToolPatchesDir(r.host.Home, r.profile.Name)
	if err := os.MkdirAll(patchDir, 0o755); err != nil {
		t.Fatalf("create patch directory: %v", err)
	}
	linkedDir := filepath.Join(patchDir, "linked")
	if err := os.Symlink(externalDir, linkedDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	err := r.prepareToolConfigSource()
	if err == nil || !strings.Contains(err.Error(), "symlinked config patch directory") || !strings.Contains(err.Error(), "is not supported") {
		t.Fatalf("expected unsupported symlinked directory error, got %v", err)
	}
}

func newSettingsRuntime(t *testing.T, tool string, builtIn string) *Runtime {
	t.Helper()
	var profile model.Profile
	switch tool {
	case "codex":
		profile = model.Profile{
			Name:           "codex",
			ConfigDir:      ".codex",
			SettingsFile:   "codex-config.toml",
			SettingsTarget: ".codex/config.toml",
		}
	case "pi":
		profile = model.Profile{
			Name:           "pi",
			ConfigDir:      ".pi",
			SettingsFile:   "pi-settings.json",
			SettingsTarget: ".pi/agent/settings.json",
		}
	default:
		t.Fatalf("unsupported settings test tool %q", tool)
	}
	r := newTemplateOverrideRuntime(t.TempDir(), profile)
	writeBuiltInToolTemplate(t, r, builtIn)
	return r
}

func newGenericConfigRuntime(t *testing.T) *Runtime {
	t.Helper()
	return newTemplateOverrideRuntime(t.TempDir(), model.Profile{Name: "tool", ConfigDir: ".tool"})
}

func writeSettingsPatch(t *testing.T, r *Runtime, projectScope bool, content string) {
	t.Helper()
	writeConfigPatch(t, r, projectScope, settingsRelativePath(t, r), content)
}

func writeConfigPatch(t *testing.T, r *Runtime, projectScope bool, relativePath string, content string) {
	t.Helper()
	baseDir := config.HostToolPatchesDir(r.host.Home, r.profile.Name)
	if projectScope {
		baseDir = config.HostProjectToolPatchesDir(r.host.Home, r.project.Hash, r.profile.Name)
	}
	writeRuntimeTestFile(t, filepath.Join(baseDir, relativePath), content)
}

func writeCanonicalSettings(t *testing.T, r *Runtime, projectScope bool, content string) {
	t.Helper()
	baseDir := config.HostToolConfigDir(r.host.Home, r.profile.Name)
	if projectScope {
		baseDir = config.HostProjectConfigDir(r.host.Home, r.project.Hash, r.profile.Name)
	}
	writeRuntimeTestFile(t, filepath.Join(baseDir, settingsRelativePath(t, r)), content)
}

func writeBuiltInConfig(t *testing.T, r *Runtime, relativePath string, content string) {
	t.Helper()
	writeRuntimeTestFile(t, filepath.Join(r.paths.ToolsDir, r.profile.Name, "config-base", relativePath), content)
}

func settingsRelativePath(t *testing.T, r *Runtime) string {
	t.Helper()
	relativePath, err := r.settingsRelativePath()
	if err != nil {
		t.Fatalf("resolve settings relative path: %v", err)
	}
	return relativePath
}

func generatedConfigPath(r *Runtime, relativePath string) string {
	return filepath.Join(r.configSourceDir, relativePath)
}

func readGeneratedJSON(t *testing.T, r *Runtime, relativePath string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(generatedConfigPath(r, relativePath))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}
	return value
}

func writeRuntimeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newTemplateOverrideRuntime(home string, profile model.Profile) *Runtime {
	return &Runtime{
		paths:         model.Paths{ToolsDir: filepath.Join(home, "extensions", "tools")},
		host:          model.Host{Home: home},
		project:       model.Project{Hash: "projhash"},
		profile:       profile,
		containerHome: "/home/agent",
	}
}

func writeBuiltInToolTemplate(t *testing.T, r *Runtime, content string) {
	t.Helper()
	prefix := r.profile.Name + "-"
	templateName := strings.TrimPrefix(r.profile.SettingsFile, prefix)
	path := filepath.Join(r.paths.ToolsDir, r.profile.Name, "templates", templateName)
	writeRuntimeTestFile(t, path, content)
}

func lookupMountSource(mounts []backend.Mount, target string) (string, bool) {
	for _, mount := range mounts {
		if mount.ContainerPath == target {
			return mount.Source, true
		}
	}
	return "", false
}
