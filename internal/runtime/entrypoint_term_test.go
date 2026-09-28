// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const terminfoWarning = "no terminfo entry for TERM="

func TestEntrypoint_TERMFallsBackWhenTerminfoMissing(t *testing.T) {
	env := []string{prependPath(t, fakeInfocmpDir(t)), "TERM=enclave-unknown-term"}
	value, output, err := runEntrypointCaptureTERM(t, env)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}
	if value != "xterm-256color" {
		t.Fatalf("expected TERM=xterm-256color, got %q", value)
	}
	if !strings.Contains(output, terminfoWarning+"enclave-unknown-term") {
		t.Fatalf("expected terminfo warning, got output:\n%s", output)
	}
}

func TestEntrypoint_TERMPreservesKnownValue(t *testing.T) {
	env := []string{prependPath(t, fakeInfocmpDir(t)), "TERM=" + fakeKnownTerm}
	value, output, err := runEntrypointCaptureTERM(t, env)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}
	if value != fakeKnownTerm {
		t.Fatalf("expected TERM=%s, got %q", fakeKnownTerm, value)
	}
	if strings.Contains(output, terminfoWarning) {
		t.Fatalf("unexpected terminfo warning, got output:\n%s", output)
	}
}

// The qemu/Alpine bundle ships without infocmp, where TERM must pass through
// untouched rather than be rewritten on every start.
func TestEntrypoint_TERMPreservedWithoutInfocmp(t *testing.T) {
	env := []string{"PATH=" + pathWithoutInfocmp(t), "TERM=enclave-unknown-term"}
	value, output, err := runEntrypointCaptureTERM(t, env)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}
	if value != "enclave-unknown-term" {
		t.Fatalf("expected TERM=enclave-unknown-term, got %q", value)
	}
	if strings.Contains(output, terminfoWarning) {
		t.Fatalf("unexpected terminfo warning, got output:\n%s", output)
	}
}

func runEntrypointCaptureTERM(t *testing.T, extraEnv []string) (string, string, error) {
	t.Helper()

	home, out, err := runEntrypointCommand(t, extraEnv, "bash", "-c", `printf "%s" "${TERM:-}" > "$HOME/term.out"`)
	valueBytes, readErr := os.ReadFile(filepath.Join(home, "term.out"))
	if readErr != nil {
		t.Fatalf("read term output: %v\nentrypoint output:\n%s", readErr, out)
	}

	return string(valueBytes), out, err
}

const fakeKnownTerm = "enclave-known-term"

// fakeInfocmpDir provides an infocmp shim that recognises a fixed set of
// terminals, so the fallback does not depend on the host terminfo database.
func fakeInfocmpDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
for known in xterm-256color ` + fakeKnownTerm + `; do
    if [ "$1" = "$known" ]; then
        exit 0
    fi
done
echo "infocmp: couldn't open terminfo file for $1." >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "infocmp"), []byte(script), 0o755); err != nil {
		t.Fatalf("write infocmp shim: %v", err)
	}
	return dir
}

func prependPath(t *testing.T, dir string) string {
	t.Helper()
	return "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// pathWithoutInfocmp mirrors the host PATH into a directory of symlinks with
// infocmp left out, since shadowing it is not possible: command -v skips a
// non-executable entry and keeps searching the rest of PATH.
func pathWithoutInfocmp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, pathDir := range filepath.SplitList(os.Getenv("PATH")) {
		// A relative entry would mirror into targets that dangle from the temp
		// dir and shadow the real binary further down PATH.
		if !filepath.IsAbs(pathDir) {
			continue
		}
		entries, err := os.ReadDir(pathDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Name() == "infocmp" {
				continue
			}
			// An existing link means an earlier PATH entry won, as it would on the host.
			_ = os.Symlink(filepath.Join(pathDir, entry.Name()), filepath.Join(dir, entry.Name()))
		}
	}
	return dir
}
