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

func TestEntrypointSkipsPythonVenvForReadonlyProjectMount(t *testing.T) {
	t.Parallel()
	output := runEntrypointPythonProject(t, model.ProjectMountReadonly, "exit 99")

	if !strings.Contains(output, "project mount is read-only") {
		t.Fatalf("expected readonly project mount skip message, got:\n%s", output)
	}
	if strings.Contains(output, "uv should not run") {
		t.Fatalf("expected uv not to run for readonly project mount, got:\n%s", output)
	}
}

func TestEntrypointContinuesWhenPythonVenvCreationFails(t *testing.T) {
	t.Parallel()
	output := runEntrypointPythonProject(t, model.ProjectMountWritable, "echo uv failed >&2\nexit 99")

	if !strings.Contains(output, "failed to create Python virtual environment") {
		t.Fatalf("expected venv failure warning, got:\n%s", output)
	}
}

func runEntrypointPythonProject(t *testing.T, projectMount string, uvBody string) string {
	t.Helper()

	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	fakeBin := filepath.Join(home, "bin")
	toolsDir := filepath.Join(home, "tools")
	featuresDir := filepath.Join(home, "features")
	for _, dir := range []string{projectDir, fakeBin, toolsDir, featuresDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(projectDir, "pyproject.toml"), []byte("[project]\nname = \"sample\"\n"), 0o644); err != nil {
		t.Fatalf("write pyproject.toml: %v", err)
	}
	uvPath := filepath.Join(fakeBin, "uv")
	uvScript := "#!/bin/sh\necho uv should not run >&2\n" + uvBody + "\n"
	if err := os.WriteFile(uvPath, []byte(uvScript), 0o755); err != nil {
		t.Fatalf("write uv stub: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join("..", "..", "entrypoint.sh"), "true")
	cmd.Env = []string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=claude",
		"ENCLAVE_PROJECT_MOUNT=" + projectMount,
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
		"ENCLAVE_FEATURES_DIR=" + featuresDir,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, string(out))
	}
	return string(out)
}
