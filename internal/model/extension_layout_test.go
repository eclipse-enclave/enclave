// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// TestExtensionLayoutMatchesShell pins the extension layout constants to the
// shell that actually consumes the directories. A rename on either side is a
// silent capability-summary gap otherwise: the scanner in internal/extinstall
// would stop recognizing a directory the container still runs.
func TestExtensionLayoutMatchesShell(t *testing.T) {
	entrypoint := readRepoFile(t, "entrypoint.sh")
	kitInit := readRepoFile(t, "runtime-assets/kit-init.sh")

	for _, want := range []struct {
		name    string
		needle  string
		sources map[string]string
	}{
		{"ToolEntrypointDir", "/" + ToolEntrypointDir, map[string]string{"entrypoint.sh": entrypoint}},
		{"FeatureEntrypointDir", FeatureEntrypointDir, map[string]string{"entrypoint.sh": entrypoint}},
		{
			"ExtensionFilesWorkspaceDir",
			ExtensionFilesDir + "/" + ExtensionFilesWorkspaceDir,
			map[string]string{"runtime-assets/kit-init.sh": kitInit},
		},
	} {
		for source, content := range want.sources {
			if !strings.Contains(content, want.needle) {
				t.Errorf("%s = %q, but %s does not mention %q", want.name, want.needle, source, want.needle)
			}
		}
	}
}

// TestKitInitSubstitutedVarsMatchesShell pins the envsubst whitelist to the one
// kit-init.sh passes. writesIntoProject reasons about which variables expand, so
// a whitelist change that is not mirrored here makes the capability summary
// claim the wrong destination for an initFiles path.
func TestKitInitSubstitutedVarsMatchesShell(t *testing.T) {
	kitInit := readRepoFile(t, "runtime-assets/kit-init.sh")

	// The single-quoted shell-format argument is the whitelist; kit-init.sh
	// passes the same one at every call site.
	pattern := regexp.MustCompile(`envsubst '([^']*)'`)
	matches := pattern.FindAllStringSubmatch(kitInit, -1)
	if len(matches) == 0 {
		t.Fatal("no envsubst whitelist found in runtime-assets/kit-init.sh")
	}

	var want []string
	for _, v := range KitInitSubstitutedVars {
		want = append(want, fmt.Sprintf("${%s}", v))
	}
	expected := strings.Join(want, " ")

	for i, match := range matches {
		if match[1] != expected {
			t.Errorf("envsubst whitelist %d in kit-init.sh is %q, KitInitSubstitutedVars says %q",
				i, match[1], expected)
		}
	}
}
