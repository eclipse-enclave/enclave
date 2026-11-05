// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	_ "embed"
	"fmt"
	"path"
	"sort"
	"strings"

	"enclave/internal/model"
)

// a2aAgentBuilderImage must match the proxy-build stage of Dockerfile.gateway,
// where check-versions tracks the pin; a test enforces it.
const a2aAgentBuilderImage = "docker.io/library/golang:1.26.10-alpine3.24@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308"

const (
	a2aAgentBuildStage   = "a2a-agent-build"
	a2aAgentAssetsRel    = "runtime-assets/a2a-agent"
	a2aAgentInstallerRel = a2aAgentAssetsRel + "/enclave-install-a2a-agent"
	a2aAgentEntrypoint   = "/usr/local/bin/enclave-a2a-entrypoint"
)

//go:embed a2a_agent_build_inputs.txt
var a2aAgentBuildInputsManifest string

var a2aAgentBuildInputs = mustParseA2AAgentBuildInputs(a2aAgentBuildInputsManifest)

func mustParseA2AAgentBuildInputs(data string) []string {
	paths, err := parseA2AAgentBuildInputs(data)
	if err != nil {
		panic(err)
	}
	return paths
}

func parseA2AAgentBuildInputs(data string) ([]string, error) {
	var paths []string
	seen := map[string]bool{}
	for idx, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if clean := path.Clean(line); clean == "." || clean != line || path.IsAbs(line) || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("invalid A2A agent build input on line %d: %q", idx+1, raw)
		}
		if seen[line] {
			return nil, fmt.Errorf("duplicate A2A agent build input on line %d: %q", idx+1, line)
		}
		seen[line] = true
		paths = append(paths, line)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("A2A agent build inputs manifest is empty")
	}
	return paths, nil
}

const (
	a2aAgentStandardAnchor   = "FROM feature-base AS standard\n"
	a2aAgentInstallAnchor    = "COPY entrypoint.sh /usr/local/bin/entrypoint.sh\n"
	a2aAgentEntrypointAnchor = "ENTRYPOINT [\"/usr/local/bin/entrypoint.sh\"]\n"
)

// withA2AAgentBuild adds the agent build stage before the standard stage,
// installs the binary and the wrapper next to the entrypoint (the image's most
// volatile layer), and makes the wrapper the ENTRYPOINT.
func withA2AAgentBuild(dockerfile string) (string, error) {
	for _, anchor := range []string{a2aAgentStandardAnchor, a2aAgentInstallAnchor, a2aAgentEntrypointAnchor} {
		if n := strings.Count(dockerfile, anchor); n != 1 {
			return "", fmt.Errorf("dockerfile has %d of %q, want 1 for the A2A agent install", n, strings.TrimSpace(anchor))
		}
	}
	var stage strings.Builder
	fmt.Fprintf(&stage, "FROM %s AS %s\n", a2aAgentBuilderImage, a2aAgentBuildStage)
	for _, input := range a2aAgentBuildInputs {
		fmt.Fprintf(&stage, "COPY %s /src/%s\n", input, input)
	}
	fmt.Fprintf(&stage, "COPY %s /src/%s\n", a2aAgentAssetsRel, a2aAgentAssetsRel)
	fmt.Fprintf(&stage, "RUN sh /src/%s /src /out && \\\n", a2aAgentInstallerRel)
	fmt.Fprintf(&stage, "    install -m 0755 /src/%s/enclave-a2a-entrypoint /out/enclave-a2a-entrypoint\n\n", a2aAgentAssetsRel)
	install := fmt.Sprintf("COPY --from=%s /out/ /usr/local/bin/\n", a2aAgentBuildStage)
	entrypoint := fmt.Sprintf("ENTRYPOINT [%q]\n", a2aAgentEntrypoint)

	dockerfile = strings.Replace(dockerfile, a2aAgentStandardAnchor, stage.String()+a2aAgentStandardAnchor, 1)
	dockerfile = strings.Replace(dockerfile, a2aAgentInstallAnchor, install+a2aAgentInstallAnchor, 1)
	return strings.Replace(dockerfile, a2aAgentEntrypointAnchor, entrypoint, 1), nil
}

// a2aAgentImageName tags the agent variant apart from the session's regular
// image, e.g. enclave-claude:latest becomes enclave-claude:a2a-latest.
func a2aAgentImageName(name string) string {
	colon := strings.LastIndex(name, ":")
	if colon <= strings.LastIndex(name, "/") {
		return name + ":a2a"
	}
	return name[:colon+1] + "a2a-" + name[colon+1:]
}

// hashA2AAgentBuildInputs hashes the agent's sources and assets; only the
// agent variant's image hash includes it.
func hashA2AAgentBuildInputs(paths model.Paths) (string, error) {
	matcher, err := dockerignoreMatcher(paths.AppRoot)
	if err != nil {
		return "", err
	}
	var files []mergedExtensionFile
	for _, rel := range append([]string{a2aAgentAssetsRel}, a2aAgentBuildInputs...) {
		if err := collectRuntimeImageStaticFiles(paths.AppRoot, rel, matcher, &files); err != nil {
			return "", err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	return hashRuntimeInputFiles(files)
}
