// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestRequiredFeatureSelection(t *testing.T) {
	paths := model.Paths{ToolsDir: "../../extensions/tools", FeaturesDir: "../../extensions/features"}
	for _, tool := range []string{"claude", "theia", "theia-next"} {
		for _, options := range []model.BuildOptions{{Slim: true}, {Features: []string{}}, {Devcontainer: true}} {
			normalized, err := normalizeConfiguredBuildOptions(paths, options, tool)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normalized.ResolvedFeatures, []string{"claude-core"}) {
				t.Fatalf("%s: selection %v", tool, normalized.ResolvedFeatures)
			}
			selection, err := resolveRuntimeImageSelection(paths, normalized, tool)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(selection.Features, normalized.ResolvedFeatures) {
				t.Fatal("build selection differs")
			}
			enabled := resolveEnabledFeatures(paths, normalized)
			if len(enabled) != 1 || enabled[0].Name != "claude-core" {
				t.Fatalf("runtime selection: %v", enabled)
			}
			if resolveFeaturesArg(normalized) != "claude-core" {
				t.Fatal("build arguments dropped dependency")
			}
		}
	}
	if _, err := normalizeConfiguredBuildOptions(paths, model.BuildOptions{Features: []string{"default", "-claude-core"}}, "claude"); err == nil || !strings.Contains(err.Error(), "explicitly excluded") {
		t.Fatalf("exclusion: %v", err)
	}
	// Reusing normalized options for another tool must resolve that tool afresh.
	normalized, err := normalizeConfiguredBuildOptions(paths, model.BuildOptions{Features: []string{}}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	normalized, err = normalizeConfiguredBuildOptions(paths, normalized, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized.ResolvedFeatures, []string{"claude-core"}) {
		t.Fatal("stale tool dependency selection")
	}
}

func TestFeatureInstallDependencyOrder(t *testing.T) {
	block := generateFeatureInstallBlock([]featureInstall{
		{Name: "consumer", Priority: 1, HasScript: true, RequiredFeatures: []string{"core"}},
		{Name: "core", Priority: 999, HasScript: true, Required: true, UpdateStamp: "new-version"},
	})
	if strings.Index(block, "# feature: core") > strings.Index(block, "# feature: consumer") {
		t.Fatal("priority overrode dependency order")
	}
	if strings.Count(block, "# feature: core") != 1 {
		t.Fatal("dependency installed multiple times")
	}
	if !strings.Contains(block, "Feature update stamp: new-version") || !strings.Contains(block, "ENCLAVE_FEATURE_INSTALL_STRICT=1") {
		t.Fatal("required feature lacks cache invalidation or fatal failure handling")
	}
}

func TestShellFeatureDependencySelection(t *testing.T) {
	if _, err := exec.LookPath("yq"); err != nil {
		t.Skip("yq is required")
	}
	root := t.TempDir()
	for name, source := range map[string]string{
		"consumer": "schemaVersion: \"1\"\nkind: mixin\nname: consumer\npriority: 1\nrequiresFeatures: [core]\n",
		"core":     "schemaVersion: \"1\"\nkind: mixin\nname: core\npriority: 999\ndefaultEnabled: false\n",
	} {
		writeAppFile(t, filepath.Join(root, "features", name, "spec.yaml"), source, 0o644)
	}
	run := func(resolved string) (string, error) {
		command := exec.Command("bash", "-c", `set -euo pipefail; . "$COMMON"; enclave_list_enabled_features consumer`)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "COMMON=" + commonShScript(t), "ENCLAVE_EXTENSIONS_ROOT=" + root, "ENCLAVE_FEATURES_RESOLVED=" + resolved}
		out, err := command.CombinedOutput()
		return string(out), err
	}
	out, err := run("0")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.Count(out, "\tcore\t") != 1 || strings.Index(out, "\tcore\t") > strings.Index(out, "\tconsumer\t") {
		t.Fatalf("dependency order: %s", out)
	}
	out, err = run("1")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.Contains(out, "\tcore\t") {
		t.Fatal("resolved per-feature installs must not reinstall dependencies")
	}
	writeAppFile(t, filepath.Join(root, "features", "core", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: core\nrequiresFeatures: [consumer]\n", 0o644)
	out, err = run("0")
	if err == nil || !strings.Contains(out, "cycle") {
		t.Fatalf("cycle error: %v: %s", err, out)
	}
}

func TestQEMURejectsRequiredFeatureTool(t *testing.T) {
	paths := model.Paths{ToolsDir: "../../extensions/tools", FeaturesDir: "../../extensions/features"}
	_, _, err := coerceQEMURunOptions(model.Options{RunOptions: model.RunOptions{Tool: "claude"}}, model.DefaultOptionSources(), paths)
	if err == nil || !strings.Contains(err.Error(), "cannot install required features") {
		t.Fatalf("QEMU dependency error: %v", err)
	}
}
