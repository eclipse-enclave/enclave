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

	"enclave/internal/model"
)

func TestProtectedGitHubEntrypointRequiresPinnedClient(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "extensions", "features", "github-cli", "feature-entrypoint.d", "setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, client, mode string
		wantError          bool
	}{
		{"ordinary needs no pin", "", "", false},
		{"missing pin", "", "1", true},
		{"wrong pin", "echo 'gh version 2.87.4 (fixture)'", "1", true},
		{"failed version", "echo 'gh version 2.87.3 (fixture)'; exit 1", "1", true},
		{"pinned", "echo 'gh version 2.87.3 (fixture)'", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			prefix := filepath.Join(home, "pin")
			client := filepath.Join(prefix, "bin", "gh")
			if tc.client != "" {
				writeFile(t, client, "#!/bin/sh\n"+tc.client+"\n")
				if err := os.Chmod(client, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(string(data), "/opt/enclave/gh-2.87.3", prefix) + `
if [ "$ENCLAVE_PROTECTED_GITHUB" = 1 ]; then
 [ "$(command -v gh)" = "$EXPECTED_GH" ] && [ "$GH_CONFIG_DIR" = "$HOME/.config/gh" ] && [ "$GH_HOST" = github.com ] || exit 1
fi
echo setup-complete
`
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "ENCLAVE_PROTECTED_GITHUB=" + tc.mode, "EXPECTED_GH=" + client}
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError || (!tc.wantError && !strings.Contains(string(output), "setup-complete")) {
				t.Fatalf("setup error=%v wantError=%v: %s", err, tc.wantError, output)
			}
		})
	}
}

func TestProtectedGitHubCommandRequiresPinWithoutImageHook(t *testing.T) {
	for _, shell := range []bool{false, true} {
		for _, version := range []string{"missing", "wrong", "pinned"} {
			name := version + "-agent"
			if shell {
				name = version + "-shell"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				prefix := filepath.Join(home, "gh-pin")
				gh := filepath.Join(prefix, "bin", "gh")
				if version != "missing" {
					output := "gh version 2.87.3 (fixture)"
					if version == "wrong" {
						output = "gh version 2.87.4 (fixture)"
					}
					writeFile(t, gh, "#!/bin/sh\necho '"+output+"'\n")
					if err := os.Chmod(gh, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				writeFile(t, filepath.Join(home, ".bashrc"), "export PATH="+filepath.Join(home, "bin")+":/usr/bin:/bin\n")
				client := filepath.Join(home, "bin", "claude")
				body := `command -v gh > "$HOME/selected-gh"`
				writeFile(t, client, "#!/bin/sh\n"+body+"\n")
				if err := os.Chmod(client, 0o700); err != nil {
					t.Fatal(err)
				}
				r := protectedTestRuntime()
				r.containerHome = home
				r.build.NoRebuild = true
				r.features = []model.Extension{{Name: "github-cli", ConfigDir: ".config/gh", AuthFiles: []string{"hosts.yml", "config.yml"}}}
				r.run.Shell = shell
				if shell {
					r.run.CmdArgs = []string{"-c", body}
				}
				argv := newCommandBuilder(r).Build()
				for i := range argv {
					argv[i] = strings.ReplaceAll(argv[i], "/opt/enclave/gh-2.87.3", prefix)
				}
				cmd := exec.Command(argv[0], argv[1:]...)
				cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
				output, err := cmd.CombinedOutput()
				if version == "pinned" {
					if err != nil {
						t.Fatalf("request-owned pin selection: %v: %s", err, output)
					}
					assertFileContent(t, filepath.Join(home, "selected-gh"), gh+"\n")
				} else {
					if err == nil {
						t.Fatal("unusable image started without protected GitHub pin")
					}
					if _, err := os.Stat(filepath.Join(home, "selected-gh")); !os.IsNotExist(err) {
						t.Fatal("workload ran before its protected feature pin was validated")
					}
				}
			})
		}
	}
}
