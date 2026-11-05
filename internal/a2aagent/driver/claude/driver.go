// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package claude is the a2aagent.Driver for Claude Code's headless
// "-p --output-format stream-json" mode (eventFormat claude-stream-json).
package claude

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"enclave/internal/a2aagent"
	"enclave/internal/tools"
)

// DisableBackgroundTasksEnv keeps one result per process (P2: a background
// Bash task otherwise starts a second turn after the first result).
const DisableBackgroundTasksEnv = "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1"

// Options tune a Driver; the zero value is usable.
type Options struct {
	// ExtraArgs go after the spec command and before the session arguments,
	// e.g. the profile's yolo flag when the session runs with yolo.
	ExtraArgs []string
	// Log receives skipped events and diagnostics; nil discards them.
	Log io.Writer
	// NewSessionID replaces the random UUID for new contexts (tests).
	NewSessionID func() (string, error)
}

// Driver implements a2aagent.Driver for Claude Code.
type Driver struct {
	spec     tools.A2ASpec
	versions tools.VersionRange
	opts     Options
}

var _ a2aagent.Driver = (*Driver)(nil)

// New validates spec and returns a driver for it.
func New(spec tools.A2ASpec, opts Options) (*Driver, error) {
	if err := tools.ValidateA2ASpec(&spec); err != nil {
		return nil, err
	}
	if spec.EventFormat != tools.EventFormatClaudeStreamJSON {
		return nil, fmt.Errorf("claude driver cannot decode event format %q", spec.EventFormat)
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
	if d.opts.NewSessionID == nil {
		d.opts.NewSessionID = newUUID
	}
	return d, nil
}

// Name implements a2aagent.Driver.
func (d *Driver) Name() string { return tools.EventFormatClaudeStreamJSON }

// CheckVersion implements a2aagent.Driver; without versionCommand nothing is checked.
func (d *Driver) CheckVersion(ctx context.Context) error {
	if len(d.spec.VersionCommand) == 0 {
		return nil
	}
	argv := d.spec.VersionCommand
	// #nosec G204 -- argv comes from the validated tool spec, not from a request.
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

// NewSessionID implements a2aagent.Driver: a UUID when the spec has newSessionArgs, else empty.
func (d *Driver) NewSessionID() (string, error) {
	if len(d.spec.NewSessionArgs) == 0 {
		return "", nil
	}
	id, err := d.opts.NewSessionID()
	if err != nil {
		return "", fmt.Errorf("new harness session ID: %w", err)
	}
	if !uuidPattern.MatchString(id) {
		return "", fmt.Errorf("new harness session ID %q is not a UUID", id)
	}
	return id, nil
}

// Command implements a2aagent.Driver. A new context passes the executor's
// pre-bound session ID through newSessionArgs; Decode reports it with the init event.
func (d *Driver) Command(_ context.Context, inv a2aagent.Invocation) (*exec.Cmd, error) {
	argv := append([]string{}, d.spec.Command...)
	argv = append(argv, d.opts.ExtraArgs...)
	switch {
	case inv.HarnessSessionID != "":
		if !uuidPattern.MatchString(inv.HarnessSessionID) {
			return nil, fmt.Errorf("harness session ID %q is not a UUID", inv.HarnessSessionID)
		}
		argv = append(argv, substitute(d.spec.ResumeArgs, inv.HarnessSessionID)...)
	case len(d.spec.NewSessionArgs) > 0:
		if !uuidPattern.MatchString(inv.NewHarnessSessionID) {
			return nil, fmt.Errorf("new harness session ID %q is not a UUID", inv.NewHarnessSessionID)
		}
		argv = append(argv, substitute(d.spec.NewSessionArgs, inv.NewHarnessSessionID)...)
	}
	// #nosec G204 -- argv is the validated tool spec plus a UUID; the prompt goes to stdin.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = inv.WorkDir
	env := inv.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(append([]string{}, env...), DisableBackgroundTasksEnv)
	return cmd, nil
}

func substitute(args []string, sessionID string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.ReplaceAll(arg, tools.SessionIDPlaceholder, sessionID)
	}
	return out
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// TranscriptPaths lists the config-store-relative paths Claude writes for one
// session created in cwd (P2, CR-P2-3); missing paths are harmless to delete.
func TranscriptPaths(cwd, sessionID string) []string {
	paths := make([]string, 0, 6)
	if project := projectDirName(cwd); project != "" {
		paths = append(paths,
			"projects/"+project+"/"+sessionID+".jsonl",
			"projects/"+project+"/"+sessionID)
	}
	return append(paths,
		"session-env/"+sessionID,
		"file-history/"+sessionID,
		"tasks/"+sessionID)
}

// InvocationTranscripts inventories pre-bound sessions even if the process
// dies before its system/init event (InvocationInventory).
func (d *Driver) InvocationTranscripts(inv a2aagent.Invocation) []string {
	id := inv.HarnessSessionID
	if id == "" {
		id = inv.NewHarnessSessionID
	}
	if !uuidPattern.MatchString(id) {
		return nil
	}
	return TranscriptPaths(inv.WorkDir, id)
}

// maxProjectDirName is where Claude starts shortening project directory names
// with a hash this driver does not reproduce.
const maxProjectDirName = 200

// projectDirName is Claude's project directory name for cwd: every character
// outside [A-Za-z0-9] becomes "-".
func projectDirName(cwd string) string {
	if cwd == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 0xFFFF:
			// Claude replaces UTF-16 code units, and this rune needs two.
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() > maxProjectDirName {
		return ""
	}
	return b.String()
}

var errNoOutcome = errors.New("claude stream ended without a result")
