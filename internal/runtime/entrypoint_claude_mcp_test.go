// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSetupRemovesOnlyEnclavePlaywrightMCP(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is unavailable")
	}
	enclaveServer := func() map[string]any {
		return map[string]any{
			"type":    "stdio",
			"command": "/opt/enclave/node/bin/npx",
			"args":    []any{"@playwright/mcp", "--headless", "--no-sandbox", "--browser", "chromium"},
			"env":     map[string]any{"PLAYWRIGHT_BROWSERS_PATH": "/opt/playwright-browsers"},
		}
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "extensions", "tools", "claude", "entrypoint.d", "setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	withoutType := enclaveServer()
	delete(withoutType, "type")
	extraArg := enclaveServer()
	extraArg["args"] = append(extraArg["args"].([]any), "--isolated")
	extraEnv := enclaveServer()
	extraEnv["env"] = map[string]any{"PLAYWRIGHT_BROWSERS_PATH": "/opt/playwright-browsers", "DEBUG": "pw:api"}

	for _, tc := range []struct {
		name       string
		server     map[string]any
		inProject  bool
		raw        string
		wantRemove bool
	}{
		{name: "enclave registration", server: enclaveServer(), inProject: true, wantRemove: true},
		{name: "enclave registration without type", server: withoutType, inProject: true, wantRemove: true},
		{name: "extra argument", server: extraArg, inProject: true},
		{name: "extra env", server: extraEnv, inProject: true},
		{name: "other project", server: enclaveServer()},
		{name: "malformed config", raw: "{not json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			project, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			config := tc.raw
			if config == "" {
				key := filepath.Join(home, "elsewhere")
				if tc.inProject {
					key = project
				}
				doc, err := json.Marshal(map[string]any{"projects": map[string]any{key: map[string]any{"mcpServers": map[string]any{"playwright": tc.server}}}})
				if err != nil {
					t.Fatal(err)
				}
				config = string(doc)
			}
			// The config store file the setup script links ~/.claude.json to.
			storePath := filepath.Join(home, ".claude", ".claude.json")
			writeFile(t, storePath, config)

			stubDir := t.TempDir()
			claudeLog := filepath.Join(t.TempDir(), "claude.log")
			writeFile(t, filepath.Join(stubDir, "claude"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CLAUDE_LOG\"\n")
			if err := os.Chmod(filepath.Join(stubDir, "claude"), 0o755); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("bash", script)
			cmd.Dir = project
			cmd.Env = []string{
				"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
				"HOME=" + home,
				"ENCLAVE_AGENT_NODE_DIR=/opt/enclave/node",
				"CLAUDE_LOG=" + claudeLog,
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("setup.sh failed: %v\n%s", err, out)
			}

			calls, err := os.ReadFile(claudeLog)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			removed := strings.Contains(string(calls), "mcp remove -s local playwright")
			if removed != tc.wantRemove {
				t.Fatalf("removed = %v, want %v (claude calls: %q)", removed, tc.wantRemove, calls)
			}
			// Removal goes through claude; the script never rewrites the store.
			assertFileContent(t, storePath, config)
		})
	}
}
