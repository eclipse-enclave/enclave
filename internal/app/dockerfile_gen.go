// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	toolInstallMarkerStart    = "# BEGIN ENCLAVE_TOOL_INSTALLS"
	toolInstallMarkerEnd      = "# END ENCLAVE_TOOL_INSTALLS"
	featureInstallMarkerStart = "# BEGIN ENCLAVE_FEATURE_INSTALLS"
	featureInstallMarkerEnd   = "# END ENCLAVE_FEATURE_INSTALLS"
)

// featureInstall describes a single selected feature for per-feature Dockerfile
// layer generation. Each feature becomes its own contiguous block so that
// adding or changing one feature only invalidates layers at or after its
// (priority, name) position instead of re-running every feature's install.
type featureInstall struct {
	Name      string
	Priority  int
	HasApt    bool
	HasScript bool
	NeedsRoot bool
	// HasInstallCommands weaves the declarative commands.install synthesizer for
	// a mixin that ships install steps instead of an install.sh sidecar. It is
	// mutually exclusive with HasScript (install.sh wins). NeedRoot forces the
	// root build phase when any install step targets root.
	HasInstallCommands      bool
	InstallCommandsNeedRoot bool
}

func renderDockerfile(templatePath string, tools []string, features []featureInstall, stamps map[string]string, forceTools map[string]bool) (string, error) {
	// #nosec G304 -- templatePath is resolved from trusted app asset locations.
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return "", err
	}
	template := string(data)

	template, err = replaceMarkerBlock(template, featureInstallMarkerStart, featureInstallMarkerEnd, generateFeatureInstallBlock(features))
	if err != nil {
		return "", err
	}

	toolBlock, err := generateToolInstallBlock(tools, stamps, forceTools)
	if err != nil {
		return "", err
	}
	template, err = replaceMarkerBlock(template, toolInstallMarkerStart, toolInstallMarkerEnd, toolBlock)
	if err != nil {
		return "", err
	}
	return template, nil
}

// replaceMarkerBlock swaps the content between startMarker and endMarker
// (inclusive) for the generated body, keeping the markers in place.
func replaceMarkerBlock(template string, startMarker string, endMarker string, body string) (string, error) {
	start := strings.Index(template, startMarker)
	if start == -1 {
		return "", fmt.Errorf("dockerfile template missing %s", startMarker)
	}
	end := strings.Index(template, endMarker)
	if end == -1 {
		return "", fmt.Errorf("dockerfile template missing %s", endMarker)
	}
	if end < start {
		return "", fmt.Errorf("dockerfile template marker order invalid for %s", startMarker)
	}
	end += len(endMarker)
	replacement := startMarker + "\n" + body + endMarker
	return template[:start] + replacement + template[end:], nil
}

// generateFeatureInstallBlock emits one source-copy + install block per selected
// feature in (priority, name) order. Each feature copies its own source tree,
// installs its own apt packages, and runs its own install script in the
// appropriate user phase, so an apt-only feature added late (higher priority
// value) does not re-run earlier features' copies or scripts.
func generateFeatureInstallBlock(features []featureInstall) string {
	ordered := append([]featureInstall(nil), features...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority < ordered[j].Priority
		}
		return ordered[i].Name < ordered[j].Name
	})

	var b strings.Builder
	for _, feature := range ordered {
		name := strings.TrimSpace(feature.Name)
		if name == "" {
			continue
		}
		fmt.Fprintf(&b, "# feature: %s (priority %d)\n", name, feature.Priority)
		b.WriteString("USER root\n")
		featureSource := "extensions/features/" + name
		featureTarget := "/opt/enclave/extensions/features/" + name
		b.WriteString(dockerfileCopyInstruction(featureSource, featureTarget))
		b.WriteString(dockerfileNormalizeExtensionTree(featureTarget))
		if feature.HasApt {
			b.WriteString("RUN --mount=type=cache,id=enclave-apt-cache,target=/var/cache/apt,sharing=locked \\\n")
			b.WriteString("    --mount=type=cache,id=enclave-apt-lib,target=/var/lib/apt,sharing=locked \\\n")
			fmt.Fprintf(&b, "    FEATURES=%q /opt/enclave/build-scripts/install-feature-apt-packages.sh\n", name)
		}
		if feature.HasScript {
			if feature.NeedsRoot {
				fmt.Fprintf(&b, "RUN FEATURES=%q \\\n", name)
				b.WriteString("    ENCLAVE_FEATURE_PHASE=root \\\n")
				b.WriteString("    /opt/enclave/build-scripts/run-feature-installs.sh\n")
			} else {
				b.WriteString("USER ${USERNAME}\n")
				b.WriteString("RUN --mount=type=cache,id=enclave-npm-${USER_ID},target=/home/${USERNAME}/.npm,uid=${USER_ID},gid=${GROUP_ID} \\\n")
				b.WriteString("    --mount=type=cache,id=enclave-gomod-${USER_ID},target=/home/${USERNAME}/go/pkg/mod,uid=${USER_ID},gid=${GROUP_ID} \\\n")
				b.WriteString("    --mount=type=cache,id=enclave-uv-${USER_ID},target=/home/${USERNAME}/.cache/uv,uid=${USER_ID},gid=${GROUP_ID} \\\n")
				fmt.Fprintf(&b, "    FEATURES=%q \\\n", name)
				b.WriteString("    ENCLAVE_FEATURE_PHASE=user \\\n")
				b.WriteString("    /opt/enclave/build-scripts/run-feature-installs.sh\n")
			}
		}
		if feature.HasInstallCommands {
			if feature.InstallCommandsNeedRoot {
				b.WriteString("USER root\n")
				fmt.Fprintf(&b, "RUN FEATURES=%q \\\n", name)
				b.WriteString("    ENCLAVE_AGENT_USER=${USERNAME} \\\n")
				b.WriteString("    /opt/enclave/build-scripts/install-extension-commands.sh\n")
			} else {
				b.WriteString("USER ${USERNAME}\n")
				b.WriteString("RUN --mount=type=cache,id=enclave-npm-${USER_ID},target=/home/${USERNAME}/.npm,uid=${USER_ID},gid=${GROUP_ID} \\\n")
				b.WriteString("    --mount=type=cache,id=enclave-gomod-${USER_ID},target=/home/${USERNAME}/go/pkg/mod,uid=${USER_ID},gid=${GROUP_ID} \\\n")
				b.WriteString("    --mount=type=cache,id=enclave-uv-${USER_ID},target=/home/${USERNAME}/.cache/uv,uid=${USER_ID},gid=${GROUP_ID} \\\n")
				fmt.Fprintf(&b, "    FEATURES=%q \\\n", name)
				b.WriteString("    /opt/enclave/build-scripts/install-extension-commands.sh\n")
			}
		}
	}
	// Restore the unprivileged user for any downstream instructions and to match
	// the aggregated fallback's end state.
	b.WriteString("USER ${USERNAME}\n")
	return b.String()
}

func dockerfileCopyInstruction(src string, dst string) string {
	data, _ := json.Marshal([]string{src, dst})
	return "COPY " + string(data) + "\n"
}

// Mirrors the extension install.sh execute rule in Dockerfile, debian/rules,
// internal/appassets, and internal/app/build_permissions.go.
func dockerfileNormalizeExtensionTree(target string) string {
	quotedTarget := util.ShellQuote(target)
	installScript := util.ShellQuote(target + "/" + model.InstallScriptFilename)
	return fmt.Sprintf("RUN chmod -R a+rX %s && \\\n    if [ -f %s ]; then chmod a+rx %s; fi\n", quotedTarget, installScript, installScript)
}

// generateToolInstallBlock creates per-tool stages and merges them into the final standard stage.
// This keeps cache invalidation scoped to the tool that changed while avoiding full reinstalls.
func writeStageArgs(b *strings.Builder) {
	b.WriteString("ARG USER_ID=1000\n")
	b.WriteString("ARG GROUP_ID=1000\n")
	b.WriteString("ARG USERNAME=agent\n")
	b.WriteString("ARG AGENT_TOOLS=all\n")
}

func generateToolInstallBlock(tools []string, stamps map[string]string, forceTools map[string]bool) (string, error) {
	ordered := append([]string(nil), tools...)
	sort.Strings(ordered)

	type toolStage struct {
		name  string
		stage string
		stamp string
	}

	var stages []toolStage
	seen := map[string]bool{}
	for _, tool := range ordered {
		name := strings.TrimSpace(tool)
		if name == "" {
			continue
		}
		if seen[name] {
			return "", fmt.Errorf("duplicate tool: %s", name)
		}
		seen[name] = true
		stamp := stamps[name]
		if stamp == "" {
			stamp = agentUpdateStampUnknown
		}
		stages = append(stages, toolStage{
			name:  name,
			stage: "tool-" + name,
			stamp: stamp,
		})
	}

	var b strings.Builder
	for _, tool := range stages {
		fmt.Fprintf(&b, "FROM tool-base AS %s\n", tool.stage)
		writeStageArgs(&b)
		b.WriteString("RUN : > /tmp/installed-tools.txt\n")
		toolTarget := "/opt/enclave/extensions/tools/" + tool.name
		fmt.Fprintf(&b, "COPY extensions/tools/%s %s\n", tool.name, toolTarget)
		// COPY output is root-owned and tool-base leaves USER at the agent,
		// so the mode normalization must run as root.
		b.WriteString("USER root\n")
		b.WriteString(dockerfileNormalizeExtensionTree(toolTarget))
		b.WriteString("USER ${USERNAME}\n")
		fmt.Fprintf(&b, "RUN --mount=type=cache,id=enclave-npm-${USER_ID}-%s,target=/home/${USERNAME}/.npm,uid=${USER_ID},gid=${GROUP_ID},sharing=locked \\\n", tool.name)
		fmt.Fprintf(&b, "    echo %q && \\\n", "Agent update stamp: "+tool.stamp)
		if forceTools[tool.name] {
			fmt.Fprintf(&b, "    enclave-install-tool %s \"${AGENT_TOOLS}\" force\n\n", tool.name)
		} else {
			fmt.Fprintf(&b, "    enclave-install-tool %s \"${AGENT_TOOLS}\"\n\n", tool.name)
		}
	}

	b.WriteString("FROM feature-base AS standard\n")
	writeStageArgs(&b)
	b.WriteString("RUN mkdir -p /tmp/installed-tools.d\n")
	for _, tool := range stages {
		fmt.Fprintf(&b, "COPY --from=%s /tmp/installed-tools.txt /tmp/installed-tools.d/%s\n", tool.stage, tool.name)
	}
	for _, tool := range stages {
		fmt.Fprintf(&b, "COPY --from=%s --chown=${USER_ID}:${GROUP_ID} /home/${USERNAME}/.local /home/${USERNAME}/.local\n", tool.stage)
		fmt.Fprintf(&b, "COPY --from=%s --chown=${USER_ID}:${GROUP_ID} /home/${USERNAME}/.config /home/${USERNAME}/.config\n", tool.stage)
	}
	// Use find rather than a glob so an empty selection (no tool stages)
	// still produces an empty manifest instead of an unmatched-glob "cat: ...
	// No such file or directory" error.
	b.WriteString("RUN if [ -d /tmp/installed-tools.d ]; then \\\n")
	b.WriteString("        find /tmp/installed-tools.d -type f -exec cat {} + | sort -u > $HOME/.installed-tools; \\\n")
	b.WriteString("        rm -rf /tmp/installed-tools.d; \\\n")
	b.WriteString("    fi\n")
	return b.String(), nil
}
