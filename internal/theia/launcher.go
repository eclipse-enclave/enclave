// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package theia launches the host-installed Theia or Theia-Next desktop IDE
// attached to a running enclave container.
package theia

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"enclave/internal/config"
)

// Variant identifies which Theia binary to launch.
type Variant string

const (
	VariantTheia     Variant = "theia"
	VariantTheiaNext Variant = "theia-next"
)

func (v Variant) Valid() bool {
	return v == VariantTheia || v == VariantTheiaNext
}

func (v Variant) Binary() string { return string(v) }

// containerNamePattern restricts attach-container values to Docker-legal
// container names. This guards against argv injection if a container name
// ever flows in from an untrusted source.
var containerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// BuildArgs returns the argv tail (everything after the binary) for a Theia
// attach launch. Preference keys are emitted in sorted order so the argv is
// deterministic and easy to assert on in tests.
func BuildArgs(containerName string, preferences map[string]any) ([]string, error) {
	if !containerNamePattern.MatchString(containerName) {
		return nil, fmt.Errorf("invalid container name: %q", containerName)
	}
	args := []string{"--attach-container", containerName}
	keys := make([]string, 0, len(preferences))
	for k := range preferences {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		encoded, err := json.Marshal(preferences[k])
		if err != nil {
			return nil, fmt.Errorf("encode preference %q: %w", k, err)
		}
		args = append(args, "--session-preference", fmt.Sprintf("%s=%s", k, string(encoded)))
	}
	return args, nil
}

// LogPath returns the file that a launched Theia variant's stdout/stderr is
// redirected to, so the chatty IDE output stays out of the CLI/GUI and can be
// found again later. One file per container; overwritten on each launch.
func LogPath(home, containerName string) string {
	return filepath.Join(config.HostTheiaLogsDir(home), containerName+".log")
}

// Launch spawns the requested Theia variant attached to containerName.
// preferences may be nil; callers normally merge defaults + config via
// LoadPreferences before calling. The IDE's stdout/stderr are redirected to
// logPath (its directory is created); pass "" to inherit the parent's streams.
func Launch(variant Variant, containerName string, preferences map[string]any, logPath string) error {
	if !variant.Valid() {
		return fmt.Errorf("unsupported theia variant: %q", variant)
	}

	// Open the log first so the attempt (and any failure below) is always
	// recorded, even if the binary lookup fails.
	var logFile *os.File
	if logPath != "" {
		f, err := openLogFile(logPath)
		if err != nil {
			return err
		}
		// The child duplicates the fd on Start, so we can close our copy after.
		defer func() { _ = f.Close() }()
		logFile = f
		_, _ = fmt.Fprintf(logFile, "=== %s: launching %s (binary %q) attached to %s ===\n",
			time.Now().Format(time.RFC3339), variant, variant.Binary(), containerName)
	}

	fail := func(err error) error {
		if logFile != nil {
			_, _ = fmt.Fprintf(logFile, "ERROR: %v\n", err)
		}
		return err
	}

	bin, err := exec.LookPath(variant.Binary())
	if err != nil {
		return fail(fmt.Errorf("%s not found on PATH: %w", variant.Binary(), err))
	}
	args, err := BuildArgs(containerName, preferences)
	if err != nil {
		return fail(err)
	}

	// #nosec G204 -- variant is a fixed enum and BuildArgs validates the container name.
	cmd := exec.Command(bin, args...)
	if logFile != nil {
		// Redact secret-bearing preferences (e.g. externalApi.token) before
		// writing the argv: the log is 0600 but need not carry the token
		// verbatim. Note the process still receives the real value as an argv
		// element, so it remains visible via `ps` to other users on the host —
		// acceptable for the loopback dev flow this targets.
		_, _ = fmt.Fprintf(logFile, "%s %s\n\n", bin, formatArgs(redactArgs(args)))
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	return nil
}

func openLogFile(logPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	// #nosec G304 -- logPath is resolved by LogPath under the enclave-managed log directory.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", logPath, err)
	}
	return f, nil
}

// sensitivePrefKeys are preference keys whose values must be masked in the
// launch log. They still reach the IDE process unmodified via argv.
var sensitivePrefKeys = map[string]bool{
	"externalApi.token": true,
}

// redactArgs returns a copy of args with the values of any sensitive
// `--session-preference key=value` pairs masked, leaving the input untouched.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i+1 < len(out); i++ {
		if out[i] != "--session-preference" {
			continue
		}
		key, _, found := strings.Cut(out[i+1], "=")
		if found && sensitivePrefKeys[key] {
			out[i+1] = key + "=<redacted>"
		}
	}
	return out
}

func formatArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"'\\") {
			quoted[i] = fmt.Sprintf("%q", a)
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}
