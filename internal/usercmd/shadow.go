// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package usercmd

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"enclave/internal/config"
)

// BuiltinNames are the top-level verbs enclave defines itself. A discovered
// command of the same name is dropped when internal/cli registers commands,
// and TestBuiltinNamesMatchRootCommand keeps this list in step with what it
// registers.
var BuiltinNames = []string{
	"__complete",
	"__completeNoDesc",
	"attach",
	"auth",
	"cleanup",
	"completion",
	"config",
	"continue",
	"devcontainer",
	"exec",
	"extension",
	"features",
	"help",
	"img",
	"info",
	"network",
	"ps",
	"resume",
	"review-target",
	"run",
	"shell",
	"ssh-init",
	"status",
	"stop",
	"theia",
	"theia-next",
	"tools",
	"update",
	"validate-extensions",
	"version",
}

// Shadowed maps each of an extension's command names that cannot become a
// verb to what takes it instead: a built-in, or a command in the user's own
// commands/ tree. Names two extensions share are left out, since which one
// wins depends on the full set installed and Discover reports it on every run.
func Shadowed(home string, names []string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	own := map[string]string{}
	// Session first, so a name in both trees reports the host command that
	// Discover keeps.
	for _, target := range []Target{TargetSession, TargetHost} {
		dir := config.HostCommandsSessionDir(home)
		if target == TargetHost {
			dir = config.HostCommandsHostDir(home)
		}
		cmds, _, _ := readDir(dir, target, sourceUser)
		for _, c := range cmds {
			own[c.Name] = c.Path
		}
	}
	shadowed := map[string]string{}
	for _, name := range names {
		switch path, ok := own[name]; {
		case slices.Contains(BuiltinNames, name):
			shadowed[name] = "a built-in command"
		case ok:
			shadowed[name] = fmt.Sprintf("your user command %s", path)
		}
	}
	return shadowed
}

// Resolving lists the commands Discover resolves to files under extDir, which
// is what an installed extension's verbs actually are once built-ins, the
// user's own commands, and other extensions have had their turn.
func Resolving(home string, extDir string) []string {
	cmds, _ := Discover(home)
	prefix := ExtensionHostDir(extDir) + string(os.PathSeparator)
	var names []string
	for _, c := range cmds {
		if strings.HasPrefix(c.Path, prefix) && !slices.Contains(BuiltinNames, c.Name) {
			names = append(names, c.Name)
		}
	}
	return names
}
