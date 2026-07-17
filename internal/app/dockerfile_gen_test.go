// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateFeatureInstallBlockScopesPerFeature(t *testing.T) {
	block := generateFeatureInstallBlock([]featureInstall{
		{Name: "node-dev", Priority: 70, HasApt: false, HasScript: true, NeedsRoot: false},
		{Name: "apt-sample", Priority: 85, HasApt: true, HasScript: false, NeedsRoot: false},
		{Name: "devtools", Priority: 40, HasApt: true, HasScript: true, NeedsRoot: false},
		{Name: "github-cli", Priority: 50, HasApt: false, HasScript: true, NeedsRoot: true},
	})

	// Ordered by (priority, name): devtools(40) < github-cli(50) < node-dev(70) < apt-sample(85).
	order := []string{
		"# feature: devtools (priority 40)",
		"# feature: github-cli (priority 50)",
		"# feature: node-dev (priority 70)",
		"# feature: apt-sample (priority 85)",
	}
	prev := -1
	for _, marker := range order {
		idx := strings.Index(block, marker)
		if idx == -1 {
			t.Fatalf("missing feature marker %q in:\n%s", marker, block)
		}
		if idx < prev {
			t.Fatalf("feature blocks out of priority order at %q in:\n%s", marker, block)
		}
		prev = idx
	}

	for _, feature := range []string{"devtools", "github-cli", "node-dev", "apt-sample"} {
		want := `COPY ["extensions/features/` + feature + `","/opt/enclave/extensions/features/` + feature + `"]`
		if !strings.Contains(block, want) {
			t.Fatalf("expected per-feature copy %q, got:\n%s", want, block)
		}
	}

	// devtools: apt install + user-phase script (with the package cache mounts).
	if !strings.Contains(block, `FEATURES="devtools" /opt/enclave/build-scripts/install-feature-apt-packages.sh`) {
		t.Fatalf("expected devtools apt install, got:\n%s", block)
	}
	if !strings.Contains(block, `--mount=type=cache,id=enclave-npm-${USER_ID}`) ||
		!strings.Contains(block, `FEATURES="devtools" \`) {
		t.Fatalf("expected devtools user-phase script with cache mounts, got:\n%s", block)
	}

	// github-cli: root-phase script, no apt install, no package cache mounts.
	if !strings.Contains(block, `FEATURES="github-cli" \`) || !strings.Contains(block, "ENCLAVE_FEATURE_PHASE=root") {
		t.Fatalf("expected github-cli root-phase script, got:\n%s", block)
	}
	if strings.Contains(block, `FEATURES="github-cli" /opt/enclave/build-scripts/install-feature-apt-packages.sh`) {
		t.Fatalf("github-cli has no apt packages and must not emit an apt install, got:\n%s", block)
	}

	// apt-sample: apt only, no script run.
	if !strings.Contains(block, `FEATURES="apt-sample" /opt/enclave/build-scripts/install-feature-apt-packages.sh`) {
		t.Fatalf("expected apt-sample apt install, got:\n%s", block)
	}
	if strings.Contains(block, `FEATURES="apt-sample" \`) {
		t.Fatalf("apt-sample has no install script and must not run one, got:\n%s", block)
	}

	// node-dev: user script only, no apt install.
	if strings.Contains(block, `FEATURES="node-dev" /opt/enclave/build-scripts/install-feature-apt-packages.sh`) {
		t.Fatalf("node-dev has no apt packages and must not emit an apt install, got:\n%s", block)
	}

	if !strings.HasSuffix(block, "USER ${USERNAME}\n") {
		t.Fatalf("feature block must end by restoring the unprivileged user, got:\n%s", block)
	}
}

func TestGenerateFeatureInstallBlockInstallCommandsRoot(t *testing.T) {
	block := generateFeatureInstallBlock([]featureInstall{
		{Name: "root-cmds", Priority: 50, HasInstallCommands: true, InstallCommandsNeedRoot: true},
	})
	if !strings.Contains(block, "USER root\n") {
		t.Fatalf("root install-commands must run in the root phase, got:\n%s", block)
	}
	if !strings.Contains(block, "/opt/enclave/build-scripts/install-extension-commands.sh") {
		t.Fatalf("expected install-extension-commands.sh run, got:\n%s", block)
	}
	if !strings.Contains(block, "ENCLAVE_AGENT_USER=${USERNAME}") {
		t.Fatalf("root phase must pass the agent user for privilege drops, got:\n%s", block)
	}
	// A root-phase install-commands block must not use the user-phase package
	// caches (they belong to the agent-owned home).
	if strings.Contains(block, "--mount=type=cache,id=enclave-npm-${USER_ID}") {
		t.Fatalf("root phase must not mount user package caches, got:\n%s", block)
	}
}

func TestGenerateFeatureInstallBlockInstallCommandsUser(t *testing.T) {
	block := generateFeatureInstallBlock([]featureInstall{
		{Name: "user-cmds", Priority: 50, HasInstallCommands: true, InstallCommandsNeedRoot: false},
	})
	if !strings.Contains(block, "/opt/enclave/build-scripts/install-extension-commands.sh") {
		t.Fatalf("expected install-extension-commands.sh run, got:\n%s", block)
	}
	for _, mount := range []string{
		"--mount=type=cache,id=enclave-npm-${USER_ID}",
		"--mount=type=cache,id=enclave-gomod-${USER_ID}",
		"--mount=type=cache,id=enclave-uv-${USER_ID}",
	} {
		if !strings.Contains(block, mount) {
			t.Fatalf("user phase must mount %q, got:\n%s", mount, block)
		}
	}
	// The user phase runs as the sandbox user directly; no root-phase privilege
	// drop env is emitted.
	if strings.Contains(block, "ENCLAVE_AGENT_USER=${USERNAME}") {
		t.Fatalf("user phase must not emit the root-phase agent-user env, got:\n%s", block)
	}
}

func TestGenerateFeatureInstallBlockScriptWinsNoInstallCommands(t *testing.T) {
	// install.sh wins: HasScript is set and HasInstallCommands is false, so the
	// declarative synthesizer must never be woven in.
	block := generateFeatureInstallBlock([]featureInstall{
		{Name: "script-feat", Priority: 50, HasScript: true, HasInstallCommands: false},
	})
	if strings.Contains(block, "install-extension-commands.sh") {
		t.Fatalf("install.sh feature must not emit commands.install synthesis, got:\n%s", block)
	}
}

func TestGenerateFeatureInstallBlockEmpty(t *testing.T) {
	if got := generateFeatureInstallBlock(nil); got != "USER ${USERNAME}\n" {
		t.Fatalf("empty feature selection should only restore the user, got: %q", got)
	}
}

// TestGenerateFeatureInstallBlockStableUnderHigherPriorityAddition is the core
// cache-granularity guarantee: adding a higher-priority-value feature must not
// change any earlier feature's generated text, so Docker reuses those layers.
func TestGenerateFeatureInstallBlockStableUnderHigherPriorityAddition(t *testing.T) {
	base := []featureInstall{
		{Name: "devtools", Priority: 40, HasApt: true, HasScript: true, NeedsRoot: false},
		{Name: "node-dev", Priority: 70, HasApt: false, HasScript: true, NeedsRoot: false},
	}
	without := generateFeatureInstallBlock(base)
	with := generateFeatureInstallBlock(append(append([]featureInstall(nil), base...),
		featureInstall{Name: "apt-sample", Priority: 85, HasApt: true}))

	// The lower-priority blocks (everything before the trailing USER restore)
	// must be a byte-identical prefix of the larger render.
	earlierBlocks := strings.TrimSuffix(without, "USER ${USERNAME}\n")
	if !strings.HasPrefix(with, earlierBlocks) {
		t.Fatalf("adding a higher-priority feature changed earlier feature blocks (cache would be invalidated)\nwithout:\n%s\nwith:\n%s", without, with)
	}
}

func TestRenderDockerfileWeavesFeatureAndToolBlocks(t *testing.T) {
	dir := t.TempDir()
	template := "FROM tool-base AS feature-base\n" +
		"ARG USERNAME=agent\n" +
		"USER root\n" +
		"# BEGIN ENCLAVE_FEATURE_INSTALLS\n" +
		"COPY extensions/features /opt/enclave/extensions/features\n" +
		"RUN echo fallback\n" +
		"# END ENCLAVE_FEATURE_INSTALLS\n" +
		"# BEGIN ENCLAVE_TOOL_INSTALLS\n" +
		"# END ENCLAVE_TOOL_INSTALLS\n"
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	got, err := renderDockerfile(path, []string{"claude"},
		[]featureInstall{{Name: "apt-sample", Priority: 85, HasApt: true}}, nil, nil)
	if err != nil {
		t.Fatalf("renderDockerfile: %v", err)
	}

	if strings.Contains(got, "RUN echo fallback") {
		t.Fatalf("aggregated fallback should be replaced by generated feature blocks, got:\n%s", got)
	}
	if strings.Contains(got, "COPY extensions/features /opt/enclave/extensions/features") {
		t.Fatalf("aggregated feature copy should be replaced by per-feature copies, got:\n%s", got)
	}
	if !strings.Contains(got, "# feature: apt-sample (priority 85)") {
		t.Fatalf("expected generated feature block, got:\n%s", got)
	}
	if !strings.Contains(got, "FROM tool-base AS tool-claude") {
		t.Fatalf("expected woven tool stage, got:\n%s", got)
	}
	// Markers are preserved so a re-render is idempotent.
	for _, marker := range []string{
		featureInstallMarkerStart, featureInstallMarkerEnd,
		toolInstallMarkerStart, toolInstallMarkerEnd,
	} {
		if !strings.Contains(got, marker) {
			t.Fatalf("expected marker %q preserved, got:\n%s", marker, got)
		}
	}
}

// TestRenderRealDockerfileTemplate renders the actual repo Dockerfile to prove
// its markers are present and correctly ordered (the build itself needs Docker,
// which is out of scope for unit tests).
func TestRenderRealDockerfileTemplate(t *testing.T) {
	repoDockerfile := filepath.Join("..", "..", "Dockerfile")
	if _, err := os.Stat(repoDockerfile); err != nil {
		t.Skipf("repo Dockerfile not found: %v", err)
	}

	got, err := renderDockerfile(repoDockerfile, []string{"claude"}, []featureInstall{
		{Name: "devtools", Priority: 40, HasApt: true, HasScript: true, NeedsRoot: false},
		{Name: "apt-sample", Priority: 85, HasApt: true},
	}, nil, nil)
	if err != nil {
		t.Fatalf("renderDockerfile on real template: %v", err)
	}
	if strings.Contains(got, "Aggregates apt_packages from all enabled feature") {
		t.Fatalf("real-template feature fallback was not replaced, got:\n%s", got)
	}
	if strings.Contains(got, "COPY extensions/features /opt/enclave/extensions/features") {
		t.Fatalf("real-template aggregate feature copy was not replaced, got:\n%s", got)
	}
	for _, want := range []string{
		"# feature: devtools (priority 40)",
		`COPY ["extensions/features/devtools","/opt/enclave/extensions/features/devtools"]`,
		"# feature: apt-sample (priority 85)",
		`COPY ["extensions/features/apt-sample","/opt/enclave/extensions/features/apt-sample"]`,
		"FROM tool-base AS tool-claude",
		"FROM feature-base AS standard",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("real template render missing %q", want)
		}
	}
	// Feature blocks must be woven before the tool stages that build on them.
	if strings.Index(got, "# feature: devtools") > strings.Index(got, "FROM tool-base AS tool-claude") {
		t.Fatal("feature install block must precede the woven tool stages")
	}
}

func TestRenderDockerfileMissingFeatureMarker(t *testing.T) {
	dir := t.TempDir()
	// Template with only tool markers must now fail fast.
	template := "FROM tool-base AS feature-base\n# BEGIN ENCLAVE_TOOL_INSTALLS\n# END ENCLAVE_TOOL_INSTALLS\n"
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	if _, err := renderDockerfile(path, []string{"claude"}, nil, nil, nil); err == nil {
		t.Fatal("expected error when feature marker is missing")
	}
}
