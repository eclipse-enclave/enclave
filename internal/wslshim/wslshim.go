// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package wslshim implements the Windows enclave launcher. It forwards every
// argument verbatim to the Linux enclave binary inside a WSL2 distribution and
// parses no argv of its own. There is no native Windows implementation of
// enclave, and this package is not a step towards one.
//
// Everything except the drive-type probe is host-independent, so the package
// builds and is unit-tested on Linux even though it only runs on Windows.
package wslshim

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
)

// exitLauncherFailure follows Docker's convention for "the launcher failed, not
// the program", which keeps shim preflight errors distinguishable from exit
// codes produced by enclave itself inside the distribution.
const exitLauncherFailure = 125

// errPrefix marks every message as coming from the launcher rather than from
// enclave inside the distribution.
const errPrefix = "enclave (windows launcher):"

// Run forwards args to the Linux enclave binary in a WSL2 distribution and
// returns the exit code to hand back to the shell.
func Run(args []string) int {
	// The shim must outlive its child so the child owns the decision of what
	// Ctrl-C means. wsl.exe forwards the console control event on its own.
	signal.Ignore(os.Interrupt)

	return shim{
		getwd:        os.Getwd,
		lookPath:     exec.LookPath,
		environ:      os.Environ(),
		driveType:    systemDriveType,
		resolveDrive: systemDriveResolver,
		spawn:        spawnCommand,
		capture:      captureCommand,
		stderr:       os.Stderr,
	}.execute(args)
}

// shim holds the host interactions the launcher needs, so the orchestration
// below can be exercised without a Windows host or a WSL2 installation.
type shim struct {
	getwd     func() (string, error)
	lookPath  func(string) (string, error)
	environ   []string
	driveType driveTyper
	// resolveDrive answers what a mapped network drive points at, which is how
	// a WSL share reached through a drive letter stays usable.
	resolveDrive driveResolver
	spawn        func(exe, cmdLine string, env []string) error
	capture      func(exe, cmdLine string, env []string) ([]byte, []byte, error)
	stderr       io.Writer
}

func (s shim) execute(args []string) int {
	cwd, err := s.getwd()
	if err != nil {
		return s.fail("cannot determine the current directory: %v", err)
	}

	// Named t rather than target so it does not shadow the type.
	t, err := resolveTarget(cwd, lookupIn(s.environ), s.driveType, s.resolveDrive)
	if err != nil {
		return s.fail("%v", err)
	}
	s.warn(t.Warnings)

	wslExe, err := s.lookPath("wsl.exe")
	if err != nil {
		return s.fail("wsl.exe was not found on PATH. Enclave on Windows runs " +
			"inside WSL2; install it with `wsl --install` and set up a Linux " +
			"distribution first.")
	}

	env, warnings, err := forwardEnv(s.environ)
	if err != nil {
		return s.fail("%v", err)
	}
	s.warn(warnings)

	binary, cdSupported, err := s.resolveBinary(wslExe, t, env)
	if err != nil {
		return s.fail("%v", err)
	}

	cmdLine, err := buildCommandLine(wslExe, wslArgs(t, binary, cdSupported, args))
	if err != nil {
		return s.fail("%v", err)
	}

	if err := s.spawn(wslExe, cmdLine, env); err != nil {
		if code, ok := exitCodeFromRunError(err); ok {
			return code
		}
		return s.fail("failed to start wsl.exe: %v", err)
	}
	return 0
}

// resolveBinary asks the distribution where its enclave binary is, because
// ~/.local/bin is only on PATH via ~/.profile, which a non-login shell does not
// source. The probe carries no user argv, so it is quoting-safe, and its result
// lets the real invocation use an absolute path with no shell in between.
//
// The same round trip detects whether this wsl.exe understands --cd, which
// builds older than early 2021 do not.
func (s shim) resolveBinary(wslExe string, t target, env []string) (string, bool, error) {
	for _, cdSupported := range []bool{true, false} {
		cmdLine, err := buildCommandLine(wslExe, probeArgs(t.Distro, cdSupported))
		if err != nil {
			return "", false, err
		}

		stdout, stderr, runErr := s.capture(wslExe, cmdLine, env)
		code, isExit := exitCodeFromRunError(runErr)
		switch {
		case runErr == nil:
			path := strings.TrimSpace(decodeWSLOutput(stdout))
			if !strings.HasPrefix(path, "/") {
				return "", false, fmt.Errorf("the enclave probe in %s returned %q instead of an absolute path", t.describe(), path)
			}
			return path, cdSupported, nil
		case isExit && code == probeNotFoundCode:
			// A conclusive answer: the distribution ran the probe and has no
			// enclave binary. Retrying would only repeat it.
			return "", false, fmt.Errorf("enclave is not installed in %s. "+
				"Install the Linux build inside the distribution (see the Windows "+
				"section of the installation docs); the Windows binary is only a launcher", t.describe())
		case cdSupported:
			// --cd may be the reason this failed, so retry without it before
			// blaming the distribution.
			continue
		default:
			return "", false, fmt.Errorf("wsl.exe failed to start enclave in %s: %s%s",
				t.describe(), summarize(stderr, runErr), s.distroHint(wslExe, env))
		}
	}
	// Unreachable: the loop either returns or exhausts both attempts above.
	return "", false, fmt.Errorf("could not locate enclave in %s", t.describe())
}

// distroHint lists the installed distributions, which is the information a
// failed -d almost always needs. It is only consulted on the error path, so the
// happy path pays for no extra round trip.
func (s shim) distroHint(wslExe string, env []string) string {
	cmdLine, err := buildCommandLine(wslExe, []string{"--list", "--quiet"})
	if err != nil {
		return ""
	}
	stdout, _, runErr := s.capture(wslExe, cmdLine, env)
	if runErr != nil {
		return ""
	}
	distros := parseDistroList(stdout)
	if len(distros) == 0 {
		return "\nNo WSL distributions are installed; create one with `wsl --install -d Ubuntu`."
	}
	return "\nInstalled distributions: " + strings.Join(distros, ", ")
}

func (s shim) warn(messages []string) {
	for _, message := range messages {
		_, _ = fmt.Fprintf(s.stderr, "%s warning: %s\n", errPrefix, message)
	}
}

func (s shim) fail(format string, args ...any) int {
	_, _ = fmt.Fprintf(s.stderr, "%s %s\n", errPrefix, fmt.Sprintf(format, args...))
	return exitLauncherFailure
}

// exitCodeFromRunError reports the child's exit code. A failure to start the
// child at all is not an exit code and is reported as such.
func exitCodeFromRunError(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}

// summarize prefers wsl.exe's own diagnostics, which it emits as UTF-16LE, and
// falls back to the Go-level error when it said nothing useful.
func summarize(stderr []byte, err error) string {
	if message := strings.TrimSpace(decodeWSLOutput(stderr)); message != "" {
		return collapseLines(message)
	}
	if err != nil {
		return err.Error()
	}
	return "no diagnostic output"
}

func collapseLines(s string) string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == '\r' || r == '\n' })
	for i, field := range fields {
		fields[i] = strings.TrimSpace(field)
	}
	return strings.Join(fields, "; ")
}
