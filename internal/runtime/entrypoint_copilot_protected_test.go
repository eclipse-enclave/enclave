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
	"testing"
)

func TestProtectedCopilotPlaintextConsentIsScoped(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("Copilot setup needs jq")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "extensions", "tools", "copilot", "entrypoint.d", "setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, mode, initial string
		wantError           bool
	}{
		{"ordinary", "0", `{"storeTokenPlaintext":false,"model":"auto"}`, false},
		{"ordinary without settings", "0", "", false},
		{"protected", "1", `{"storeTokenPlaintext":false,"model":"auto"}`, false},
		{"native headers", "1", "// native header\n{\"model\":\"auto\"}\n", false},
		{"new protected settings", "1", "", false},
		{"malformed settings", "1", "{invalid", true},
		{"non-object settings", "1", "[]", true},
		{"multiple documents", "1", "{}\n{}", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			settings := filepath.Join(home, ".copilot", "settings.json")
			if test.initial != "" {
				writeFile(t, settings, test.initial)
			}
			command := exec.Command("bash", "-c", string(script))
			command.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "ENCLAVE_YOLO=0", "ENCLAVE_PROTECTED_AUTH=" + test.mode}
			output, err := command.CombinedOutput()
			if (err != nil) != test.wantError {
				t.Fatalf("setup error=%v wantError=%v: %s", err, test.wantError, output)
			}
			data, readErr := os.ReadFile(settings)
			if test.mode == "0" && test.initial == "" {
				if !os.IsNotExist(readErr) {
					t.Fatal("ordinary setup created plaintext consent")
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			if test.wantError || test.mode == "0" {
				if string(data) != test.initial {
					t.Fatal("setup replaced ordinary or invalid settings")
				}
			} else {
				var parsed map[string]any
				if json.Unmarshal(data, &parsed) != nil || parsed["storeTokenPlaintext"] != true || (test.initial != "" && parsed["model"] != "auto") {
					t.Fatalf("protected settings = %s", data)
				}
			}
			leftovers, err := filepath.Glob(filepath.Join(home, ".copilot", ".protected-settings.*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("temporary settings survived: %v, %v", leftovers, err)
			}
		})
	}
}
