// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/term"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/hosthooks"
	"enclave/internal/hosthooks/builtin"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func newInvocationID() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return hex.EncodeToString(id[:])
}
func hookWarning(message string) { logx.Warnf("%s", message) }
func discoveryWarning(seen map[string]bool) func(string) {
	return func(message string) {
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(message)))
		if seen[key] {
			return
		}
		seen[key] = true
		hookWarning(message)
	}
}
func configuredHostProviders(opts model.Options, seen map[string]bool) []hosthooks.Provider {
	if seen == nil {
		seen = map[string]bool{}
	}
	warn := discoveryWarning(seen)
	if os.Getenv(hosthooks.ChildEnv) != "" {
		return nil
	}
	home, err := config.ResolveHostHomeReadOnly()
	if err != nil {
		warn(err.Error())
		return nil
	}
	root := config.HostExtensionsDir(home)
	providers, warnings := hosthooks.Discover(hosthooks.Roots{User: config.HostHooksDir(home), Features: filepath.Join(root, "features"), Tools: filepath.Join(root, "tools")}, opts.Tool)
	for _, warning := range warnings {
		warn(warning)
	}
	providers = append(providers, builtin.Tint())
	configs := config.EffectiveHostHooks(opts)
	for id := range configs {
		if strings.HasPrefix(id, "tool:") && id != "tool:"+opts.Tool {
			delete(configs, id)
		}
	}
	return hosthooks.Activate(providers, configs, warn)
}
func hookRequest(action string, opts model.Options, profile model.Profile, project model.Project, id string) hosthooks.Request {
	entrypoint := strings.Fields(profile.Command)
	argv := append(append([]string{}, entrypoint...), opts.CmdArgs...)
	explicit := false
	if opts.Shell {
		argv = append([]string{"/bin/bash"}, opts.CmdArgs...)
	}
	if action == "exec" {
		argv = append([]string{}, opts.CmdArgs...)
		explicit = len(argv) > 0
		if !explicit {
			argv = []string{"/bin/bash"}
		}
	}
	stdoutIsTTY := term.IsTerminal(int(os.Stdout.Fd())) // #nosec G115 -- stdout file descriptors fit in int on supported hosts.
	return hosthooks.Request{
		SchemaVersion: hosthooks.Version, InvocationID: id,
		Action: action, Backend: opts.Backend, Background: opts.Background, Shell: opts.Shell,
		Tool:     hosthooks.Tool{Name: opts.Tool, Entrypoint: entrypoint},
		Command:  hosthooks.Command{Argv: argv, Explicit: explicit},
		Project:  hosthooks.Project{Path: project.Dir, Hash: project.Hash},
		Terminal: hosthooks.Terminal{StdoutIsTTY: stdoutIsTTY, ColorSuppressed: logx.ColorSuppressedByEnv()},
	}
}
func prepareHostLaunch(input *CommandInput, opts model.Options, profile model.Profile, project model.Project) bool {
	if input.Replayed || os.Getenv(hosthooks.ChildEnv) != "" {
		return true
	}
	if input.HostHookWarnings == nil {
		input.HostHookWarnings = map[string]bool{}
	}
	providers := configuredHostProviders(opts, input.HostHookWarnings)
	if len(providers) == 0 {
		return true
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env, restart := (hosthooks.Runner{Warn: hookWarning}).Prepare(ctx, providers, hookRequest(input.Action, opts, profile, project, input.InvocationID), os.Environ())
	if ctx.Err() != nil {
		return false
	}
	if restart {
		record := replayRecord{Version: 1, PID: os.Getpid(), InvocationID: input.InvocationID, Tool: opts.Tool, Backend: opts.Backend, ToolSource: input.Sources.Tool, BackendSource: input.Sources.Backend}
		for key := range input.HostHookWarnings {
			record.WarningKeys = append(record.WarningKeys, key)
		}
		sort.Strings(record.WarningKeys)
		if err := replayHost(input.OriginalArgs, env, record); err != nil {
			hookWarning("host process replay failed; continuing without startup environment changes: " + err.Error())
		}
		return true
	}
	applyHostEnvironment(env)
	return true
}
func applyHostEnvironment(env []string) {
	values := hosthooks.EnvironmentMap(env)
	for key := range hosthooks.EnvironmentMap(os.Environ()) {
		if _, ok := values[key]; !ok {
			_ = os.Unsetenv(key)
		}
	}
	for key, value := range values {
		_ = os.Setenv(key, value)
	}
}
func targetHostOptions(target model.TerminalTarget, fallback model.Options) model.Options {
	if target.Tool == "" || target.ProjectDir == "" {
		return fallback
	}
	global, project, _, err := config.LoadDefaults(target.ProjectDir)
	if err != nil {
		logx.Debugf("resolve target host hooks: %v", err)
		return fallback
	}
	opts, _, _ := config.ResolveOptionsForTool(model.Options{}, model.OptionSources{}, global, project, target.Tool)
	return opts
}
func terminalLifecycle(action string, opts model.Options, profile model.Profile, project model.Project, id string, caches ...map[string]bool) model.TerminalLifecycle {
	seen := map[string]bool{}
	if len(caches) > 0 && caches[0] != nil {
		seen = caches[0]
	}
	return func(ctx context.Context, target model.TerminalTarget, handled ...os.Signal) func(error) {
		targetOpts := targetHostOptions(target, opts)
		if target.Tool != "" {
			opts.Tool = target.Tool
		}
		if target.ProjectDir != "" {
			project.Dir = target.ProjectDir
			if resolved, err := config.ResolveProjectFromDir(target.ProjectDir); err == nil {
				project = resolved
			}
		}
		if paths, err := config.ResolvePaths(); err == nil {
			if targetProfile, err := config.LoadProfileQuiet(paths, opts.Tool); err == nil {
				profile = targetProfile
			}
		}
		request := hookRequest(action, opts, profile, project, id)
		request.Session = &hosthooks.Session{Name: target.Name, ID: target.ID}
		request.AttachmentID = newInvocationID()
		if !request.Terminal.StdoutIsTTY || request.Background || os.Getenv(hosthooks.ChildEnv) != "" {
			return func(error) {}
		}
		scope := (hosthooks.Runner{Warn: hookWarning}).Begin(ctx, configuredHostProviders(targetOpts, seen), request, os.Environ(), os.Stdout, handled...)
		return func(err error) {
			outcome := hosthooks.Outcome{Status: "unknown"}
			if err != nil {
				outcome.Status = "failed"
				var exit *backend.ExitError
				if errors.As(err, &exit) {
					outcome.ExitCode = &exit.Code
				}
				if errors.Is(err, backend.ErrInterrupted) {
					outcome.Status = "interrupted"
				}
			} else if action != "attach" {
				code := 0
				outcome = hosthooks.Outcome{Status: "completed", ExitCode: &code}
			}
			scope.Close(outcome)
		}
	}
}
