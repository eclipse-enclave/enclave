// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/docker"
	"enclave/internal/hosthooks"
	"enclave/internal/model"
)

func isolateHostStartup(t *testing.T, args []string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv(model.EnvHome, root)
	t.Setenv("GENERIC_PREPARED", "")
	t.Setenv(hosthooks.ReplayEnv, "")
	if err := os.Unsetenv("GENERIC_PREPARED"); err != nil {
		t.Fatal(err)
	}
	setRunningAsRoot(t, false)
	previousArgs, previousStdin, previousStderr, previousReexec, previousPing := os.Args, os.Stdin, os.Stderr, hostReexec, dockerPing
	t.Cleanup(func() {
		os.Args, os.Stdin, os.Stderr, hostReexec, dockerPing = previousArgs, previousStdin, previousStderr, previousReexec, previousPing
	})
	os.Args = append([]string{"enclave"}, args...)

	provider := filepath.Join(config.HostHooksDir(home), "fixture")
	if err := os.MkdirAll(provider, 0700); err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, filepath.Join(provider, "manifest.json"), `{"schemaVersion":"1","phases":{"prepare":"prepare"}}`, 0600)
	writeAppFile(t, filepath.Join(provider, "prepare"), "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{\"schemaVersion\":\"1\",\"hostEnvironment\":{\"setIfAbsent\":{\"GENERIC_PREPARED\":\"yes\"}},\"restartHostProcess\":true}'\n", 0700)
	path, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, path, `{"host_hooks":{"user:fixture":{"enabled":true}}}`, 0600)
	return home
}

func hostPipeInput(t *testing.T, input string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	if _, err := writer.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
}

type hostStartupExit int

func runHostStartup(args []string) (code int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if exit, ok := recovered.(hostStartupExit); ok {
				code = int(exit)
			} else {
				panic(recovered)
			}
		}
	}()
	return Run(args)
}

func stubHostReplay(t *testing.T) *int {
	t.Helper()
	restarts := new(int)
	hostReexec = func(_ string, argv, env []string) error {
		*restarts++
		if *restarts > 1 {
			t.Fatal("repeated host replay")
		}
		values := hosthooks.EnvironmentMap(env)
		t.Setenv("GENERIC_PREPARED", values["GENERIC_PREPARED"])
		t.Setenv(hosthooks.ReplayEnv, values[hosthooks.ReplayEnv])
		panic(hostStartupExit(Run(argv[1:])))
	}

	return restarts
}

func TestHostHooksReexecStartupKeepsInteractiveChoicesWithoutSavedDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "unset choices"},
		{name: "explicit auto choices", args: []string{"--tool", "auto", "--backend", "auto"}},
		{name: "implicit exec tool", args: []string{"exec", "--name", "foo", "codex"}},
		{name: "explicit auto exec tool", args: []string{"exec", "--tool=auto", "--backend=auto", "--", "codex"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateHostStartup(t, tc.args)
			toolPrompt := stubToolResolution(t, []string{"claude", "codex"}, true, "claude")
			stubBackendResolution(t, []string{backend.NamePodman, backend.NameDocker}, true)
			unsaved := errors.New("choice persistence disabled")
			saveToolChoice = func(string, string) (string, error) { return "", unsaved }
			backendPrompts := 0
			saveBackendChoice = func(_ string, choice string) (string, error) {
				backendPrompts++
				if choice != backend.NamePodman {
					t.Fatalf("backend choice = %q", choice)
				}
				return "", unsaved
			}
			hostPipeInput(t, "podman\nagent input\n")
			restarts := stubHostReplay(t)
			reachedEngine := 0
			dockerPing = func(context.Context) error {
				reachedEngine++
				if docker.Binary() != backend.NamePodman || os.Getenv("GENERIC_PREPARED") != "yes" {
					t.Fatalf("restarted with backend=%q hint=%q", docker.Binary(), os.Getenv("GENERIC_PREPARED"))
				}
				if _, present := os.LookupEnv(hosthooks.ReplayEnv); present {
					t.Fatal("replay marker leaked to engine helpers")
				}
				return errors.New("stop before contacting an engine")
			}
			if code := runHostStartup(tc.args); code != 1 {
				t.Fatalf("startup exit = %d, want engine-check failure", code)
			}
			if *restarts != 1 || reachedEngine != 1 || len(toolPrompt.Offered) != 1 || backendPrompts != 1 {
				t.Fatalf("restarts=%d engine checks=%d tool prompts=%d backend prompts=%d; want one each", *restarts, reachedEngine, len(toolPrompt.Offered), backendPrompts)
			}
			remaining, err := io.ReadAll(os.Stdin)
			if err != nil || string(remaining) != "agent input\n" {
				t.Fatalf("remaining stdin = %q, err=%v", remaining, err)
			}
		})
	}
}

func TestHostHooksReplayReportsStartupWarningsOnce(t *testing.T) {
	args := []string{"continue", "--tool=custom", "--backend=docker", "--auth-name=test", "--auth-scope=project"}
	home := isolateHostStartup(t, args)
	stubBackendResolution(t, nil, false)
	fixtures := map[string]string{
		filepath.Join(config.HostExtensionsDir(home), "tools", "custom", "spec.yaml"):                  "schemaVersion: \"1\"\nkind: sandbox\nname: custom\nsandbox:\n  image: unused\n  entrypoint: {run: [claude]}\n  resumeArgs: [--resume]\n",
		filepath.Join(config.HostExtensionsDir(home), "tools", "custom", "files", "unused", "payload"): "unused",
		filepath.Join(config.HostCommandsHostDir(home), "run"):                                         "#!/bin/sh\nexit 0\n",
	}
	for path, contents := range fixtures {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"session_tint":"invalid","host_hooks":{"user:fixture":{"enabled":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	os.Stderr = output
	restarts := stubHostReplay(t)
	dockerPing = func(context.Context) error { return errors.New("stop before contacting an engine") }
	if code := runHostStartup(args); code != 1 || *restarts != 1 {
		t.Fatalf("exit=%d restarts=%d", code, *restarts)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range []string{"session_tint", "--auth-name is ignored", "is shadowed by a built-in command", "does not define continue behavior", "sandbox.image", "is not a recognized files/ entry"} {
		if count := strings.Count(string(raw), warning); count != 1 {
			t.Fatalf("warning %q appeared %d times: %s", warning, count, raw)
		}
	}
}

func TestHostHooksOneReplayWithMultipleProvidersAndRemovedHook(t *testing.T) {
	args := []string{"--tool=claude", "--backend=docker"}
	home := isolateHostStartup(t, args)
	stubBackendResolution(t, nil, false)
	second := filepath.Join(config.HostHooksDir(home), "second")
	writeAppFile(t, filepath.Join(second, "manifest.json"), `{"schemaVersion":"1","phases":{"prepare":"prepare"}}`, 0600)
	writeAppFile(t, filepath.Join(second, "prepare"), "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{\"schemaVersion\":\"1\",\"hostEnvironment\":{\"set\":{\"GENERIC_SECOND\":\"yes\"}},\"restartHostProcess\":true}'\n", 0700)
	path, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, path, `{"host_hooks":{"user:fixture":{"enabled":true},"user:second":{"enabled":true}}}`, 0600)
	restarts, engineChecks := 0, 0
	hostReexec = func(_ string, argv, env []string) error {
		restarts++
		if restarts > 1 {
			t.Fatal("multiple process replays")
		}
		values := hosthooks.EnvironmentMap(env)
		if values["GENERIC_SECOND"] != "yes" || values["GENERIC_PREPARED"] != "yes" {
			t.Fatal(values)
		}
		if err := os.RemoveAll(config.HostHooksDir(home)); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GENERIC_PREPARED", values["GENERIC_PREPARED"])
		t.Setenv(hosthooks.ReplayEnv, values[hosthooks.ReplayEnv])
		panic(hostStartupExit(Run(argv[1:])))
	}
	dockerPing = func(context.Context) error { engineChecks++; return errors.New("stop before contacting an engine") }
	if code := runHostStartup(args); code != 1 || restarts != 1 || engineChecks != 1 {
		t.Fatal(code, restarts, engineChecks)
	}
}
func TestHostHooksReplayFailureContinuesWithoutPatch(t *testing.T) {
	args := []string{"--tool=claude", "--backend=docker"}
	isolateHostStartup(t, args)
	stubBackendResolution(t, nil, false)
	restarts, engineChecks := 0, 0
	hostReexec = func(_ string, argv, env []string) error { restarts++; return errors.New("executable unavailable") }
	dockerPing = func(context.Context) error {
		engineChecks++
		if _, ok := os.LookupEnv("GENERIC_PREPARED"); ok {
			t.Fatal("failed replay changed parent environment")
		}
		return errors.New("stop before contacting an engine")
	}
	if code := Run(args); code != 1 || restarts != 1 || engineChecks != 1 {
		t.Fatal(code, restarts, engineChecks)
	}
}
