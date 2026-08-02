// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

// rootlessSeccompProfile is the Moby default seccomp profile plus the minimum
// delta for the nested user-namespace launcher; see seccomp/README.md.
//
//go:embed seccomp/rootless-default.json
var rootlessSeccompProfile []byte

// sandboxExecPath is the in-image wrapper that joins the sandbox namespace for
// exec'd commands; installed by the Dockerfile next to the entrypoint.
const sandboxExecPath = "/usr/local/bin/enclave-sandbox-exec"

// isRootlessDocker is a variable so tests can pin the daemon flavor.
var isRootlessDocker = dockercmd.IsRootless

// applyRootlessSandbox adapts a session spec to a rootless daemon. Rootless
// Docker maps the invoking host user to container UID 0 and everything else to
// subordinate host IDs, so a container process can only own bind-mounted host
// paths as UID 0. To keep the agent at a normal identity, the container starts
// as (rootless) root and the entrypoint forks a child user namespace that maps
// outer UID/GID 0 to the sandbox UID/GID; the agent runs there unprivileged
// with no capabilities. The custom seccomp profile permits exactly the
// namespace and mount syscalls the launcher needs.
//
// Admin sessions are excluded: they intentionally run as (rootless) root in
// the outer view, which can write the read-only-for-the-sandbox system paths
// (e.g. for package installs).
func (b *Backend) applyRootlessSandbox(ctx context.Context, req backend.Request, spec runSpec) error {
	rootless, err := isRootlessDocker(ctx)
	if err != nil {
		return fmt.Errorf("detect rootless docker: %w", err)
	}
	if !rootless {
		return nil
	}
	if req.Security.Admin {
		return nil
	}
	if req.RuntimeUIDRemap {
		return fmt.Errorf("--runtime-uid-remap is not applicable with rootless Docker; the rootless sandbox already remaps the container identity")
	}
	if user := spec.config.User; user != "" && user != "root" {
		return fmt.Errorf("running as user %q is not supported with rootless Docker; the rootless sandbox owns the container identity", user)
	}
	uid, gid := sandboxIdentity(b.opts.Host)
	profilePath, err := ensureRootlessSeccompProfile(b.opts.Host.Home)
	if err != nil {
		return fmt.Errorf("materialize rootless seccomp profile: %w", err)
	}
	spec.config.User = "root"
	spec.config.Env = append(spec.config.Env,
		model.EnvRootlessSandbox+"=1",
		model.EnvSandboxUID+"="+uid,
		model.EnvSandboxGID+"="+gid,
	)
	spec.hostConfig.SecurityOpt = append(spec.hostConfig.SecurityOpt, "seccomp="+profilePath)
	return nil
}

// wrapSandboxExec routes an exec through the sandbox-exec wrapper on a
// rootless daemon, so exec'd processes join the launcher's child namespace and
// run under the same identity and read-only system view as the session
// process. An explicit root exec (--admin) stays in the outer namespace by
// design: that is the rootless equivalent of a privileged shell and the only
// place package installs work. The wrapper itself must start as (rootless)
// root to be allowed to join the namespace.
func (b *Backend) wrapSandboxExec(ctx context.Context, argv []string, user string) ([]string, string) {
	if user == "root" || len(argv) == 0 {
		return argv, user
	}
	rootless, err := isRootlessDocker(ctx)
	if err != nil || !rootless {
		return argv, user
	}
	return append([]string{sandboxExecPath}, argv...), "root"
}

// sandboxIdentity is the nonzero UID/GID the agent reports inside the child
// namespace. It follows the host identity for familiarity; a root-invoked
// enclave falls back to the conventional 1000 because the sandbox exists
// precisely to avoid UID 0.
func sandboxIdentity(host model.Host) (string, string) {
	uid := strings.TrimSpace(host.UID)
	gid := strings.TrimSpace(host.GID)
	if v, err := strconv.Atoi(uid); err != nil || v <= 0 {
		uid = "1000"
	}
	if v, err := strconv.Atoi(gid); err != nil || v <= 0 {
		gid = "1000"
	}
	return uid, gid
}

// ensureRootlessSeccompProfile writes the embedded profile to its host cache
// path when missing or stale and returns the path.
func ensureRootlessSeccompProfile(home string) (string, error) {
	path := config.HostSeccompProfilePath(home)
	// `docker run --security-opt seccomp=` resolves relative to the caller's
	// working directory, so an unresolved host home would both litter the CWD
	// and break sessions started from elsewhere.
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("host cache root did not resolve to an absolute path: %q", path)
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, rootlessSeccompProfile) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, rootlessSeccompProfile, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
