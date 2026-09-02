// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"
	"slices"

	"enclave/internal/model"
)

// diffCapabilities reports what an update would change about what an extension
// can do, gained capabilities first. Every list- or count-valued field on
// capabilities participates.
func diffCapabilities(before capabilities, after capabilities) []string {
	var changes []string
	beforeYolo, afterYolo := before.yoloActive(), after.yoloActive()
	switch {
	case afterYolo && !beforeYolo:
		changes = append(changes, fmt.Sprintf("now skips approval prompts (%s runs the agent unattended)", after.Spec.YoloFlag))
	case !afterYolo && beforeYolo:
		changes = append(changes, fmt.Sprintf("no longer skips approval prompts (drops %s)", before.Spec.YoloFlag))
	case afterYolo && beforeYolo && before.Spec.YoloFlag != after.Spec.YoloFlag:
		changes = append(changes, fmt.Sprintf("changes the approval-skipping flag from %s to %s", before.Spec.YoloFlag, after.Spec.YoloFlag))
	}
	changes = append(changes, diffFlag(before.Spec.NeedsRoot, after.Spec.NeedsRoot,
		"now installs as root", "no longer installs as root")...)
	changes = append(changes, diffFlag(before.InstallScript, after.InstallScript,
		"adds "+model.InstallScriptFilename, "drops "+model.InstallScriptFilename)...)
	changes = append(changes, diffFlag(before.UpdateProbe, after.UpdateProbe,
		"adds "+model.CheckUpdateScriptFilename, "drops "+model.CheckUpdateScriptFilename)...)
	changes = append(changes, diffList("install command", before.Spec.InstallCommands, after.Spec.InstallCommands)...)
	changes = append(changes, diffCount("root install command", before.Spec.InstallCommandsAsRoot, after.Spec.InstallCommandsAsRoot)...)
	changes = append(changes, diffFlag(before.IgnoredGoDir, after.IgnoredGoDir,
		"adds ignored go/ handlers (require recompilation)", "drops go/ handlers")...)
	changes = append(changes, diffScalar("entrypoint override", before.Spec.EntrypointOverride, after.Spec.EntrypointOverride)...)
	changes = append(changes, diffScalar("continue args", before.Spec.ContinueArgs, after.Spec.ContinueArgs)...)
	changes = append(changes, diffScalar("resume args", before.Spec.ResumeArgs, after.Spec.ResumeArgs)...)
	changes = append(changes, diffScalar("post-start IDE launch", before.Spec.PostStartOpenIDE, after.Spec.PostStartOpenIDE)...)
	changes = append(changes, diffList("startup script", before.StartupScripts, after.StartupScripts)...)
	changes = append(changes, diffList("startup command", before.Spec.StartupCommands, after.Spec.StartupCommands)...)
	changes = append(changes, diffList("environment var", before.Spec.EnvironmentVars, after.Spec.EnvironmentVars)...)
	changes = append(changes, diffList("allowlist domain", before.AllowDomains, after.AllowDomains)...)
	changes = append(changes, diffList("allowlist directive", before.AllowlistDirectives, after.AllowlistDirectives)...)
	changes = append(changes, diffList("denied domain", before.Spec.DeniedDomains, after.Spec.DeniedDomains)...)
	changes = append(changes, diffList("port", before.allPorts(), after.allPorts())...)
	changes = append(changes, diffList("credential", before.Spec.CredentialEnv, after.Spec.CredentialEnv)...)
	changes = append(changes, diffList("credential grant",
		mapDescribe(before.Spec.CredentialSources, describeCredentialSource),
		mapDescribe(after.Spec.CredentialSources, describeCredentialSource))...)
	changes = append(changes, diffList("proxy-managed alias", before.Spec.ProxyManaged, after.Spec.ProxyManaged)...)
	changes = append(changes, diffList("provider",
		mapDescribe(before.Spec.Providers, describeProvider),
		mapDescribe(after.Spec.Providers, describeProvider))...)
	beforeHosts, afterHosts := before.Spec.HostExposure, after.Spec.HostExposure
	changes = append(changes, diffList("passthrough path", beforeHosts.PassthroughPaths, afterHosts.PassthroughPaths)...)
	changes = append(changes, diffScalar("host config dir", beforeHosts.HostConfigDir, afterHosts.HostConfigDir)...)
	changes = append(changes, diffScalar("host credentials file", beforeHosts.HostCredentialsFile, afterHosts.HostCredentialsFile)...)
	changes = append(changes, diffScalar("host oauth json", beforeHosts.HostOAuthJSON, afterHosts.HostOAuthJSON)...)
	changes = append(changes, diffScalar("mixin config dir", beforeHosts.MixinConfigDir, afterHosts.MixinConfigDir)...)
	changes = append(changes, diffList("mixin auth file", beforeHosts.MixinAuthFiles, afterHosts.MixinAuthFiles)...)
	changes = append(changes, diffList("apt package", before.Spec.AptPackages, after.Spec.AptPackages)...)
	changes = append(changes, diffList("skill", before.Skills, after.Skills)...)
	changes = append(changes, diffList("init file",
		mapDescribe(before.Spec.InitFiles, describeInitFile),
		mapDescribe(after.Spec.InitFiles, describeInitFile))...)
	changes = append(changes, diffList("workspace file", before.WorkspaceFiles, after.WorkspaceFiles)...)
	changes = append(changes, diffList("home file", before.HomeFiles, after.HomeFiles)...)
	if before.Files != after.Files || before.Bytes != after.Bytes {
		changes = append(changes, fmt.Sprintf("staged content changes from %d files/%d bytes to %d files/%d bytes",
			before.Files, before.Bytes, after.Files, after.Bytes))
	}
	return changes
}

// diffFlag reports a gained or lost boolean capability, phrased by the caller
// because "adds"/"drops" reads wrong for some of them.
func diffFlag(before bool, after bool, added string, dropped string) []string {
	switch {
	case after && !before:
		return []string{added}
	case before && !after:
		return []string{dropped}
	default:
		return nil
	}
}

// diffScalar reports a change to a single string-valued field: a newly set
// value, a cleared one, or a change from one value to another.
func diffScalar(label string, before string, after string) []string {
	if before == after {
		return nil
	}
	switch {
	case before == "":
		return []string{fmt.Sprintf("adds %s %q", label, after)}
	case after == "":
		return []string{fmt.Sprintf("drops %s %q", label, before)}
	default:
		return []string{fmt.Sprintf("changes %s from %q to %q", label, before, after)}
	}
}

func diffCount(label string, before int, after int) []string {
	switch {
	case after > before:
		return []string{fmt.Sprintf("adds %d %s(s) (%d -> %d)", after-before, label, before, after)}
	case after < before:
		return []string{fmt.Sprintf("drops %d %s(s) (%d -> %d)", before-after, label, before, after)}
	default:
		return nil
	}
}

func diffList(label string, before []string, after []string) []string {
	var changes []string
	for _, value := range dedupeSorted(after) {
		if !slices.Contains(before, value) {
			changes = append(changes, fmt.Sprintf("adds %s %s", label, value))
		}
	}
	for _, value := range dedupeSorted(before) {
		if !slices.Contains(after, value) {
			changes = append(changes, fmt.Sprintf("drops %s %s", label, value))
		}
	}
	return changes
}
