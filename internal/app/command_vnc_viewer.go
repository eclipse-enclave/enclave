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
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/vncviewer"
)

// vncReadyTimeout bounds the wait for the in-container VNC stack. The startup
// command runs in the background, so `enclave vnc-viewer` right after
// `enclave run` can legitimately arrive before Xvnc listens.
const vncReadyTimeout = 20 * time.Second

// vncReadyPollInterval is the gap between readiness probes.
const vncReadyPollInterval = 500 * time.Millisecond

// vncExecTimeout bounds a single password read.
const vncExecTimeout = 5 * time.Second

// runVNCViewer opens a host-installed VNC viewer on the contained display of a
// running session of the current project. The container name may be passed as
// the single positional argument. Without it the project's only VNC-enabled
// session is used.
func runVNCViewer(projectDir string, opts model.Options) int {
	if err := checkDocker(); err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	be, err := newListingBackend(opts)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	execer, ok := be.(backend.OutputExecer)
	if !ok {
		logx.Errorf("backend %s cannot read the session password", be.Name())
		return 1
	}

	project, err := config.ResolveProjectFromDir(projectDir)
	if err != nil {
		logx.Errorf("resolve project: %v", err)
		return 1
	}

	requested := ""
	if len(opts.CmdArgs) > 0 {
		requested = strings.TrimSpace(opts.CmdArgs[0])
	}

	ctx := context.Background()
	sessions, err := be.List(ctx, backend.SessionFilter{RunningOnly: true, ProjectHash: project.Hash})
	if err != nil {
		logx.Errorf("list containers: %v", err)
		return 1
	}

	session, binding, err := resolveVNCSession(sessions, requested)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	viewer := opts.VNCViewer
	if len(viewer) == 0 {
		viewer = vncviewer.DefaultViewer()
	}

	target := vncviewer.Target{
		Container: session.Ref.Name,
		Host:      vncDialHost(binding),
		Port:      binding.HostPort,
	}
	target.Password, err = awaitVNCPassword(ctx, execer, session.Ref, target)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	logx.Infof("opening %s display at %s:%s with %s", session.Ref.Name, target.Host, target.Port, viewer[0])
	if err := vncviewer.Launch(viewer, target); err != nil {
		var notFound *vncviewer.ViewerNotFoundError
		if errors.As(err, &notFound) {
			logx.Errorf("%v", err)
			logx.Infof("install a VNC viewer (Debian/Ubuntu: apt install tigervnc-viewer, macOS: built in) " +
				"or configure another one as vnc_viewer, e.g. " +
				"{\"vnc_viewer\": [\"remmina\", \"-c\", \"vnc://:{password}@{host}:{port}\"]}")
			return 1
		}
		logx.Errorf("%v", err)
		return 1
	}
	return 0
}

// resolveVNCSession picks the session whose display to open and returns its
// published RFB binding. Sessions without the vnc feature have no such
// binding, which is what distinguishes "no session" from "session without a
// VNC display" in the error messages.
func resolveVNCSession(sessions []backend.Session, requested string) (backend.Session, backend.PortMapping, error) {
	type candidate struct {
		session backend.Session
		binding backend.PortMapping
	}
	var withVNC []candidate
	var withoutVNC []string
	for _, session := range sessions {
		if session.Ref.Name == "" {
			continue
		}
		if binding, ok := vncPortBinding(session); ok {
			withVNC = append(withVNC, candidate{session: session, binding: binding})
			continue
		}
		withoutVNC = append(withoutVNC, session.Ref.Name)
	}
	sort.Slice(withVNC, func(i, j int) bool { return withVNC[i].session.Ref.Name < withVNC[j].session.Ref.Name })
	sort.Strings(withoutVNC)

	if requested != "" {
		for _, c := range withVNC {
			if c.session.Ref.Name == requested {
				return c.session, c.binding, nil
			}
		}
		for _, name := range withoutVNC {
			if name == requested {
				return backend.Session{}, backend.PortMapping{}, fmt.Errorf(
					"container %q runs without the vnc feature (restart it with `%s --features +vnc`)", requested, model.AppName)
			}
		}
		return backend.Session{}, backend.PortMapping{}, fmt.Errorf(
			"no running enclave container named %q for this project (use `%s ps` to list)", requested, model.AppName)
	}

	switch len(withVNC) {
	case 0:
		if len(withoutVNC) == 0 {
			return backend.Session{}, backend.PortMapping{}, fmt.Errorf(
				"no running enclave container for this project (start one with `%s --features +vnc`)", model.AppName)
		}
		return backend.Session{}, backend.PortMapping{}, fmt.Errorf(
			"no running enclave container for this project has a VNC display. Restart with `%s --features +vnc` (running: %s)",
			model.AppName, strings.Join(withoutVNC, ", "))
	case 1:
		return withVNC[0].session, withVNC[0].binding, nil
	default:
		names := make([]string, 0, len(withVNC))
		for _, c := range withVNC {
			names = append(names, c.session.Ref.Name)
		}
		return backend.Session{}, backend.PortMapping{}, fmt.Errorf(
			"multiple running enclave containers with a VNC display. Pass one explicitly:\n  %s", strings.Join(names, "\n  "))
	}
}

// vncPortBinding returns the session's published RFB binding, if any.
func vncPortBinding(session backend.Session) (backend.PortMapping, bool) {
	want := strconv.Itoa(vncviewer.RFBContainerPort)
	for _, binding := range session.Ports {
		if binding.ContainerPort != want || binding.HostPort == "" {
			continue
		}
		if binding.Protocol != "" && binding.Protocol != "tcp" {
			continue
		}
		return binding, true
	}
	return backend.PortMapping{}, false
}

// vncDialHost resolves the address the viewer connects to. The feature
// publishes on the host loopback. A wildcard binding still reaches the display
// through loopback, so prefer it over handing 0.0.0.0 to a viewer.
func vncDialHost(binding backend.PortMapping) string {
	switch strings.TrimSpace(binding.HostIP) {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return binding.HostIP
	}
}

// awaitVNCPassword waits until the session's VNC stack is usable: the password
// file exists and the published port accepts connections. The vnc feature
// starts its supervisor in the background, so both can lag the container.
func awaitVNCPassword(ctx context.Context, execer backend.OutputExecer, ref backend.SessionRef, target vncviewer.Target) (string, error) {
	deadline := time.Now().Add(vncReadyTimeout)
	var lastErr error
	notified := false
	for {
		password, err := readVNCPassword(ctx, execer, ref)
		switch {
		case err != nil:
			lastErr = err
		case password == "":
			lastErr = fmt.Errorf("%s is empty", vncviewer.PasswordPath)
		default:
			if err := dialVNCPort(target); err != nil {
				lastErr = err
				break
			}
			return password, nil
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("the VNC display of %s is not ready after %s: %w (check the supervisor logs in /tmp/enclave-vnc/log)",
				ref.Name, vncReadyTimeout, lastErr)
		}
		if !notified {
			logx.Infof("waiting for the VNC display of %s to come up", ref.Name)
			notified = true
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(vncReadyPollInterval):
		}
	}
}

func readVNCPassword(ctx context.Context, execer backend.OutputExecer, ref backend.SessionRef) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, vncExecTimeout)
	defer cancel()
	out, err := execer.ExecOutput(ctx, ref, []string{"cat", vncviewer.PasswordPath}, "")
	if err != nil {
		return "", fmt.Errorf("read %s in %s: %w", vncviewer.PasswordPath, ref.Name, err)
	}
	return strings.TrimSpace(out), nil
}

func dialVNCPort(target vncviewer.Target) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(target.Host, target.Port), vncExecTimeout)
	if err != nil {
		return fmt.Errorf("connect to %s:%s: %w", target.Host, target.Port, err)
	}
	return conn.Close()
}
