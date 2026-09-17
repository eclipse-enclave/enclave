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
	"strings"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/prompt"
)

// toolFallback is the tool used whenever the question cannot be asked:
// scripts, --json, --yes and non-terminal runs behave exactly as they did
// before the first-run question existed.
const toolFallback = "claude"

// Seams for tool resolution, replaced in tests.
var (
	listAgentTools = hostAgentTools
	saveToolChoice = config.WriteGlobalDefault
	chooseTool     = func(question string, options []string) (string, error) {
		return prompt.Choose(question, options, os.Stdin, os.Stderr)
	}
)

// actionAsksForTool reports whether an invocation is worth interrupting with
// the tool question. Only the verbs that start a session or build its image
// commit to a tool; listing, status, policy and extension verbs read it at
// most as a filter and must not block on an answer. `update` with explicit
// targets rebuilds exactly those images and never reads the default tool, so
// it must not ask for one either.
func actionAsksForTool(parsed cli.Result) bool {
	switch parsed.Action {
	case "exec":
		return true
	case "update":
		return len(parsed.Options.UpdateTools) == 0
	default:
		return isRunAction(parsed.Action)
	}
}

// toolPromptAllowed reports whether resolving an unset tool may ask the user.
func toolPromptAllowed(parsed cli.Result) bool {
	return actionAsksForTool(parsed) && promptAllowed(parsed)
}

// toolUnset reports whether the tool still carries the "ask me" value.
func toolUnset(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed == "" || trimmed == model.ToolAuto
}

// resolveTool turns the unset "auto" tool into a concrete profile name. A
// configured value or an explicit --tool is returned unchanged. interactive
// permits the one-time question; without it the historical claude default is
// kept, so scripts, CI and JSON consumers are unaffected.
func resolveTool(tool string, interactive bool) string {
	if !toolUnset(tool) {
		return tool
	}
	if !interactive || !promptUsable() {
		return toolWithoutAsking()
	}
	return askForTool()
}

// toolWithoutAsking picks a tool for the runs that must not stop for a
// question: the historical claude default, except on a host that installed
// exactly one agent, where an interactive run would not ask either and claude
// may not even be installed.
func toolWithoutAsking() string {
	if tools := listAgentTools(); len(tools) == 1 {
		logx.Debugf("no tool configured; using the only installed agent %s without asking", tools[0])
		return tools[0]
	}
	logx.Debugf("no tool configured; using %s without asking", toolFallback)
	return toolFallback
}

// askForTool asks once which agent to run and saves the answer to the global
// config, so only the first run pays for the question. An unanswered question
// falls back for this run without saving anything.
func askForTool() string {
	tools := listAgentTools()
	switch len(tools) {
	case 0:
		return toolFallback
	case 1:
		return tools[0]
	}
	configPath, err := config.GlobalConfigPath()
	if err != nil {
		configPath = "the global config"
	}
	question := fmt.Sprintf("Which coding agent should enclave use? The answer is saved to %s; --tool overrides it for a single run.", configPath)
	choice, err := chooseTool(question, tools)
	if err != nil || choice == "" {
		logx.Warnf("No tool chosen; using %s for this run.", toolFallback)
		return toolFallback
	}
	if _, err := saveToolChoice("tool", choice); err != nil {
		logx.Warnf("Using %s for this run, but the choice could not be saved: %v", choice, err)
	}
	return choice
}

// hostAgentTools lists the installed agent profiles a first run may offer. Any
// failure to enumerate them leaves the list empty and the caller falls back
// without asking.
func hostAgentTools() []string {
	paths, err := config.ResolvePaths()
	if err != nil {
		logx.Debugf("tool question: resolve paths: %v", err)
		return nil
	}
	names, err := config.ListProfiles(paths)
	if err != nil {
		logx.Debugf("tool question: list profiles: %v", err)
		return nil
	}
	profiles := make([]model.Profile, 0, len(names))
	for _, name := range names {
		profile, err := config.LoadProfile(paths, name)
		if err != nil {
			logx.Debugf("tool question: load profile %s: %v", name, err)
			continue
		}
		profiles = append(profiles, profile)
	}
	return agentToolNames(profiles)
}

// agentToolNames drops the IDE profiles. IDE profiles (postStart.openIDE)
// attach a host IDE to a container instead of running an agent in the
// terminal, which is not what this question is about.
func agentToolNames(profiles []model.Profile) []string {
	names := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if profile.PostStart != nil && strings.TrimSpace(profile.PostStart.OpenIDE) != "" {
			continue
		}
		names = append(names, profile.Name)
	}
	return names
}
