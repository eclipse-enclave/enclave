// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/contexttable"
	"enclave/internal/a2aagent/driver/claude"
	"enclave/internal/a2aagent/driver/codex"
	"enclave/internal/a2aagent/driver/copilot"
	"enclave/internal/a2aagent/driver/pi"
	"enclave/internal/a2aagent/executor"
	"enclave/internal/a2aagent/server"
	"enclave/internal/buildinfo"
	"enclave/internal/tools"
)

// heartbeatInterval is fixed by the Wave 2 decisions; no option exists.
const heartbeatInterval = 30 * time.Second

// errNoDriver is what serve reports for a tool without an a2a block (P9).
var errNoDriver = errors.New("this tool has no sandbox.a2a block, so it cannot run in serve mode")

// harnessEnv is the value of a2a.EnvHarness: the sandbox.a2a block plus the profile's yolo flag.
type harnessEnv struct {
	tools.A2ASpec
	YoloFlag string `json:"yoloFlag,omitempty"`
}

// newDriver builds the harness driver and card skills from the a2a block in the environment.
func newDriver(environ []string, log io.Writer) (a2aagent.Driver, []a2a.AgentSkill, error) {
	raw := lookupEnv(environ, a2a.EnvHarness)
	if raw == "" {
		return nil, nil, errNoDriver
	}
	var harness harnessEnv
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&harness); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", a2a.EnvHarness, err)
	}
	spec := harness.A2ASpec
	var extra []string
	switch {
	case lookupEnv(environ, a2a.EnvYolo) != "1":
		_, _ = fmt.Fprintf(log, "enclave-a2a-agent serve: %s is not 1; the worker runs without bypassed permissions\n", a2a.EnvYolo)
	case harness.YoloFlag == "":
		_, _ = fmt.Fprintln(log, "enclave-a2a-agent serve: the tool has no yolo flag; the worker runs without bypassed permissions")
	default:
		extra = []string{harness.YoloFlag}
	}
	var driver a2aagent.Driver
	switch spec.EventFormat {
	case tools.EventFormatClaudeStreamJSON:
		d, err := claude.New(spec, claude.Options{ExtraArgs: extra, Log: log})
		if err != nil {
			return nil, nil, err
		}
		driver = d
	case tools.EventFormatCodexExecJSON:
		d, err := codex.New(spec, codex.Options{ExtraArgs: extra, Log: log})
		if err != nil {
			return nil, nil, err
		}
		driver = d
	case tools.EventFormatCopilotJSON:
		d, err := copilot.New(spec, copilot.Options{ExtraArgs: extra, Log: log})
		if err != nil {
			return nil, nil, err
		}
		driver = d
	case tools.EventFormatPiJSON:
		d, err := pi.New(spec, pi.Options{ExtraArgs: extra, Log: log})
		if err != nil {
			return nil, nil, err
		}
		driver = d
	default:
		return nil, nil, fmt.Errorf("no driver for a2a event format %q", spec.EventFormat)
	}
	skills := make([]a2a.AgentSkill, 0, len(spec.Skills))
	for _, sk := range spec.Skills {
		tags := sk.Tags
		if tags == nil {
			tags = []string{}
		}
		skills = append(skills, a2a.AgentSkill{ID: sk.ID, Name: sk.Name, Description: sk.Description, Tags: tags})
	}
	return driver, skills, nil
}

func lookupEnv(environ []string, key string) string {
	value := ""
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			value = v
		}
	}
	return value
}

// runServe runs the worker adapter as the session's main process. Owned by A10.
func runServe(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: enclave-a2a-agent serve")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "enclave-a2a-agent serve: %v\n", err)
		return 1
	}
	return 0
}

// serve follows the A8 start order: table, executor, then the server, which starts Run before it listens.
func serve(ctx context.Context, environ []string) error {
	cfg, err := a2aagent.LoadConfig(environ)
	if err != nil {
		return err
	}
	if cfg.Role != a2a.RoleWorker {
		return fmt.Errorf("role %q does not serve; only workers run the adapter", cfg.Role)
	}
	driver, skills, err := newDriver(environ, os.Stderr)
	if err != nil {
		return err
	}
	if err := driver.CheckVersion(ctx); err != nil {
		return fmt.Errorf("harness %s: %w", driver.Name(), err)
	}
	tbl, err := contexttable.Open(contexttable.Options{
		Dir: cfg.TableDir,
		Header: a2aagent.TableHeader{
			Group: cfg.Group, GroupGeneration: cfg.GroupGeneration,
			Worker: cfg.Member, WorkerGeneration: cfg.MemberGeneration,
		},
		QueueDepth:     cfg.QueueDepth,
		RecordLimit:    cfg.RecordLimit,
		RecordMaxAge:   cfg.RecordMaxAge,
		ContextTTL:     cfg.ContextTTL,
		TranscriptRoot: transcriptRoot(cfg),
	})
	if err != nil {
		return err
	}
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	ex, err := executor.New(executor.Options{
		Table: tbl, Driver: driver,
		Group: cfg.Group, GroupGeneration: cfg.GroupGeneration,
		Worker: cfg.Member, WorkerGeneration: cfg.MemberGeneration,
		WorkDir:            workDir,
		TaskDeadline:       cfg.TaskDeadline,
		TerminationTimeout: cfg.TerminationTimeout,
		MaxResultBytes:     cfg.MaxResultBytes,
		HeartbeatInterval:  heartbeatInterval,
		Log:                os.Stderr,
	})
	if err != nil {
		return err
	}
	srv, err := server.New(server.Options{
		Config: cfg, Table: tbl, Executor: ex, Skills: skills,
		Version: buildinfo.Read().Version, Log: os.Stderr,
	})
	if err != nil {
		return err
	}
	return srv.Serve(ctx)
}

// transcriptRoot is the config store that holds a2a/<memberGeneration>/; transcript paths are relative to it.
func transcriptRoot(cfg a2aagent.Config) string {
	dir := filepath.Clean(cfg.TableDir)
	if filepath.Base(dir) != cfg.MemberGeneration || filepath.Base(filepath.Dir(dir)) != "a2a" {
		return ""
	}
	return filepath.Dir(filepath.Dir(dir))
}
