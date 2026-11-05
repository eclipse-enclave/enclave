// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package pi is the a2aagent.Driver for Pi's headless
// "-p --mode json" mode (eventFormat pi-json).
package pi

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"enclave/internal/a2aagent"
	"enclave/internal/tools"
)

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

// Driver implements a2aagent.Driver for Pi.
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
	if spec.EventFormat != tools.EventFormatPiJSON {
		return nil, fmt.Errorf("pi driver cannot decode event format %q", spec.EventFormat)
	}
	if len(spec.NewSessionArgs) == 0 {
		return nil, fmt.Errorf("pi driver requires a2a.newSessionArgs for per-context transcript isolation")
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
func (d *Driver) Name() string { return tools.EventFormatPiJSON }

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
// pre-bound session ID through newSessionArgs; Decode reports it in the session header.
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
	cmd.Env = append([]string{}, env...)
	var home string
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") {
			home = strings.TrimPrefix(kv, "HOME=")
		}
	}
	if !filepath.IsAbs(home) {
		return nil, fmt.Errorf("pi driver requires an absolute HOME for config-store session isolation")
	}
	id := inv.HarnessSessionID
	if id == "" {
		id = inv.NewHarnessSessionID
	}
	cmd.Args = append(cmd.Args, "--session-dir", filepath.Join(home, ".pi", "agent", "a2a-sessions", id))
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

// TranscriptPaths returns the per-context directory the driver passes with
// --session-dir. Pi's timestamped JSONL files and any branches stay isolated.
func TranscriptPaths(sessionID string) []string {
	return []string{"agent/a2a-sessions/" + sessionID}
}

// InvocationTranscripts inventories the isolated session directory before
// spawn, including when cancellation arrives before Pi emits its header.
func (d *Driver) InvocationTranscripts(inv a2aagent.Invocation) []string {
	id := inv.HarnessSessionID
	if id == "" {
		id = inv.NewHarnessSessionID
	}
	if !uuidPattern.MatchString(id) {
		return nil
	}
	return TranscriptPaths(id)
}
