// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package sbx runs each Enclave session in its own Docker Sandboxes (sbx)
// sandbox, and therefore in its own microVM. It drives the sbx CLI: the
// session image is loaded into sbx's image store, a sandbox is created from a
// generated kit with the session's mounts and ports, the session command runs
// through `sbx exec`, and the sandbox is removed when it ends. Persistent
// stores keep the host layout shared with the other backends.
package sbx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"enclave/internal/backend"
	"enclave/internal/backend/hostlayout"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// lookPath finds host executables; tests replace it.
var lookPath = exec.LookPath

type Options struct {
	Host  model.Host
	Paths model.Paths
	// StateDir holds host-side sbx backend state (loaded image stamps).
	StateDir string
	// Images is the host engine that builds session images.
	Images ImageSource
}

type Backend struct {
	opts   Options
	cli    runner
	images ImageSource
	// layout realizes persistent stores on the shared host layout.
	layout *hostlayout.Layout
}

func New(opts Options) *Backend {
	return &Backend{
		opts:   opts,
		cli:    cliRunner{binary: defaultBinary},
		images: opts.Images,
		layout: hostlayout.New(opts.Host),
	}
}

func (b *Backend) Name() string { return backend.NameSBX }

func (b *Backend) Check(context.Context) error {
	if _, err := lookPath(defaultBinary); err != nil {
		return fmt.Errorf("sbx backend: %s not found on PATH; install Docker Sandboxes and run `sbx login`", defaultBinary)
	}
	if b.images == nil {
		return fmt.Errorf("sbx backend: no host engine configured to build session images")
	}
	return nil
}

// Capabilities reports no restricted networking: sbx enforces its own network
// policy on every sandbox, but Enclave's allowlist and secret release are not
// mapped onto it yet.
func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{RestrictedNetwork: false, SecretHTTPRelease: false}
}

func (b *Backend) Storage() backend.StoreManager { return b.layout.Storage() }

func (b *Backend) PrepareStores(ctx context.Context, prep backend.StorePrep) (backend.StoreState, error) {
	return b.layout.PrepareStores(ctx, prep)
}

func (b *Backend) Run(ctx context.Context, req backend.Request, attach backend.AttachIO) (backend.ExitStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	interrupted := false
	if req.AuthSync != nil {
		defer func() {
			if !interrupted {
				b.layout.SyncAuth(req)
			}
		}()
	}
	if req.Detached {
		return backend.ExitStatus{}, fmt.Errorf("sbx backend: detached sessions are not supported")
	}
	if err := backend.Validate(req, b.Capabilities()); err != nil {
		return backend.ExitStatus{}, err
	}
	if _, err := sandboxName(req.Session.Name); err != nil {
		return backend.ExitStatus{}, err
	}
	if err := b.Check(ctx); err != nil {
		return backend.ExitStatus{}, err
	}
	progress := orWriter(attach.Err, os.Stderr)
	img, err := b.ensureImage(ctx, req.Image, progress)
	if err != nil {
		if ctx.Err() != nil {
			interrupted = true
			return backend.ExitStatus{}, interruptedStart()
		}
		return backend.ExitStatus{}, err
	}

	tempDir, err := os.MkdirTemp("", "enclave-sbx-")
	if err != nil {
		return backend.ExitStatus{}, fmt.Errorf("sbx backend: create session directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	kitDir := filepath.Join(tempDir, "kit")
	stageDir := filepath.Join(tempDir, "files")
	envFilePath := filepath.Join(tempDir, "session.env")
	for _, dir := range []string{kitDir, stageDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return backend.ExitStatus{}, fmt.Errorf("sbx backend: create session directory: %w", err)
		}
	}
	plan, err := buildPlan(planInput{
		Request:     req,
		Image:       img,
		StoreSource: b.layout.MountSource,
		KitDir:      kitDir,
		EnvFilePath: envFilePath,
		StageDir:    stageDir,
		TTY:         attach.TTY && stdinIsTerminal(attach.In),
	})
	if err != nil {
		return backend.ExitStatus{}, err
	}
	if err := os.WriteFile(filepath.Join(kitDir, "spec.yaml"), plan.KitSpec, 0o600); err != nil {
		return backend.ExitStatus{}, fmt.Errorf("sbx backend: write sandbox kit: %w", err)
	}
	if err := os.WriteFile(envFilePath, plan.EnvFile, 0o600); err != nil {
		return backend.ExitStatus{}, fmt.Errorf("sbx backend: write session env file: %w", err)
	}

	logx.Infof("Creating sandbox %s (one microVM per session).", plan.Name)
	if err := b.cli.Run(ctx, progress, plan.CreateArgs...); err != nil {
		// A failed create does not establish ownership of the sandbox name.
		if ctx.Err() != nil {
			interrupted = true
			logx.Warnf("Sandbox creation interrupted; check `sbx ls` and remove %s with `sbx rm %s` only if it belongs to this session.", plan.Name, plan.Name)
			return backend.ExitStatus{}, backend.ErrInterrupted
		}
		return backend.ExitStatus{}, fmt.Errorf("%w (if a sandbox named %s already exists, check `sbx ls` first; it may belong to another session)", err, plan.Name)
	}
	defer b.removeSandbox(plan.Name)

	if len(plan.StagedFiles) > 0 {
		if err := b.cli.Run(ctx, progress, copyArgs(plan.Name, copyInScript, plan.StagedFiles)...); err != nil {
			if ctx.Err() != nil {
				interrupted = true
				return backend.ExitStatus{}, interruptedStart()
			}
			return backend.ExitStatus{}, fmt.Errorf("sbx backend: place file mounts: %w", err)
		}
	}
	if ctx.Err() != nil {
		interrupted = true
		return backend.ExitStatus{}, interruptedStart()
	}

	if attach.OnStarted != nil {
		attach.OnStarted()
	}
	code, runErr := b.cli.Attach(plan.ExecArgs, attach)

	if len(plan.StagedFiles) > 0 {
		b.persistStagedFiles(plan, progress)
	}
	if runErr != nil {
		return backend.ExitStatus{Code: 1}, runErr
	}
	if code != 0 {
		return backend.ExitStatus{Code: code}, &backend.ExitError{Code: code}
	}
	return backend.ExitStatus{Code: 0}, nil
}

// persistStagedFiles copies writable file mounts back to their host sources:
// target to staging copy inside the sandbox, then staging copy to source.
func (b *Backend) persistStagedFiles(plan sessionPlan, progress io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := b.cli.Run(ctx, progress, copyArgs(plan.Name, copyOutScript, plan.StagedFiles)...); err != nil {
		logx.Warnf("sbx backend: could not copy file mounts out of the sandbox; host copies are unchanged: %v", err)
		return
	}
	for _, file := range plan.StagedFiles {
		data, err := os.ReadFile(file.HostStaged) // #nosec G304 -- staged path is generated under the session directory.
		if err != nil {
			logx.Warnf("sbx backend: persist file mount %s: %v", file.Target, err)
			continue
		}
		mode := os.FileMode(0o600)
		if info, err := os.Stat(file.Source); err == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(file.Source, data, mode); err != nil { // #nosec G703 -- source was supplied as a validated mount source.
			logx.Warnf("sbx backend: persist file mount %s: %v", file.Target, err)
		}
	}
}

func (b *Backend) removeSandbox(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := b.cli.Run(ctx, io.Discard, "rm", "-f", name); err != nil {
		logx.Warnf("Failed to remove sandbox %s; remove it with `sbx rm %s`: %v", name, name, err)
	}
}

func (b *Backend) Start(context.Context, backend.Request) (backend.SessionRef, error) {
	return backend.SessionRef{}, fmt.Errorf("sbx backend: detached sessions are not supported")
}

type sandboxEntry struct {
	Name      string     `json:"name"`
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	CreatedAt *time.Time `json:"created_at"`
}

// List reports Enclave-managed sandboxes. Session-name allocation relies on
// it, so concurrent sessions of one project get distinct sandbox names.
func (b *Backend) List(ctx context.Context, filter backend.SessionFilter) ([]backend.Session, error) {
	if _, err := lookPath(defaultBinary); err != nil {
		return nil, nil
	}
	out, err := b.cli.Output(ctx, "ls", "--json")
	if err != nil {
		return nil, err
	}
	var entries []sandboxEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("sbx backend: decode sbx ls output: %w", err)
	}
	sessions := make([]backend.Session, 0, len(entries))
	for _, entry := range entries {
		session, ok := sessionFromEntry(entry)
		if !ok || !matchesFilter(session, filter) {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func sessionFromEntry(entry sandboxEntry) (backend.Session, bool) {
	tool, hash, tail, ok := model.ParseManagedName(entry.Name)
	if !ok {
		return backend.Session{}, false
	}
	session := backend.Session{
		Ref:         backend.SessionRef{Name: entry.Name, ID: entry.ID},
		Tool:        tool,
		ProjectHash: hash,
		Status:      entry.Status,
		Name:        tail,
	}
	if entry.CreatedAt != nil {
		session.CreatedAt = *entry.CreatedAt
	}
	return session, true
}

func matchesFilter(session backend.Session, filter backend.SessionFilter) bool {
	name := session.Ref.Name
	switch {
	case strings.TrimSpace(filter.ExactName) != "":
		if name != strings.TrimSpace(filter.ExactName) {
			return false
		}
	case strings.TrimSpace(filter.NamePrefix) != "":
		if !strings.HasPrefix(name, strings.TrimSpace(filter.NamePrefix)) {
			return false
		}
	}
	if filter.RunningOnly && session.Status != "running" {
		return false
	}
	if filter.Tool != "" && session.Tool != filter.Tool {
		return false
	}
	if filter.ProjectHash != "" && session.ProjectHash != filter.ProjectHash {
		return false
	}
	if filter.Background != nil && *filter.Background {
		return false
	}
	return true
}

func (b *Backend) Inspect(ctx context.Context, ref backend.SessionRef) (*backend.Session, error) {
	sessions, err := b.List(ctx, backend.SessionFilter{All: true, ExactName: ref.Name})
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("sbx backend: no sandbox named %s", ref.Name)
	}
	return &sessions[0], nil
}

func (b *Backend) Attach(context.Context, backend.SessionRef, backend.AttachIO) error {
	return fmt.Errorf("sbx backend: attach is not supported")
}

func (b *Backend) Stop(ctx context.Context, ref backend.SessionRef, _ backend.StopOptions) error {
	return b.cli.Run(ctx, io.Discard, "stop", ref.Name)
}

func (b *Backend) Remove(ctx context.Context, ref backend.SessionRef) error {
	return b.cli.Run(ctx, io.Discard, "rm", "-f", ref.Name)
}

func interruptedStart() error {
	logx.Warnf("Interrupted before the session started; cleaning up.")
	return backend.ErrInterrupted
}

func stdinIsTerminal(in io.Reader) bool {
	file, ok := orStdin(in).(*os.File)
	return ok && term.IsTerminal(int(file.Fd())) // #nosec G115 -- file descriptors fit in int on all supported platforms.
}
