// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package vncviewer launches a host-installed VNC viewer against the contained
// display of a running enclave session (the vnc feature).
package vncviewer

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Container-side contract of the vnc feature. The supervisor writes the
// per-session plaintext password here. See extensions/features/vnc/README.md.
const (
	// RFBContainerPort is the container port the vnc feature publishes.
	RFBContainerPort = 5900
	// PasswordPath is the in-container plaintext password file.
	PasswordPath = "/tmp/enclave-vnc/vnc-password" // #nosec G101 -- in-container file path, not a credential.
)

// Placeholders substituted into a viewer command.
const (
	placeholderHost      = "{host}"
	placeholderPort      = "{port}"
	placeholderPassword  = "{password}"
	placeholderContainer = "{container}"
)

// Password environment variables exported to the viewer. xtigervncviewer reads
// VNC_PASSWORD, which is why the default Linux viewer needs no placeholder at
// all. ENCLAVE_VNC_PASSWORD is the stable name for custom viewers.
const (
	EnvVNCPassword     = "VNC_PASSWORD"         // #nosec G101 -- variable name, not a credential.
	EnvEnclavePassword = "ENCLAVE_VNC_PASSWORD" // #nosec G101 -- variable name, not a credential.
)

// Target is the resolved connection info for one session's display. It is
// never serialized. The password only ever reaches the viewer's environment.
type Target struct {
	Container string
	Host      string
	Port      string
	Password  string `json:"-"` // #nosec G117 -- carries a live secret, so it must never be marshaled.
}

// DefaultViewer returns the viewer command used when vnc_viewer is unset.
func DefaultViewer() []string { return defaultViewerFor(runtime.GOOS) }

// defaultViewerFor picks the platform's zero-configuration viewer. macOS ships
// Screen Sharing, which handles vnc:// URLs including the password, so nothing
// needs installing there. Elsewhere the reference viewer is TigerVNC's, which
// takes the password from the environment.
func defaultViewerFor(goos string) []string {
	if goos == "darwin" {
		return []string{"open", "vnc://:" + placeholderPassword + "@" + placeholderHost + ":" + placeholderPort}
	}
	return []string{"xtigervncviewer", placeholderHost + ":" + placeholderPort}
}

// BuildArgv substitutes the target into a viewer command. A command that
// references neither {host} nor {port} gets "<host>:<port>" appended, so a
// bare ["remmina"] or ["vncviewer"] works as configured.
func BuildArgv(viewer []string, target Target) ([]string, error) {
	if len(viewer) == 0 {
		return nil, fmt.Errorf("empty viewer command")
	}
	if strings.TrimSpace(target.Host) == "" || strings.TrimSpace(target.Port) == "" {
		return nil, fmt.Errorf("viewer target is missing host or port")
	}

	argv := make([]string, 0, len(viewer)+1)
	addressed := false
	for _, arg := range viewer {
		if strings.Contains(arg, placeholderHost) || strings.Contains(arg, placeholderPort) {
			addressed = true
		}
		replaced := strings.NewReplacer(
			placeholderHost, target.Host,
			placeholderPort, target.Port,
			placeholderPassword, target.Password,
			placeholderContainer, target.Container,
		).Replace(arg)
		argv = append(argv, replaced)
	}
	if !addressed {
		argv = append(argv, target.Host+":"+target.Port)
	}
	return argv, nil
}

// Launch runs the viewer in the foreground and waits for it to exit, so viewer
// errors (a rejected password, a closed display) land on the caller's terminal
// and Ctrl-C reaches the viewer. The password is passed through the child's
// environment only: never in argv, where /proc would expose it to every local
// user for the viewer's lifetime.
func Launch(viewer []string, target Target) error {
	argv, err := BuildArgv(viewer, target)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return &ViewerNotFoundError{Command: argv[0], err: err}
	}

	// #nosec G204 -- the viewer command comes from the global user config, which
	// is host-user-owned. Neither a project nor a session can set it.
	cmd := exec.Command(bin, argv[1:]...)
	cmd.Env = append(os.Environ(),
		EnvVNCPassword+"="+target.Password,
		EnvEnclavePassword+"="+target.Password,
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", argv[0], err)
	}
	return nil
}

// ViewerNotFoundError reports a viewer command that is not on PATH, so the
// caller can render the install-or-configure hint instead of a bare exec error.
type ViewerNotFoundError struct {
	Command string
	err     error
}

func (e *ViewerNotFoundError) Error() string {
	return fmt.Sprintf("viewer %q not found on PATH", e.Command)
}

func (e *ViewerNotFoundError) Unwrap() error { return e.err }
