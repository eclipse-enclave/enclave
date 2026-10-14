// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package protectedauth describes how each built-in tool runs under
// --protect-auth: which gateway adapters capture its logins and which native
// credential files the session view replaces. The host and the gateway both
// read it, so a tool name selects the same behavior on either side.
package protectedauth

import (
	"slices"
	"strings"
)

// Gateway adapters that capture in-session logins.
const (
	AdapterAnthropic = "anthropic"
	AdapterOpenAI    = "openai"
	AdapterMistral   = "mistral"
	AdapterCopilot   = "copilot"
)

var grantFiles = map[string]string{
	AdapterAnthropic: "oauth-grant.json",
	AdapterOpenAI:    "openai-grant.json",
	AdapterMistral:   "mistral-grant.json",
	AdapterCopilot:   "copilot-grant.json",
}

// GrantFile names the durable grant an adapter keeps in a protected identity.
func GrantFile(adapter string) string {
	return grantFiles[adapter]
}

// GrantFiles names every adapter's grant, for resetting an identity.
func GrantFiles() []string {
	names := make([]string, 0, len(grantFiles))
	for _, name := range grantFiles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// InterruptedWritePrefix starts the temporary files of the gateway's durable
// writes, which an interrupted write can leave behind with credentials.
const InterruptedWritePrefix = ".oauth-"

// NativeFile is a credential file the tool reads from its config directory.
// The session links ConfigPath to ViewName in the handle view.
type NativeFile struct {
	ConfigPath string
	ViewName   string
}

// Tool is the protected-auth description of one built-in tool.
type Tool struct {
	Name string
	// ConfigDir is the home-relative config directory the description
	// assumes; a same-named extension with another layout is refused.
	ConfigDir string
	Adapters  []string
	// Providers maps the tool's spec providers to the adapters that
	// capture their logins.
	Providers map[string]string
	Files     []NativeFile
	// StorageDirEnv names a variable that points the tool's credential
	// storage at the view directly, instead of a linked file.
	StorageDirEnv string
	// LocationEnv names variables that would move the tool's credential
	// files away from the view.
	LocationEnv []string
}

var tools = []Tool{
	{
		Name:          "claude",
		ConfigDir:     ".claude",
		Adapters:      []string{AdapterAnthropic},
		Providers:     map[string]string{"anthropic": AdapterAnthropic},
		StorageDirEnv: "CLAUDE_SECURESTORAGE_CONFIG_DIR",
		LocationEnv:   []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"},
	},
	{
		Name:        "codex",
		ConfigDir:   ".codex",
		Adapters:    []string{AdapterOpenAI},
		Providers:   map[string]string{"openai-codex": AdapterOpenAI},
		Files:       []NativeFile{{ConfigPath: "auth.json", ViewName: "auth.json"}},
		LocationEnv: []string{"CODEX_HOME"},
	},
	{
		Name:        "opencode",
		ConfigDir:   ".config/opencode",
		Adapters:    []string{AdapterOpenAI, AdapterCopilot},
		Providers:   map[string]string{"openai": AdapterOpenAI, "github-copilot": AdapterCopilot},
		Files:       []NativeFile{{ConfigPath: "auth.json", ViewName: "auth.json"}},
		LocationEnv: []string{"OPENCODE_AUTH_CONTENT"},
	},
	{
		Name:        "pi",
		ConfigDir:   ".pi",
		Adapters:    []string{AdapterAnthropic, AdapterOpenAI},
		Providers:   map[string]string{"anthropic": AdapterAnthropic, "openai-codex": AdapterOpenAI},
		Files:       []NativeFile{{ConfigPath: "agent/auth.json", ViewName: "auth.json"}},
		LocationEnv: []string{"PI_CODING_AGENT_DIR"},
	},
	{
		Name:        "mistral-vibe",
		ConfigDir:   ".vibe",
		Adapters:    []string{AdapterMistral},
		Providers:   map[string]string{"mistral": AdapterMistral},
		Files:       []NativeFile{{ConfigPath: ".env", ViewName: "vibe.env"}},
		LocationEnv: []string{"VIBE_HOME"},
	},
	{Name: "theia", ConfigDir: ".theia-ide"},
	{Name: "theia-next", ConfigDir: ".theia-ide-next"},
}

// Lookup returns the protected-auth description of a built-in tool. Tools
// without one cannot run protected.
func Lookup(name string) (Tool, bool) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

// Tools returns every tool that can run protected.
func Tools() []Tool {
	return slices.Clone(tools)
}

// LinkSpec encodes the native files for the entrypoint, which links each
// config path to its view file.
func (t Tool) LinkSpec() string {
	links := make([]string, 0, len(t.Files))
	for _, file := range t.Files {
		links = append(links, file.ConfigPath+":"+file.ViewName)
	}
	return strings.Join(links, ",")
}

// Uses reports whether the tool captures logins with adapter.
func (t Tool) Uses(adapter string) bool {
	return slices.Contains(t.Adapters, adapter)
}
