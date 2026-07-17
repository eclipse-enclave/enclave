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

// kitInitScript returns the absolute path to the runtime helper under test.
func kitInitScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "runtime-assets", "kit-init.sh"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return p
}

// runWriteInitFile sources kit-init.sh and pipes content into
// enclave_write_init_file <path> <mode> <onlyIfMissing>.
func runWriteInitFile(t *testing.T, env map[string]string, path, mode, onlyIfMissing, content string) (string, error) {
	t.Helper()
	kit := kitInitScript(t)
	script := `set -e; . "$KIT"; printf '%s' "$CONTENT" | enclave_write_init_file "$P" "$M" "$O"`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"KIT=" + kit,
		"P=" + path,
		"M=" + mode,
		"O=" + onlyIfMissing,
		"CONTENT=" + content,
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestWriteInitFileSubstitutesWhitelist(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	target := filepath.Join(home, "cfg", "${USER}.conf")
	content := "work=${WORKDIR}\nhome=${HOME}\nuser=${USER}\nkeep=${FOO}\n"

	env := map[string]string{
		"HOME":        home,
		"USER":        "alice",
		"PROJECT_DIR": project,
	}
	if out, err := runWriteInitFile(t, env, target, "", "false", content); err != nil {
		t.Fatalf("write init file: %v\n%s", err, out)
	}

	resolved := filepath.Join(home, "cfg", "alice.conf")
	got, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatalf("read resolved file %q: %v", resolved, err)
	}
	want := "work=" + project + "\nhome=" + home + "\nuser=alice\nkeep=${FOO}\n"
	if string(got) != want {
		t.Fatalf("content = %q, want %q", string(got), want)
	}
}

func TestWriteInitFileOnlyIfMissingKeepsExisting(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "keep.conf")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	env := map[string]string{"HOME": home, "USER": "bob"}
	if out, err := runWriteInitFile(t, env, target, "", "true", "replacement"); err != nil {
		t.Fatalf("write init file: %v\n%s", err, out)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("content = %q, want %q (must not overwrite)", string(got), "original")
	}
}

// TestWriteInitFileSkipDrainsStdin guards the SIGPIPE regression: a skipped
// entry must consume its piped-in content rather than close the pipe on the
// upstream writer. This mirrors entrypoint.sh, where `yq ... | write_init_file`
// runs under `set -e -o pipefail`; if the skip branch returns without draining
// stdin, the producer dies from SIGPIPE (exit 141) and fails the whole
// pipeline, aborting container start. The producer streams 200 KiB (well over
// the ~64 KiB pipe buffer) so a missing drain forces a real SIGPIPE instead of
// the write silently fitting in the buffer.
func TestWriteInitFileSkipDrainsStdin(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "keep.conf")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	kit := kitInitScript(t)
	// `head -c` on /dev/zero exits 0 on its own (no SIGPIPE from the source),
	// so any non-zero pipeline status must come from the reader closing early.
	script := `set -e -o pipefail; . "$KIT"; ` +
		`head -c 200000 /dev/zero | enclave_write_init_file "$P" "" "true"`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"KIT=" + kit,
		"P=" + target,
		"HOME=" + home,
		"USER=bob",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("skipped entry with piped content must not fail the pipeline (SIGPIPE): %v\n%s", err, out)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("content = %q, want %q (skip must not overwrite)", string(got), "original")
	}
}

func TestWriteInitFileOverwritesWhenNotOnlyIfMissing(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "over.conf")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	env := map[string]string{"HOME": home, "USER": "bob"}
	if out, err := runWriteInitFile(t, env, target, "", "false", "replacement"); err != nil {
		t.Fatalf("write init file: %v\n%s", err, out)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "replacement" {
		t.Fatalf("content = %q, want %q (must overwrite)", string(got), "replacement")
	}
}

func TestWriteInitFileAppliesMode(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "secret.conf")
	env := map[string]string{"HOME": home, "USER": "bob"}
	if out, err := runWriteInitFile(t, env, target, "0600", "false", "x"); err != nil {
		t.Fatalf("write init file: %v\n%s", err, out)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 0600", perm)
	}
}

func TestWriteInitFileCreatesParentDirs(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "a", "b", "c", "deep.conf")
	env := map[string]string{"HOME": home, "USER": "bob"}
	if out, err := runWriteInitFile(t, env, target, "", "false", "deep"); err != nil {
		t.Fatalf("write init file: %v\n%s", err, out)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "deep" {
		t.Fatalf("content = %q, want %q", string(got), "deep")
	}
}

// hasMikefarahYq reports whether the yq on PATH is mikefarah/yq v4 (the flavor
// the apply helper targets). The kislyuk yq (jq wrapper) is skipped.
func hasMikefarahYq(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("yq"); err != nil {
		return false
	}
	out, err := exec.Command("yq", "--version").CombinedOutput()
	if err != nil {
		return false
	}
	v := strings.ToLower(string(out))
	return strings.Contains(v, "mikefarah") || strings.Contains(v, "version v4")
}

// runFeatureEnabled sources kit-init.sh and reports the exit status of
// enclave_feature_enabled <name> against a manifest file (empty manifestPath
// means "no manifest present").
func runFeatureEnabled(t *testing.T, manifestPath, name string) bool {
	t.Helper()
	kit := kitInitScript(t)
	script := `. "$KIT"; enclave_feature_enabled "$NAME"`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"KIT=" + kit,
		"NAME=" + name,
		"ENCLAVE_ENABLED_FEATURES_FILE=" + manifestPath,
	}
	return cmd.Run() == nil
}

func TestFeatureEnabledHonorsManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "installed-features")
	if err := os.WriteFile(manifest, []byte("python-dev\ngithub-cli\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if !runFeatureEnabled(t, manifest, "python-dev") {
		t.Error("listed feature should be enabled")
	}
	if runFeatureEnabled(t, manifest, "not-in-manifest") {
		t.Error("unlisted feature must not be enabled")
	}
	// Guard against substring matches (grep -xF is whole-line, fixed-string).
	if runFeatureEnabled(t, manifest, "python") {
		t.Error("partial name must not match a manifest line")
	}
}

func TestFeatureEnabledFailsOpenWithoutManifest(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if !runFeatureEnabled(t, missing, "anything") {
		t.Error("absent manifest must fail open (enabled) to preserve prior behavior")
	}
}

func TestApplyInitFilesMaterializesEntry(t *testing.T) {
	if !hasMikefarahYq(t) {
		t.Skip("requires mikefarah/yq v4; host has kislyuk yq")
	}
	extDir := t.TempDir()
	home := t.TempDir()
	project := t.TempDir()

	spec := `schemaVersion: "1"
kind: sandbox
name: demo
commands:
  initFiles:
    - path: ${HOME}/gen/${USER}.conf
      content: |
        work=${WORKDIR}
        keep=${FOO}
      mode: "0600"
      onlyIfMissing: true
`
	if err := os.WriteFile(filepath.Join(extDir, "spec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	kit := kitInitScript(t)
	script := `set -e; . "$KIT"; enclave_apply_init_files "$EXT"`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"KIT=" + kit,
		"EXT=" + extDir,
		"HOME=" + home,
		"USER=carol",
		"PROJECT_DIR=" + project,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply init files: %v\n%s", err, out)
	}

	resolved := filepath.Join(home, "gen", "carol.conf")
	got, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatalf("read resolved: %v", err)
	}
	want := "work=" + project + "\nkeep=${FOO}\n"
	if string(got) != want {
		t.Fatalf("content = %q, want %q", string(got), want)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 0600", perm)
	}
}
