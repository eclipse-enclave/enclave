// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/prompt"
)

// Seams for tool resolution, replaced in tests.
var (
	listAgentTools = hostAgentTools
	saveToolChoice = config.WriteGlobalDefault
	chooseTool     = func(question string, options []string) (string, error) {
		return prompt.Choose(question, options, os.Stdin, os.Stderr)
	}
)

// toolUnset reports whether the tool still carries the "ask me" value.
func toolUnset(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed == "" || trimmed == model.ToolAuto
}

// resolveTool turns the unset "auto" tool into a concrete profile name. A
// configured value or an explicit --tool is returned unchanged. interactive
// permits the one-time question, whose answer is saved to the global config so
// only the first run pays for it. Without a terminal, or when the question goes
// unanswered, the run fails and names the ways to configure a tool instead of
// guessing one.
func resolveTool(tool string, interactive bool) (string, error) {
	if !toolUnset(tool) {
		return tool, nil
	}
	configPath, err := config.GlobalConfigPath()
	if err != nil {
		configPath = "the global config"
	}
	tools := listAgentTools()
	if len(tools) == 0 || !interactive || !promptUsable() {
		return "", noToolConfigured(configPath, tools)
	}
	question := fmt.Sprintf("Which coding agent should enclave use? The answer is saved to %s; --tool overrides it for a single run.", configPath)
	choice, err := chooseTool(question, tools)
	if err != nil || choice == "" {
		return "", fmt.Errorf("no tool chosen; pass --tool <name> or set \"tool\" in %s", configPath)
	}
	if _, err := saveToolChoice("tool", choice); err != nil {
		logx.Warnf("Using %s for this run, but the choice could not be saved: %v", choice, err)
	}
	return choice, nil
}

// noToolConfigured is the error for a command that needs a tool, has none
// configured, and cannot ask for one.
func noToolConfigured(configPath string, installed []string) error {
	msg := fmt.Sprintf("no tool configured; pass --tool <name> or set \"tool\" in %s", configPath)
	if len(installed) > 0 {
		msg += " (installed agents: " + strings.Join(installed, ", ") + ")"
	}
	return errors.New(msg)
}

// hostAgentTools lists the installed agent profiles the question may offer. Any
// failure to enumerate them leaves the list empty and the caller fails as if
// none were installed.
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
