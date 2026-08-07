// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

// probeNotFoundCode is the probe's exit status when no enclave binary exists in
// the distribution. It matches the shell's "command not found" convention.
const probeNotFoundCode = 127

// probeMarker prefixes the probe's answer. The probe runs a login shell, so
// /etc/profile, /etc/profile.d/*, and ~/.profile all run before it and anything
// they write to stdout — a version manager's init, a banner — arrives ahead of
// the path. The marker is what tells the two apart.
const probeMarker = "enclave-bin="

// probeScript locates the Linux enclave binary. `make install` puts it in
// ~/.local/bin, which is only on PATH via ~/.profile, so the probe runs a login
// shell first and falls back to the known install locations.
//
// The script is a fixed literal that interpolates no user input, which is what
// makes this round trip safe to hand to a shell at all.
const probeScript = `p=$(command -v enclave 2>/dev/null) || p=""
case "$p" in /*) printf "\nenclave-bin=%s\n" "$p"; exit 0 ;; esac
for c in "$HOME/.local/bin/enclave" /usr/local/bin/enclave /usr/bin/enclave; do
  if [ -x "$c" ]; then printf "\nenclave-bin=%s\n" "$c"; exit 0; fi
done
exit 127`

// cdScript changes into the project directory and execs the binary. It is only
// used when wsl.exe predates --cd. The user's arguments arrive as positional
// parameters and are forwarded through "$@", so the shell never re-splits them.
// The shell reports its own reason when the directory is unreachable, and exits
// with the launcher-failure code because it never reached enclave.
const cdScript = `cd "$1" || exit 125
bin=$2
shift 2
exec "$bin" "$@"`

// probeArgs builds the binary-resolution round trip. It carries no user argv.
func probeArgs(distro string, useCD bool) []string {
	args := distroArgs(distro)
	if useCD {
		// The path is irrelevant here; passing --cd is how support for it gets
		// detected, and / exists in every distribution.
		args = append(args, "--cd", "/")
	}
	return append(args, "-e", "/bin/sh", "-lc", probeScript)
}

// wslArgs builds the real invocation. With --cd available, -e runs the resolved
// absolute path directly, so no shell re-parses the user's arguments on the
// Linux side either.
func wslArgs(t target, binary string, cdSupported bool, userArgs []string) []string {
	args := distroArgs(t.Distro)
	if cdSupported {
		args = append(args, "--cd", t.LinuxPath, "-e", binary)
		return append(args, userArgs...)
	}

	args = append(args, "-e", "/bin/sh", "-c", cdScript, "enclave-wsl-launcher", t.LinuxPath, binary)
	return append(args, userArgs...)
}

func distroArgs(distro string) []string {
	if distro == "" {
		// No -d selects the WSL default distribution.
		return nil
	}
	return []string{"-d", distro}
}
