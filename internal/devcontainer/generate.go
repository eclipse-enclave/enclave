// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package devcontainer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"enclave/internal/model"
)

// GenerateConfig holds the inputs for generating a devcontainer.json file.
type GenerateConfig struct {
	Image         string
	Tool          string
	ProjectDir    string
	SecretEnvVars []string
	Force         bool
}

// devcontainerJSON is the structure written to .devcontainer/devcontainer.json.
type devcontainerJSON struct {
	Name            string            `json:"name"`
	Image           string            `json:"image"`
	WorkspaceFolder string            `json:"workspaceFolder"`
	WorkspaceMount  string            `json:"workspaceMount"`
	RemoteUser      string            `json:"remoteUser"`
	ContainerEnv    map[string]string `json:"containerEnv"`
}

// Generate creates a .devcontainer/devcontainer.json in the given project directory.
// It returns the path to the generated file.
func Generate(cfg GenerateConfig) (string, error) {
	dir := filepath.Join(cfg.ProjectDir, model.DevcontainerDir)
	outPath := filepath.Join(dir, model.DevcontainerFilename)

	if !cfg.Force {
		if _, err := os.Stat(outPath); err == nil {
			return "", fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
		}
	}

	env := map[string]string{
		"TOOL":        cfg.Tool,
		"PROJECT_DIR": cfg.ProjectDir,
	}
	for _, v := range cfg.SecretEnvVars {
		env[v] = "${localEnv:" + v + "}"
	}

	doc := devcontainerJSON{
		Name:            model.AppName + "-" + cfg.Tool,
		Image:           cfg.Image,
		WorkspaceFolder: cfg.ProjectDir,
		WorkspaceMount:  fmt.Sprintf("source=%s,target=%s,type=bind", cfg.ProjectDir, cfg.ProjectDir),
		RemoteUser:      model.ContainerUser,
		ContainerEnv:    env,
	}

	data, err := marshalSorted(doc)
	if err != nil {
		return "", fmt.Errorf("failed to marshal devcontainer.json: %w", err)
	}

	// #nosec G301
	// Devcontainer directory is intentionally traversable by tooling.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", dir, err)
	}

	// #nosec G306
	// Generated project config should be readable/editable by standard tools.
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", outPath, err)
	}

	return outPath, nil
}

// marshalSorted produces JSON with containerEnv keys in sorted order.
func marshalSorted(doc devcontainerJSON) ([]byte, error) {
	// Use an ordered representation to get stable key order in containerEnv.
	type orderedEntry struct {
		Key   string
		Value string
	}
	entries := make([]orderedEntry, 0, len(doc.ContainerEnv))
	for k, v := range doc.ContainerEnv {
		entries = append(entries, orderedEntry{k, v})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })

	envMap := make([]byte, 0, 256)
	envMap = append(envMap, '{')
	for i, e := range entries {
		if i > 0 {
			envMap = append(envMap, ',')
		}
		k, _ := json.Marshal(e.Key)
		v, _ := json.Marshal(e.Value)
		envMap = append(envMap, k...)
		envMap = append(envMap, ':')
		envMap = append(envMap, v...)
	}
	envMap = append(envMap, '}')
	orderedEnv := json.RawMessage(envMap)

	// Build the top-level object with ordered keys using json.RawMessage for env.
	type docWithRawEnv struct {
		Name            string          `json:"name"`
		Image           string          `json:"image"`
		WorkspaceFolder string          `json:"workspaceFolder"`
		WorkspaceMount  string          `json:"workspaceMount"`
		RemoteUser      string          `json:"remoteUser"`
		ContainerEnv    json.RawMessage `json:"containerEnv"`
	}

	return json.MarshalIndent(docWithRawEnv{
		Name:            doc.Name,
		Image:           doc.Image,
		WorkspaceFolder: doc.WorkspaceFolder,
		WorkspaceMount:  doc.WorkspaceMount,
		RemoteUser:      doc.RemoteUser,
		ContainerEnv:    orderedEnv,
	}, "", "  ")
}
