// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"strings"

	"enclave/internal/logx"
	"enclave/internal/model"
)

// runningAsRoot checks both IDs: the image bakes the real UID (user.Current),
// and under sudo both are 0.
var runningAsRoot = func() bool {
	return os.Getuid() == 0 || os.Geteuid() == 0
}

// rootAllowed reports whether the user opted in to running as root, through
// --allow-root or ENCLAVE_ALLOW_ROOT. There is deliberately no config key, so
// neither global nor project config can grant it.
func rootAllowed(flag bool) bool {
	return flag || envTruthy(os.LookupEnv(model.EnvAllowRoot))
}

// checkRootGuard refuses to run as root unless allowed. As root the image would
// be built for UID 0, so the agent would be host root on bind mounts under
// rootful Docker, and every file Enclave writes becomes root-owned, which
// breaks later runs as the regular user. The opt-in only skips this check; it
// does not make root runs supported.
func checkRootGuard(allowed bool) error {
	if !runningAsRoot() {
		return nil
	}
	if allowed {
		logx.Warnf("running as root: the opt-in only skips the root check; root runs are unsupported (images cannot be built for UID 0 yet), and files Enclave writes are owned by root")
		return nil
	}
	return rootRefusal()
}

// rootRefusal stays on one line: under --json it becomes the result
// envelope's error field.
func rootRefusal() error {
	who := "Run enclave as a regular user."
	if sudoUser := strings.TrimSpace(os.Getenv("SUDO_USER")); sudoUser != "" && sudoUser != "root" {
		who = fmt.Sprintf("enclave was started through sudo; run it as %s without sudo.", sudoUser)
	}
	return fmt.Errorf("refusing to run as root: the agent would run as UID 0, which is host root on bind-mounted directories under rootful Docker, and files Enclave writes would be owned by root. %s "+
		"To use Docker without sudo, add your user to the docker group and sign in again (see https://docs.docker.com/engine/install/linux-postinstall/), or use rootless podman with --backend podman. "+
		"Pass --allow-root or set %s=1 to skip this check", who, model.EnvAllowRoot)
}

func envTruthy(value string, set bool) bool {
	if !set {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
