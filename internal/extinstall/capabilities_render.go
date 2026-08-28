// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"
	"io"
	"strings"

	"enclave/internal/model"
)

// render writes the capability summary shown before an install is confirmed.
// Lines with nothing to report are omitted. The caller has already opened the
// section, so this renders the body alone.
func (c capabilities) render(w io.Writer, style Style, source string) {
	rows := &rowSet{}
	row := rows.add
	row("source", source)

	install := ""
	switch {
	case c.InstallScript && c.Spec.NeedsRoot:
		install = model.InstallScriptFilename + " (needsRoot: true)"
	case c.InstallScript:
		install = model.InstallScriptFilename
	case c.Spec.InstallCommands > 0:
		install = fmt.Sprintf("commands.install (%d)", c.Spec.InstallCommands)
		if c.Spec.InstallCommandsAsRoot > 0 {
			install = fmt.Sprintf("commands.install (%d, %d as root)", c.Spec.InstallCommands, c.Spec.InstallCommandsAsRoot)
		}
		if c.Spec.NeedsRoot {
			install += " (needsRoot: true)"
		}
	case c.Spec.NeedsRoot:
		install = "needsRoot: true"
	}
	row("install script", install)
	updateProbe := ""
	if c.UpdateProbe {
		updateProbe = model.CheckUpdateScriptFilename + " (runs in a container on each automatic update check)"
	}
	row("update probe", updateProbe)
	row("entrypoint override", c.Spec.EntrypointOverride)
	skipsApproval := ""
	if c.yoloActive() {
		skipsApproval = fmt.Sprintf("%s (agent executes actions on its own, without asking you to approve each one)", c.Spec.YoloFlag)
	}
	row("skips approval", skipsApproval)

	startup := append([]string{}, c.StartupScripts...)
	if len(c.Spec.StartupCommands) > 0 {
		startup = append(startup, fmt.Sprintf("commands.startup (%d)", len(c.Spec.StartupCommands)))
	}
	row("startup scripts", strings.Join(startup, ", "))
	row("environment vars", strings.Join(c.Spec.EnvironmentVars, ", "))
	row("allowlist domains", joinWithPlus(c.AllowDomains))
	row("allowlist directives", strings.Join(c.AllowlistDirectives, ", "))
	row("denied domains", strings.Join(c.Spec.DeniedDomains, ", "))
	row("ports", strings.Join(c.allPorts(), ", "))
	envAliases := strings.Join(c.Spec.CredentialEnv, ", ")
	if envAliases != "" {
		envAliases += " (env alias)"
	}
	row("credentials", envAliases)
	for _, source := range c.Spec.CredentialSources {
		if source.ReleaseHeader == "" {
			continue
		}
		row("credential → host", fmt.Sprintf("%s -> %s (header %s)", source.ID, strings.Join(source.ReleaseHosts, ", "), source.ReleaseHeader))
	}
	for _, source := range c.Spec.CredentialSources {
		if source.FilePath == "" {
			continue
		}
		note := fmt.Sprintf("%s: %s", source.ID, source.FilePath)
		if source.FileParser != "" {
			note += " (" + source.FileParser + ")"
		}
		row("credential file", note)
	}
	row("proxy-managed", strings.Join(c.Spec.ProxyManaged, ", "))
	for _, provider := range c.Spec.Providers {
		row("provider", describeProvider(provider))
	}
	row("passthrough paths", strings.Join(c.Spec.HostExposure.PassthroughPaths, ", "))
	row("host config dir", c.Spec.HostExposure.HostConfigDir)
	row("host credentials file", c.Spec.HostExposure.HostCredentialsFile)
	row("host oauth json", c.Spec.HostExposure.HostOAuthJSON)
	row("mixin config dir", c.Spec.HostExposure.MixinConfigDir)
	row("mixin auth files", strings.Join(c.Spec.HostExposure.MixinAuthFiles, ", "))
	row("apt packages", strings.Join(c.Spec.AptPackages, ", "))
	row("skills", strings.Join(c.Skills, ", "))
	for _, file := range c.Spec.InitFiles {
		note := file.Path
		switch {
		case writesIntoProject(file.Path) && !file.OnlyIfMissing:
			note += " (overwrites in the project on every start)"
		case writesIntoProject(file.Path):
			note += " (seeded once in the project)"
		}
		row("init files", note)
	}
	workspaceFiles := strings.Join(c.WorkspaceFiles, ", ")
	if workspaceFiles != "" {
		workspaceFiles += " (seeded into the project at start, never overwrites)"
	}
	row("workspace files", workspaceFiles)
	homeFiles := strings.Join(c.HomeFiles, ", ")
	if homeFiles != "" {
		homeFiles += " (baked into $HOME at build)"
	}
	row("home files", homeFiles)
	if c.ShadowsBuiltin {
		row("shadows built-in", "yes")
	}
	if c.IgnoredGoDir {
		row("ignored", "go/ handlers (require recompilation)")
	}
	if c.Files > 0 {
		row("content", fmt.Sprintf("%d files, %d bytes", c.Files, c.Bytes))
	}
	rows.render(w, style)
	if !rows.empty() {
		_, _ = fmt.Fprintln(w)
	}
}
