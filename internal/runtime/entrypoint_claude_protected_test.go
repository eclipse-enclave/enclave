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
	"testing"
)

func TestProtectedClaudeEntrypointSeedsPersistentAccountStateOnce(t *testing.T) {
	script := filepath.Join("..", "..", "extensions", "tools", "claude", "entrypoint.d", "setup.sh")
	for _, tc := range []struct {
		name, existing, link, protected, want string
	}{
		{name: "fresh store", protected: "1", want: "seed"},
		{name: "persisted state", existing: "persisted", protected: "1", want: "persisted"},
		{name: "planted link", link: "elsewhere", protected: "1"},
		{name: "ordinary session", protected: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			view := filepath.Join(home, ".enclave-auth")
			writeFile(t, filepath.Join(view, ".claude.json"), "seed")
			target := filepath.Join(home, ".claude", ".claude.json")
			if tc.existing != "" {
				writeFile(t, target, tc.existing)
			}
			if tc.link != "" {
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, tc.link), target); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "-c", `. "$0"`, script)
			cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "ENCLAVE_PROTECTED_AUTH=" + tc.protected, "ENCLAVE_AUTH_DIR=" + view}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("setup failed: %v\n%s", err, output)
			}
			if tc.link != "" {
				if _, err := os.Stat(filepath.Join(home, tc.link)); !os.IsNotExist(err) {
					t.Fatal("seed followed a planted link")
				}
				return
			}
			data, err := os.ReadFile(target)
			if tc.want == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("ordinary session received protected seed: %q, %v", data, err)
				}
				return
			}
			if err != nil || string(data) != tc.want {
				t.Fatalf("account state = %q, %v; want %q", data, err, tc.want)
			}
		})
	}
}
