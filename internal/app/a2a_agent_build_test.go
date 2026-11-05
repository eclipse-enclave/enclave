// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"enclave/internal/model"
	"enclave/internal/util"
)

var repoRootFromApp = filepath.Join("..", "..")

func TestParseA2AAgentBuildInputsRejectsInvalidEntries(t *testing.T) {
	for _, data := range []string{"go.mod\ngo.mod\n", "../go.mod\n", "/go.mod\n", "internal/../go.mod\n", "# only a comment\n"} {
		if _, err := parseA2AAgentBuildInputs(data); err == nil {
			t.Errorf("expected parse error for %q", data)
		}
	}
}

// The build stage copies only the manifest, so a missing package builds from
// the repo but fails from embedded or packaged assets.
func TestA2AAgentBuildInputsCoverInternalDeps(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/enclave-a2a-agent")
	cmd.Dir = repoRootFromApp
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		rel, ok := strings.CutPrefix(pkg, "enclave/")
		if !ok {
			continue
		}
		covered := false
		for _, input := range a2aAgentBuildInputs {
			if rel == input || strings.HasPrefix(rel, input+"/") {
				covered = true
			}
		}
		if !covered {
			t.Errorf("enclave-a2a-agent depends on %s but a2a_agent_build_inputs.txt does not list %q", pkg, rel)
		}
	}
}

func TestA2AAgentBuildInputsExist(t *testing.T) {
	for _, input := range append([]string{a2aAgentInstallerRel, a2aAgentAssetsRel + "/enclave-a2a-entrypoint"}, a2aAgentBuildInputs...) {
		if _, err := os.Stat(filepath.Join(repoRootFromApp, input)); err != nil {
			t.Errorf("build input %s: %v", input, err)
		}
	}
}

func TestA2AAgentBuilderImageMatchesGatewayPin(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRootFromApp, "Dockerfile.gateway"))
	if err != nil {
		t.Skipf("Dockerfile.gateway not found: %v", err)
	}
	match := regexp.MustCompile(`(?m)^FROM (\S*golang:\S+) AS proxy-build$`).FindStringSubmatch(string(data))
	if match == nil {
		t.Fatal("Dockerfile.gateway has no golang proxy-build stage")
	}
	if match[1] != a2aAgentBuilderImage {
		t.Fatalf("a2aAgentBuilderImage = %q, want the gateway pin %q", a2aAgentBuilderImage, match[1])
	}
}

func TestA2AAgentBuildInputsAreOnlyRealDeps(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/enclave-a2a-agent")
	cmd.Dir = repoRootFromApp
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	deps := strings.Fields(string(out))

	// go.sum is a real input exactly when a third-party module is linked.
	cmd = exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{.Path}}{{end}}", "./cmd/enclave-a2a-agent")
	cmd.Dir = repoRootFromApp
	modules, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list modules: %v", err)
	}
	thirdParty := false
	for _, module := range strings.Fields(string(modules)) {
		thirdParty = thirdParty || module != "enclave"
	}
	listsGoSum := false
	for _, input := range a2aAgentBuildInputs {
		listsGoSum = listsGoSum || input == "go.sum"
	}
	if thirdParty != listsGoSum {
		t.Errorf("a2a_agent_build_inputs.txt lists go.sum: %v, but third-party modules linked: %v", listsGoSum, thirdParty)
	}

	for _, input := range a2aAgentBuildInputs {
		if input == "go.mod" || input == "go.sum" {
			continue
		}
		used := false
		for _, pkg := range deps {
			rel, ok := strings.CutPrefix(pkg, "enclave/")
			if ok && (rel == input || strings.HasPrefix(rel, input+"/")) {
				used = true
			}
		}
		if !used {
			t.Errorf("a2a_agent_build_inputs.txt lists %s, which enclave-a2a-agent does not link", input)
		}
	}
}

func TestWithA2AAgentBuild(t *testing.T) {
	regular := renderRealDockerfile(t)
	if strings.Contains(regular, "a2a") {
		t.Fatal("the regular session image mentions the A2A agent")
	}
	got, err := withA2AAgentBuild(regular)
	if err != nil {
		t.Fatal(err)
	}
	stage := "FROM " + a2aAgentBuilderImage + " AS a2a-agent-build\n"
	install := "COPY --from=a2a-agent-build /out/ /usr/local/bin/\n"
	entrypoint := "ENTRYPOINT [\"/usr/local/bin/enclave-a2a-entrypoint\"]\n"
	for _, want := range []string{
		stage,
		"COPY cmd/enclave-a2a-agent /src/cmd/enclave-a2a-agent\n",
		"COPY go.mod /src/go.mod\n",
		"COPY runtime-assets/a2a-agent /src/runtime-assets/a2a-agent\n",
		"RUN sh /src/runtime-assets/a2a-agent/enclave-install-a2a-agent /src /out && \\\n",
		"    install -m 0755 /src/runtime-assets/a2a-agent/enclave-a2a-entrypoint /out/enclave-a2a-entrypoint\n",
		install,
		entrypoint,
	} {
		if strings.Count(got, want) != 1 {
			t.Fatalf("variant has %d of %q, want 1", strings.Count(got, want), want)
		}
	}
	if strings.Contains(got, `ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]`) {
		t.Fatal("the variant keeps the regular ENTRYPOINT")
	}
	standard := strings.Index(got, "FROM feature-base AS standard")
	if strings.Index(got, stage) > standard || strings.Index(got, install) < standard ||
		strings.Index(got, install) > strings.Index(got, "COPY entrypoint.sh") ||
		strings.Index(got, entrypoint) < strings.Index(got, "COPY entrypoint.sh") {
		t.Fatal("the build stage must precede the standard stage, the install must sit before the entrypoint copy, and the wrapper must be the final ENTRYPOINT")
	}
	if _, err := withA2AAgentBuild("FROM feature-base AS standard\nCOPY entrypoint.sh /usr/local/bin/entrypoint.sh\n"); err == nil {
		t.Fatal("a template without the ENTRYPOINT line must fail")
	}
}

func TestA2AAgentImageName(t *testing.T) {
	for in, want := range map[string]string{
		"enclave-claude:latest":               "enclave-claude:a2a-latest",
		"enclave-claude:custom-abc123-slim":   "enclave-claude:a2a-custom-abc123-slim",
		"registry:5000/enclave-claude:latest": "registry:5000/enclave-claude:a2a-latest",
		"registry:5000/enclave-claude":        "registry:5000/enclave-claude:a2a",
	} {
		if got := a2aAgentImageName(in); got != want {
			t.Errorf("a2aAgentImageName(%q) = %q, want %q", in, got, want)
		}
	}

	opts := model.BuildOptions{ImageName: model.ImageName}
	regular, err := resolveBuildConfig(opts, "claude", model.Project{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts.A2AAgent = true
	variant, err := resolveBuildConfig(opts, "claude", model.Project{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if regular.ImageName != "enclave-claude:latest" || variant.ImageName != "enclave-claude:a2a-latest" {
		t.Fatalf("image names %q and %q", regular.ImageName, variant.ImageName)
	}
	if variant.HashSuffix != regular.HashSuffix {
		t.Fatal("the variant's build config differs beyond the image name")
	}
	opts.ImageNameSet, opts.ImageName = true, "mine:v1"
	if named, _ := resolveBuildConfig(opts, "claude", model.Project{}, t.TempDir()); named.ImageName != "mine:v1" {
		t.Fatalf("an explicit --image-name became %q", named.ImageName)
	}
}

// Sessions without --peer-group must get the image they got before A14: the
// same Dockerfile and entrypoint, and a hash that no agent input feeds into.
func TestRegularImageHashIgnoresA2AAgent(t *testing.T) {
	repoEntrypoint, err := os.ReadFile(filepath.Join(repoRootFromApp, "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(repoEntrypoint)), "a2a") {
		t.Fatal("entrypoint.sh, which every image carries, must not handle A2A; the variant's wrapper does")
	}
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile", "FROM debian:trixie-slim AS tool-base\nFROM tool-base AS feature-base\n# BEGIN ENCLAVE_FEATURE_INSTALLS\n# END ENCLAVE_FEATURE_INSTALLS\n# BEGIN ENCLAVE_TOOL_INSTALLS\n# END ENCLAVE_TOOL_INSTALLS\nCOPY entrypoint.sh /usr/local/bin/entrypoint.sh\nENTRYPOINT [\"/usr/local/bin/entrypoint.sh\"]\n")
	write("entrypoint.sh", "#!/bin/bash\n")
	write(a2aAgentAssetsRel+"/enclave-a2a-entrypoint", "#!/bin/sh\n")
	write("go.mod", "module enclave\n")
	write("cmd/enclave-a2a-agent/main.go", "package main\n")
	write(a2aAgentInstallerRel, "#!/bin/sh\n")
	paths := model.Paths{
		AppRoot:       root,
		Dockerfile:    filepath.Join(root, "Dockerfile"),
		Entrypoint:    filepath.Join(root, "entrypoint.sh"),
		ExtensionsDir: filepath.Join(root, "extensions"),
		ToolsDir:      filepath.Join(root, "extensions", "tools"),
	}
	cfg := buildConfig{ImageName: "enclave-a14-test:never-built", HashSuffix: "suffix"}
	hashes := func() (regular, variant string) {
		t.Helper()
		_, regular, err := needsRebuildForSelection(paths, cfg, runtimeImageSelection{})
		if err != nil {
			t.Fatal(err)
		}
		_, variant, err = needsRebuildForSelection(paths, cfg, runtimeImageSelection{A2AAgent: true})
		if err != nil {
			t.Fatal(err)
		}
		return regular, variant
	}

	regular, variant := hashes()
	dockerfile, err := renderEngineDockerfile(paths.Dockerfile, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	entrypointHash, _ := util.HashFile(paths.Entrypoint)
	extensionsHash, _ := hashMergedExtensionFiles(paths, runtimeImageSelection{})
	staticHash, _ := hashRuntimeImageStaticFiles(paths)
	if want := util.HashString(dockerfile) + "-" + entrypointHash + "-" + extensionsHash + "-" + staticHash + "-suffix"; regular != want {
		t.Fatalf("regular hash %q, want the pre-A14 formula %q", regular, want)
	}
	if variant == regular || !strings.HasPrefix(variant, util.HashString(mustWithA2AAgentBuild(t, dockerfile))+"-") {
		t.Fatalf("variant hash %q must hash the variant Dockerfile", variant)
	}

	for _, rel := range []string{"cmd/enclave-a2a-agent/main.go", "go.mod", a2aAgentInstallerRel, a2aAgentAssetsRel + "/enclave-a2a-entrypoint"} {
		write(rel, "changed")
		nextRegular, nextVariant := hashes()
		if nextRegular != regular {
			t.Fatalf("changing %s changed the regular image hash", rel)
		}
		if nextVariant == variant {
			t.Fatalf("changing %s did not change the A2A variant hash", rel)
		}
		variant = nextVariant
	}
}

func mustWithA2AAgentBuild(t *testing.T, dockerfile string) string {
	t.Helper()
	got, err := withA2AAgentBuild(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRuntimeImageSelectionCarriesA2AAgent(t *testing.T) {
	paths := writeTestCodexToolPaths(t, false)
	for _, want := range []bool{false, true} {
		selection, err := resolveRuntimeImageSelection(paths, model.BuildOptions{Slim: true, A2AAgent: want}, "codex")
		if err != nil {
			t.Fatal(err)
		}
		if selection.A2AAgent != want {
			t.Fatalf("selection.A2AAgent = %v, want %v", selection.A2AAgent, want)
		}
	}
}

func TestInstallA2AAgentScriptBuildsTheBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles enclave-a2a-agent")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	src, err := filepath.Abs(repoRootFromApp)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	cmd := exec.Command("sh", filepath.Join(src, a2aAgentInstallerRel), src, out)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install script: %v\n%s", err, output)
	}
	info, err := os.Stat(filepath.Join(out, "enclave-a2a-agent"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed binary: %v, %v", info, err)
	}

	err = exec.Command("sh", filepath.Join(src, a2aAgentInstallerRel), src).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("missing argument: err = %v, want exit 2", err)
	}
}
