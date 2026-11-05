// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package codex adapts Codex CLI's noninteractive JSONL event stream.
package codex

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"enclave/internal/a2aagent"
	"enclave/internal/tools"
)

// Options tune a Driver; the zero value uses the current Codex config root.
type Options struct {
	ExtraArgs []string
	Log       io.Writer
	// ConfigDir is the config-store root, not a transcript directory.
	ConfigDir string
}

// Driver implements a2aagent.Driver for codex exec --json.
type Driver struct {
	spec     tools.A2ASpec
	versions tools.VersionRange
	opts     Options
	mu       sync.Mutex
	// The executor runs one harness at a time. Keep the invocation's private
	// SQLite directory until Decode returns, including errors and cancellation.
	sqlitePath string
}

var _ a2aagent.Driver = (*Driver)(nil)

// New validates the spec and resolves the config root used for transcript inventory.
func New(spec tools.A2ASpec, opts Options) (*Driver, error) {
	if err := tools.ValidateA2ASpec(&spec); err != nil {
		return nil, err
	}
	if spec.EventFormat != tools.EventFormatCodexExecJSON {
		return nil, fmt.Errorf("codex driver cannot decode event format %q", spec.EventFormat)
	}
	if len(spec.NewSessionArgs) != 0 {
		return nil, fmt.Errorf("codex chooses its session ID; a2a.newSessionArgs must be empty")
	}
	d := &Driver{spec: spec, opts: opts}
	if spec.Versions != "" {
		r, err := tools.ParseVersionRange(spec.Versions)
		if err != nil {
			return nil, err
		}
		d.versions = r
	}
	if d.opts.Log == nil {
		d.opts.Log = io.Discard
	}
	if d.opts.ConfigDir == "" {
		d.opts.ConfigDir = os.Getenv("CODEX_HOME")
	}
	if d.opts.ConfigDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("codex config root: %w", err)
		}
		d.opts.ConfigDir = filepath.Join(home, ".codex")
	}
	root, err := filepath.Abs(d.opts.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("codex config root: %w", err)
	}
	d.opts.ConfigDir = root
	return d, nil
}

// Name implements a2aagent.Driver.
func (d *Driver) Name() string { return tools.EventFormatCodexExecJSON }

// NewSessionID implements a2aagent.Driver; thread.started reports the chosen ID.
func (d *Driver) NewSessionID() (string, error) { return "", nil }

// InvocationTranscripts lets the executor persist the allocated SQLite root
// before spawn, even when Codex has not picked a thread ID yet.
func (d *Driver) InvocationTranscripts(_ a2aagent.Invocation) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sqlitePath == "" {
		return nil
	}
	return []string{d.sqlitePath}
}

// CheckVersion checks the installed harness against the spec's pinned range.
func (d *Driver) CheckVersion(ctx context.Context) error {
	if len(d.spec.VersionCommand) == 0 {
		return nil
	}
	argv := d.spec.VersionCommand
	// #nosec G204 -- argv comes from the validated tool spec.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	v, err := tools.ExtractVersion(string(out))
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	if !d.versions.Contains(v) {
		return fmt.Errorf("%s version %s is outside the supported range %s", argv[0], v, d.versions)
	}
	return nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Command leaves process ownership and stdin to the executor. The trailing '-'
// makes stdin unambiguous for both exec and its resume subcommand.
func (d *Driver) Command(_ context.Context, inv a2aagent.Invocation) (*exec.Cmd, error) {
	argv := append([]string{}, d.spec.Command...)
	argv = append(argv, d.opts.ExtraArgs...)
	if inv.HarnessSessionID != "" {
		if !uuidPattern.MatchString(inv.HarnessSessionID) {
			return nil, fmt.Errorf("harness session ID %q is not a UUID", inv.HarnessSessionID)
		}
		paths, err := TranscriptPaths(d.opts.ConfigDir, inv.HarnessSessionID)
		if err != nil {
			return nil, fmt.Errorf("find codex resume transcript: %w", err)
		}
		found := false
		for _, path := range paths {
			found = found || strings.HasPrefix(path, "sessions/") || strings.HasPrefix(path, "archived_sessions/")
		}
		if !found {
			return nil, fmt.Errorf("no codex rollout found for session %s", inv.HarnessSessionID)
		}
	}
	base := filepath.Join(d.opts.ConfigDir, "a2a-codex-sqlite")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, fmt.Errorf("create codex SQLite inventory root: %w", err)
	}
	dir, err := os.MkdirTemp(base, "turn-")
	if err != nil {
		return nil, fmt.Errorf("create codex SQLite directory: %w", err)
	}
	rel, err := filepath.Rel(d.opts.ConfigDir, dir)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.sqlitePath = filepath.ToSlash(rel)
	d.mu.Unlock()
	argv = append(argv, "-c", "sqlite_home="+strconv.Quote(dir))
	if inv.HarnessSessionID != "" {
		for _, arg := range d.spec.ResumeArgs {
			argv = append(argv, strings.ReplaceAll(arg, tools.SessionIDPlaceholder, inv.HarnessSessionID))
		}
	}
	argv = append(argv, "-")
	// #nosec G204 -- validated spec and UUID only; the prompt goes to stdin.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = inv.WorkDir
	env := inv.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(append([]string{}, env...), "CODEX_HOME="+d.opts.ConfigDir, "CODEX_SQLITE_HOME="+dir)
	return cmd, nil
}
