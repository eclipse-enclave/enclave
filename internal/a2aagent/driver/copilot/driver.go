// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package copilot implements the Copilot CLI JSONL headless driver.
package copilot

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
	ExtraArgs []string
	Log       io.Writer
}

// Driver implements a2aagent.Driver for Copilot CLI.
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
	if spec.EventFormat != tools.EventFormatCopilotJSON {
		return nil, fmt.Errorf("copilot driver cannot decode event format %q", spec.EventFormat)
	}
	d := &Driver{spec: spec, opts: opts}
	if spec.Versions != "" {
		versions, err := tools.ParseVersionRange(spec.Versions)
		if err != nil {
			return nil, err
		}
		d.versions = versions
	}
	if d.opts.Log == nil {
		d.opts.Log = io.Discard
	}
	return d, nil
}

// Name implements a2aagent.Driver.
func (d *Driver) Name() string { return tools.EventFormatCopilotJSON }

// CheckVersion implements a2aagent.Driver.
func (d *Driver) CheckVersion(ctx context.Context) error {
	if len(d.spec.VersionCommand) == 0 {
		return nil
	}
	argv := d.spec.VersionCommand
	// #nosec G204 -- command comes from the validated tool spec.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	version, err := tools.ExtractVersion(string(out))
	if err != nil {
		return err
	}
	if !d.versions.Contains(version) {
		return fmt.Errorf("%s version %s is outside the supported range %s", argv[0], version, d.versions)
	}
	return nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NewSessionID binds a UUID before Copilot creates any per-session files.
func (d *Driver) NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Command implements a2aagent.Driver; the executor supplies stdin and cancellation.
func (d *Driver) Command(_ context.Context, inv a2aagent.Invocation) (*exec.Cmd, error) {
	id := inv.HarnessSessionID
	args := d.spec.ResumeArgs
	if id == "" {
		id = inv.NewHarnessSessionID
		args = d.spec.NewSessionArgs
	}
	if !uuidPattern.MatchString(id) {
		return nil, fmt.Errorf("copilot harness session ID %q is not a UUID", id)
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("copilot driver requires newSessionArgs")
	}
	env := inv.Env
	if env == nil {
		env = os.Environ()
	}
	root := envValue(env, "COPILOT_HOME")
	if root == "" {
		home := envValue(env, "HOME")
		if home == "" {
			return nil, fmt.Errorf("copilot driver needs COPILOT_HOME or HOME")
		}
		root = filepath.Join(home, ".copilot")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("copilot config root: %w", err)
	}
	privateHome := filepath.Join(root, filepath.FromSlash(TranscriptPaths(id)[0]))
	if err := prepareHome(root, privateHome); err != nil {
		return nil, fmt.Errorf("prepare copilot session config: %w", err)
	}
	argv := append([]string{}, d.spec.Command...)
	argv = append(argv, d.opts.ExtraArgs...)
	for _, arg := range args {
		argv = append(argv, strings.ReplaceAll(arg, tools.SessionIDPlaceholder, id))
	}
	// #nosec G204 -- validated spec argv and UUID; prompt is never an argument.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = inv.WorkDir
	cmd.Env = append(append([]string{}, env...), "COPILOT_HOME="+privateHome)
	return cmd, nil
}

func envValue(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], name+"="); ok {
			return value
		}
	}
	return ""
}

// Copilot's shared session-store.db duplicates prompts and answers in an FTS
// index. A private COPILOT_HOME keeps that database inside the retention unit.
// Only configuration inputs are shared; session/history/plugin runtime state is not.
func prepareHome(root, privateHome string) error {
	if err := os.MkdirAll(privateHome, 0o700); err != nil {
		return err
	}
	for _, name := range []string{
		"config.json", "settings.json", "permissions-config.json", "mcp-config.json",
		"mcp-oauth-config", "mcp-secrets", "lsp-config.json", "providers.json",
		"agents", "copilot-instructions.md", "extensions", "hooks", "instructions", "skills",
	} {
		source := filepath.Join(root, name)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		target := filepath.Join(privateHome, name)
		if _, err := os.Lstat(target); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Symlink(source, target); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

// TranscriptPaths returns the whole config-store-relative per-session home.
func TranscriptPaths(sessionID string) []string {
	if !uuidPattern.MatchString(sessionID) {
		return nil
	}
	return []string{"a2a-harness/copilot/" + sessionID}
}

// InvocationTranscripts inventories private state even when the process dies
// before Copilot's result event exposes its session ID.
func (d *Driver) InvocationTranscripts(inv a2aagent.Invocation) []string {
	id := inv.HarnessSessionID
	if id == "" {
		id = inv.NewHarnessSessionID
	}
	return TranscriptPaths(id)
}
