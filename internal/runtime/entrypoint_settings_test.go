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

	"github.com/BurntSushi/toml"
)

func TestEntrypoint_SettingsTemplateCopy_DoesNotOverwriteExistingTarget(t *testing.T) {
	template := `{"a":1}`
	existingTarget := `{"a":999}`

	targetPath, output, err := runEntrypointSettingsTemplate(t, "settings.json", template, &existingTarget)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	gotBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(gotBytes) != existingTarget {
		t.Fatalf("expected existing target content %q, got %q", existingTarget, string(gotBytes))
	}
}

func TestEntrypoint_SettingsTemplateCopy_WorksWithNestedPiTargetAndAuthDir(t *testing.T) {
	template := `{"provider":"openai-codex"}`

	targetPath, output, err := runEntrypointSettingsTemplateWithConfigAndAuth(
		t,
		"pi-settings.json",
		template,
		filepath.Join(".pi", "agent", "settings.json"),
		"agent/auth.json",
	)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	gotBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(gotBytes) != template {
		t.Fatalf("expected copied template %q, got %q", template, string(gotBytes))
	}
}

func TestEntrypoint_YoloInjectsBypassPermissions_PreservesExistingSettings(t *testing.T) {
	requireJQ(t)

	template := `{"model":"claude-sonnet-4-20250514","permissions":{"allow":["Read"]},"customKey":42}`

	targetPath, output, err := runEntrypointSettingsPatchWithToolAndEnv(t, "claude", "settings.json", template, []string{"ENCLAVE_YOLO=1"})
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	outBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(outBytes, &got); err != nil {
		t.Fatalf("parse settings: %v\ncontent:\n%s", err, string(outBytes))
	}

	// skipDangerousModePermissionPrompt must be injected
	bp, ok := got["skipDangerousModePermissionPrompt"]
	if !ok {
		t.Fatalf("skipDangerousModePermissionPrompt not found in settings:\n%s", string(outBytes))
	}
	if bp != true {
		t.Fatalf("skipDangerousModePermissionPrompt = %v, want true", bp)
	}

	// All original keys must be preserved
	if got["model"] != "claude-sonnet-4-20250514" {
		t.Fatalf("model = %v, want claude-sonnet-4-20250514", got["model"])
	}
	if got["customKey"] != float64(42) {
		t.Fatalf("customKey = %v, want 42", got["customKey"])
	}
	perms, ok := got["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions not a map: %v", got["permissions"])
	}
	allow, ok := perms["allow"].([]any)
	if !ok || len(allow) != 1 || allow[0] != "Read" {
		t.Fatalf("permissions.allow = %v, want [Read]", perms["allow"])
	}
}

func TestEntrypoint_YoloDoesNotInjectBypassPermissionsForNonClaude(t *testing.T) {
	requireJQ(t)

	template := `{"model":"claude-sonnet-4-20250514"}`

	targetPath, output, err := runEntrypointSettingsPatchWithToolAndEnv(t, "pi", "settings.json", template, []string{"ENCLAVE_YOLO=1"})
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	outBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(outBytes, &got); err != nil {
		t.Fatalf("parse settings: %v\ncontent:\n%s", err, string(outBytes))
	}

	if _, ok := got["skipDangerousModePermissionPrompt"]; ok {
		t.Fatalf("skipDangerousModePermissionPrompt should not be present for non-claude tool:\n%s", string(outBytes))
	}
}

func TestEntrypoint_NoYolo_DoesNotInjectBypassPermissions(t *testing.T) {
	requireJQ(t)

	template := `{"model":"claude-sonnet-4-20250514"}`

	targetPath, output, err := runEntrypointSettingsPatchWithToolAndEnv(t, "claude", "settings.json", template, nil)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	outBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(outBytes, &got); err != nil {
		t.Fatalf("parse settings: %v\ncontent:\n%s", err, string(outBytes))
	}

	if _, ok := got["skipDangerousModePermissionPrompt"]; ok {
		t.Fatalf("skipDangerousModePermissionPrompt should not be present without ENCLAVE_YOLO=1:\n%s", string(outBytes))
	}
}

func TestEntrypoint_YoloPreTrustsWorkspaceForCodex(t *testing.T) {
	template := "model_reasoning_effort = \"high\"\n"

	targetPath, output, err := runEntrypointSettingsPatchWithToolAndEnv(t, "codex", "config.toml", template, []string{"ENCLAVE_YOLO=1"})
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	outBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	got := string(outBytes)

	projectDir := filepath.Join(filepath.Dir(filepath.Dir(targetPath)), "project")
	wantHeader := "[projects.\"" + projectDir + "\"]"
	if !strings.Contains(got, wantHeader) {
		t.Fatalf("expected workspace trust header %q in config:\n%s", wantHeader, got)
	}
	if !strings.Contains(got, "trust_level = \"trusted\"") {
		t.Fatalf("expected trust_level = \"trusted\" in config:\n%s", got)
	}
	// Existing template content must be preserved.
	if !strings.Contains(got, "model_reasoning_effort = \"high\"") {
		t.Fatalf("expected existing template content preserved:\n%s", got)
	}
	// The trust table must be appended exactly once.
	if n := strings.Count(got, wantHeader); n != 1 {
		t.Fatalf("expected trust header exactly once, got %d:\n%s", n, got)
	}
}

func TestEntrypoint_NoYolo_DoesNotPreTrustWorkspaceForCodex(t *testing.T) {
	template := "model_reasoning_effort = \"high\"\n"

	targetPath, output, err := runEntrypointSettingsPatchWithToolAndEnv(t, "codex", "config.toml", template, nil)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	outBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if strings.Contains(string(outBytes), "[projects.") {
		t.Fatalf("workspace trust must not be injected without ENCLAVE_YOLO=1:\n%s", string(outBytes))
	}
}

func TestEntrypoint_YoloDoesNotDuplicatePreExistingCodexTrust(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	templatePath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(templatePath, []byte("model_reasoning_effort = \"high\"\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	// Seed the persistent target with a pre-existing project entry in the
	// dotted form codex/toml encoders may emit. A bracketed-header guard would
	// miss this and append a second definition of the same key -> TOML error.
	targetPath := filepath.Join(home, "target", "config.toml")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	existing := "projects.\"" + projectDir + "\".trust_level = \"trusted\"\n"
	if err := os.WriteFile(targetPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing target: %v", err)
	}

	entrypointPath := filepath.Join("..", "..", "entrypoint.sh")
	toolsDir := filepath.Join("..", "..", "extensions", "tools")
	cmd := exec.Command("bash", entrypointPath, "true")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=codex",
		"ENCLAVE_YOLO=1",
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
		"ENCLAVE_TOOL_SETTINGS_TEMPLATE=" + templatePath,
		"ENCLAVE_TOOL_SETTINGS_TARGET=" + targetPath,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, out)
	}

	outBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	got := string(outBytes)

	// The pre-existing entry must be detected, so nothing is appended.
	if n := strings.Count(got, projectDir); n != 1 {
		t.Fatalf("expected project key exactly once (no duplicate), got %d:\n%s", n, got)
	}
	// The result must remain parseable TOML (a duplicated key would error).
	var parsed map[string]any
	if _, err := toml.Decode(got, &parsed); err != nil {
		t.Fatalf("result is not valid TOML: %v\ncontent:\n%s", err, got)
	}
}

func TestEntrypoint_YoloPreservesUserTrustOverrideForCodex(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	templatePath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(templatePath, []byte("model_reasoning_effort = \"high\"\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	// The user explicitly marked this very workspace untrusted (e.g. via a
	// passed-through host config). Yolo pre-trust must defer to that override
	// rather than flipping it to trusted.
	targetPath := filepath.Join(home, "target", "config.toml")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	existing := "[projects.\"" + projectDir + "\"]\ntrust_level = \"untrusted\"\n"
	if err := os.WriteFile(targetPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing target: %v", err)
	}

	entrypointPath := filepath.Join("..", "..", "entrypoint.sh")
	toolsDir := filepath.Join("..", "..", "extensions", "tools")
	cmd := exec.Command("bash", entrypointPath, "true")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=codex",
		"ENCLAVE_YOLO=1",
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
		"ENCLAVE_TOOL_SETTINGS_TEMPLATE=" + templatePath,
		"ENCLAVE_TOOL_SETTINGS_TARGET=" + targetPath,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, out)
	}

	outBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	got := string(outBytes)

	// The user's untrusted override stays; pre-trust did not flip it.
	if !strings.Contains(got, "trust_level = \"untrusted\"") {
		t.Fatalf("expected user untrusted override to be preserved:\n%s", got)
	}
	if strings.Contains(got, "trust_level = \"trusted\"") {
		t.Fatalf("pre-trust must not override an explicit user trust setting:\n%s", got)
	}
	// And no duplicate project table was appended.
	var parsed map[string]any
	if _, err := toml.Decode(got, &parsed); err != nil {
		t.Fatalf("result is not valid TOML: %v\ncontent:\n%s", err, got)
	}
}

func runEntrypointSettingsTemplate(t *testing.T, templateFilename string, templateContent string, existingTargetContent *string) (string, string, error) {
	t.Helper()

	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	templatePath := filepath.Join(home, templateFilename)
	if err := os.MkdirAll(filepath.Dir(templatePath), 0o755); err != nil {
		t.Fatalf("mkdir template dir: %v", err)
	}
	if err := os.WriteFile(templatePath, []byte(templateContent), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	targetPath := filepath.Join(home, "target", templateFilename)
	if existingTargetContent != nil {
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			t.Fatalf("mkdir target dir: %v", err)
		}
		if err := os.WriteFile(targetPath, []byte(*existingTargetContent), 0o644); err != nil {
			t.Fatalf("write existing target: %v", err)
		}
	}

	entrypointPath := filepath.Join("..", "..", "entrypoint.sh")
	toolsDir := filepath.Join("..", "..", "extensions", "tools")
	cmd := exec.Command("bash", entrypointPath, "true")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=pi",
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
		"ENCLAVE_TOOL_SETTINGS_TEMPLATE=" + templatePath,
		"ENCLAVE_TOOL_SETTINGS_TARGET=" + targetPath,
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return targetPath, string(out), err
}

func runEntrypointSettingsTemplateWithConfigAndAuth(t *testing.T, templateFilename string, templateContent string, relativeTargetPath string, authFile string) (string, string, error) {
	t.Helper()

	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	templatePath := filepath.Join(home, templateFilename)
	if err := os.MkdirAll(filepath.Dir(templatePath), 0o755); err != nil {
		t.Fatalf("mkdir template dir: %v", err)
	}
	if err := os.WriteFile(templatePath, []byte(templateContent), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	configDir := filepath.Join(home, ".pi")
	targetPath := filepath.Join(home, filepath.FromSlash(relativeTargetPath))
	authDir := filepath.Join(home, ".enclave-auth")

	entrypointPath := filepath.Join("..", "..", "entrypoint.sh")
	toolsDir := filepath.Join("..", "..", "extensions", "tools")
	cmd := exec.Command("bash", entrypointPath, "true")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=pi",
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
		"ENCLAVE_TOOL_CONFIG_DIR=" + configDir,
		"ENCLAVE_AUTH_DIR=" + authDir,
		"ENCLAVE_AUTH_FILES=" + authFile,
		"ENCLAVE_AUTH_RECONCILE_LIB=" + filepath.Join("..", "..", "runtime-assets", "auth-reconcile.sh"),
		"ENCLAVE_TOOL_SETTINGS_TEMPLATE=" + templatePath,
		"ENCLAVE_TOOL_SETTINGS_TARGET=" + targetPath,
	}
	out, err := cmd.CombinedOutput()
	return targetPath, string(out), err
}

func runEntrypointSettingsPatchWithToolAndEnv(t *testing.T, tool string, templateFilename string, templateContent string, extraEnv []string) (string, string, error) {
	t.Helper()

	if tool == "" {
		tool = "pi"
	}

	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	templatePath := filepath.Join(home, templateFilename)
	if err := os.MkdirAll(filepath.Dir(templatePath), 0o755); err != nil {
		t.Fatalf("mkdir template dir: %v", err)
	}
	if err := os.WriteFile(templatePath, []byte(templateContent), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	targetPath := filepath.Join(home, "target", templateFilename)

	entrypointPath := filepath.Join("..", "..", "entrypoint.sh")
	toolsDir := filepath.Join("..", "..", "extensions", "tools")
	cmd := exec.Command("bash", entrypointPath, "true")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=" + tool,
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
		"ENCLAVE_TOOL_SETTINGS_TEMPLATE=" + templatePath,
		"ENCLAVE_TOOL_SETTINGS_TARGET=" + targetPath,
	}
	env = append(env, extraEnv...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return targetPath, string(out), err
}

func requireJQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
}
