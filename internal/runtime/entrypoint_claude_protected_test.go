// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectedClaudeEntrypointNeverFallsBackToOrdinaryClient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client string
		mode   os.FileMode
	}{
		{name: "missing"},
		{name: "not executable", client: "echo '2.1.63 (Claude Code)'", mode: 0o600},
		{name: "wrong version", client: "echo '2.1.64 (Claude Code)'", mode: 0o700},
		{name: "version failure", client: "exit 1", mode: 0o700},
		{name: "version failure with matching output", client: "echo '2.1.63 (Claude Code)'; exit 1", mode: 0o700},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, ordinary, setup := claudeEntrypointFixture(t)
			if tc.client != "" {
				writeClaudeEntrypointClient(t, home, tc.client, tc.mode)
			}
			cmd := exec.Command("bash", "-c", `. "$1"; echo protected-setup-complete`, "bash", setup)
			cmd.Env = []string{"HOME=" + home, "PATH=" + ordinary + ":" + os.Getenv("PATH"), "ENCLAVE_PROTECTED_AUTH=1"}
			output, err := cmd.CombinedOutput()
			if err == nil || strings.Contains(string(output), "protected-setup-complete") {
				t.Fatalf("invalid pinned client started: error=%v output=%s", err, output)
			}
			if _, err := os.Stat(filepath.Join(home, "ordinary-client-used")); !os.IsNotExist(err) {
				t.Fatal("fell back to the ordinary client")
			}
		})
	}
}

func TestProtectedClaudeEntrypointSelectsPinAndDisablesUpdates(t *testing.T) {
	home, ordinary, setup := claudeEntrypointFixture(t)
	pinned := writeClaudeEntrypointClient(t, home, `if [ "$1" = "--version" ]; then echo '2.1.63 (Claude Code)'; fi`, 0o700)
	cmd := exec.Command("bash", "-c", `. "$1"; command -v claude; printf 'updates=%s\n' "$DISABLE_AUTOUPDATER"`, "bash", setup)
	cmd.Env = []string{"HOME=" + home, "PATH=" + ordinary + ":" + os.Getenv("PATH"), "ENCLAVE_PROTECTED_AUTH=1", "DISABLE_AUTOUPDATER=0"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("protected setup: %v\n%s", err, output)
	}
	if string(output) != pinned+"\nupdates=1\n" {
		t.Fatalf("incorrect protected client setup: %s", output)
	}
	if _, err := os.Stat(filepath.Join(home, "ordinary-client-used")); !os.IsNotExist(err) {
		t.Fatal("ordinary client was invoked during protected setup")
	}
}

func TestOrdinaryClaudeEntrypointDoesNotRequirePin(t *testing.T) {
	home, ordinary, setup := claudeEntrypointFixture(t)
	cmd := exec.Command("bash", "-c", `. "$1"; command -v claude; printf 'updates=%s\n' "$DISABLE_AUTOUPDATER"`, "bash", setup)
	cmd.Env = []string{"HOME=" + home, "PATH=" + ordinary + ":" + os.Getenv("PATH"), "DISABLE_AUTOUPDATER=0"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ordinary setup: %v\n%s", err, output)
	}
	if string(output) != filepath.Join(ordinary, "claude")+"\nupdates=0\n" {
		t.Fatalf("ordinary installation changed: %s", output)
	}
}

func claudeEntrypointFixture(t *testing.T) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	ordinary := filepath.Join(home, "ordinary-bin")
	if err := os.MkdirAll(ordinary, 0o700); err != nil {
		t.Fatal(err)
	}
	client := "#!/bin/sh\ntouch \"$HOME/ordinary-client-used\"\nif [ \"$1\" = \"--version\" ]; then echo '2.1.63 (Claude Code)'; fi\n"
	if err := os.WriteFile(filepath.Join(ordinary, "claude"), []byte(client), 0o700); err != nil {
		t.Fatal(err)
	}
	setup := filepath.Join("..", "..", "extensions", "tools", "claude", "entrypoint.d", "setup.sh")
	return home, ordinary, setup
}

func writeClaudeEntrypointClient(t *testing.T, home, script string, mode os.FileMode) string {
	t.Helper()
	client := filepath.Join(home, ".local", "share", "enclave", "claude-2.1.63", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(client), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(client, []byte("#!/bin/sh\n"+script+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	return client
}
