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
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// dockerBinary is the docker CLI executable. It is a variable so tests can
// point it at a stub.
var dockerBinary = "docker"

// cliError wraps a failed `docker` invocation, retaining stderr and the exit
// code so callers can classify failures (see IsNotFound and the run helpers).
type cliError struct {
	args   []string
	code   int
	stderr string
	err    error
}

func (e *cliError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" && e.err != nil {
		msg = e.err.Error()
	}
	if msg == "" {
		msg = "command failed"
	}
	return fmt.Sprintf("docker %s: %s", strings.Join(e.args, " "), msg)
}

func (e *cliError) Unwrap() error {
	return e.err
}

// IsNotFound reports whether err came from a `docker` command that failed
// because the target image, container, or volume does not exist. It replaces
// the SDK's errdefs.IsNotFound by matching the daemon's stderr message.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var ce *cliError
	if !errors.As(err, &ce) {
		return false
	}
	// Match the daemon's not-found phrasings ("No such container/image/volume",
	// generic inspect's "No such object") rather than a bare "not found", which
	// also appears in unrelated failures like "executable file not found".
	s := strings.ToLower(ce.stderr)
	for _, phrase := range []string{
		"no such container",
		"no such image",
		"no such volume",
		"no such network",
		"no such object",
	} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

// IsCLIUnavailable reports whether err means the docker binary itself could
// not be found or executed, as opposed to a docker command that ran and
// failed.
func IsCLIUnavailable(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
}

// IsSocketPermissionDenied reports whether err came from a docker command
// that ran but was refused access to the daemon socket. The stderr phrasing
// varies across engine versions ("...connect to the Docker daemon socket",
// "...connect to the docker API"), so match only the stable prefix.
func IsSocketPermissionDenied(err error) bool {
	var ce *cliError
	if !errors.As(err, &ce) {
		return false
	}
	return strings.Contains(strings.ToLower(ce.stderr), "permission denied while trying to connect")
}

// commandExitCode returns the process exit code of a failed exec.Command and
// whether it could be determined.
func commandExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}

// dockerExitCodeUnableToStart is the exit code `docker run` uses when it could
// not start the container at all (as opposed to the container's own non-zero
// exit). We treat it as a hard error rather than an ExitError.
const dockerExitCodeUnableToStart = 125

// capture runs `docker <args...>` and returns trimmed stdout. On failure it
// returns a *cliError carrying stderr and the exit code.
func capture(ctx context.Context, args ...string) (string, error) {
	return captureCmd(ctx, true, args...)
}

// captureCmd runs `docker <args...>` without a TTY and returns its stdout,
// trimming surrounding whitespace when trim is set. On failure it returns the
// stdout captured so far and a *cliError carrying stderr and the exit code.
func captureCmd(ctx context.Context, trim bool, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204 -- args are fixed flags and caller-controlled values passed without a shell.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		code, _ := commandExitCode(err)
		return stdout.String(), &cliError{
			args:   args,
			code:   code,
			stderr: strings.TrimSpace(stderr.String()),
			err:    err,
		}
	}
	if trim {
		return strings.TrimSpace(stdout.String()), nil
	}
	return stdout.String(), nil
}

// runMode selects the foreground/detached and TTY behaviour for `docker run`.
type runMode struct {
	Detach      bool
	Interactive bool
	TTY         bool
}

// buildRunArgs translates a ContainerConfig/HostConfig pair into the argument
// list for `docker run`. Flags come first, then the image, then the command.
func buildRunArgs(config *ContainerConfig, hostConfig *HostConfig, name string, mode runMode) []string {
	args := []string{"run"}
	if mode.Detach {
		args = append(args, "--detach")
	}
	if mode.Interactive {
		args = append(args, "--interactive")
	}
	if mode.TTY {
		args = append(args, "--tty")
	}
	if strings.TrimSpace(name) != "" {
		args = append(args, "--name", name)
	}

	var cmd []string
	if config != nil {
		if config.User != "" {
			args = append(args, "--user", config.User)
		}
		if config.WorkingDir != "" {
			args = append(args, "--workdir", config.WorkingDir)
		}
		if config.Hostname != "" {
			args = append(args, "--hostname", config.Hostname)
		}
		for _, env := range config.Env {
			args = append(args, "--env", env)
		}
		args = append(args, sortedMapFlags("--label", config.Labels)...)
		args = append(args, exposedPortFlags(config.ExposedPorts)...)
		if len(config.Entrypoint) > 0 {
			args = append(args, "--entrypoint", config.Entrypoint[0])
			cmd = append(cmd, config.Entrypoint[1:]...)
		}
		cmd = append(cmd, config.Cmd...)
	}

	if hostConfig != nil {
		if hostConfig.AutoRemove {
			args = append(args, "--rm")
		}
		if hostConfig.Privileged {
			args = append(args, "--privileged")
		}
		if hostConfig.Init != nil && *hostConfig.Init {
			args = append(args, "--init")
		}
		if !hostConfig.NetworkMode.IsEmpty() {
			args = append(args, "--network", string(hostConfig.NetworkMode))
		}
		for _, bind := range hostConfig.Binds {
			args = append(args, "--volume", bind)
		}
		args = append(args, mountFlags(hostConfig.Mounts)...)
		args = append(args, portBindingFlags(hostConfig.PortBindings)...)
		for _, host := range hostConfig.ExtraHosts {
			args = append(args, "--add-host", host)
		}
		for _, opt := range hostConfig.SecurityOpt {
			args = append(args, "--security-opt", opt)
		}
		for _, cap := range hostConfig.CapAdd {
			args = append(args, "--cap-add", cap)
		}
		for _, cap := range hostConfig.CapDrop {
			args = append(args, "--cap-drop", cap)
		}
		args = append(args, sortedMapFlags("--sysctl", hostConfig.Sysctls)...)
		args = append(args, tmpfsFlags(hostConfig.Tmpfs)...)
	}

	if config != nil && config.Image != "" {
		args = append(args, config.Image)
	}
	return append(args, cmd...)
}

// mountFlags renders Mounts as `--mount type=...,source=...,target=...` args.
func mountFlags(mounts []Mount) []string {
	out := make([]string, 0, len(mounts)*2)
	for _, m := range mounts {
		var spec strings.Builder
		fmt.Fprintf(&spec, "type=%s", m.Type)
		if m.Source != "" {
			fmt.Fprintf(&spec, ",source=%s", m.Source)
		}
		if m.Target != "" {
			fmt.Fprintf(&spec, ",target=%s", m.Target)
		}
		if m.ReadOnly {
			spec.WriteString(",readonly")
		}
		out = append(out, "--mount", spec.String())
	}
	return out
}

// portBindingFlags renders PortBindings as `--publish` args, sorted for stable
// output.
func portBindingFlags(bindings PortMap) []string {
	keys := make([]string, 0, len(bindings))
	for port := range bindings {
		keys = append(keys, string(port))
	}
	sort.Strings(keys)
	var out []string
	for _, key := range keys {
		port := Port(key)
		for _, binding := range bindings[port] {
			spec := ""
			if binding.HostIP != "" {
				spec = binding.HostIP + ":"
			}
			spec += binding.HostPort + ":" + port.Num() + "/" + port.Proto()
			out = append(out, "--publish", spec)
		}
	}
	return out
}

// exposedPortFlags renders a PortSet as `--expose` args, sorted for stability.
func exposedPortFlags(ports PortSet) []string {
	keys := make([]string, 0, len(ports))
	for port := range ports {
		keys = append(keys, string(port))
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		out = append(out, "--expose", key)
	}
	return out
}

// tmpfsFlags renders a tmpfs map as `--tmpfs path[:opts]` args, sorted.
func tmpfsFlags(tmpfs map[string]string) []string {
	keys := make([]string, 0, len(tmpfs))
	for path := range tmpfs {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys)*2)
	for _, path := range keys {
		if opts := tmpfs[path]; opts != "" {
			out = append(out, "--tmpfs", path+":"+opts)
		} else {
			out = append(out, "--tmpfs", path)
		}
	}
	return out
}

// sortedMapFlags renders a string map as repeated `<flag> key=value` args in
// sorted key order.
func sortedMapFlags(flag string, values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		out = append(out, flag, key+"="+values[key])
	}
	return out
}

// firstLine returns the first non-empty line of out.
func firstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// parseReclaimedSpace extracts the byte count from a docker prune "Total
// reclaimed space: <size>" line, returning 0 when absent.
func parseReclaimedSpace(out string) uint64 {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		const marker = "Total reclaimed space:"
		if idx := strings.Index(line, marker); idx >= 0 {
			return parseHumanBytes(strings.TrimSpace(line[idx+len(marker):]))
		}
	}
	return 0
}

// parseHumanBytes parses a Docker human-readable size such as "1.2GB", "800MB
// (66%)", or "0B" into a byte count. Docker formats these with SI units
// (1kB = 1000 bytes). Anything it cannot parse yields 0.
func parseHumanBytes(value string) uint64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	// Drop any trailing annotation like " (66%)".
	if idx := strings.IndexByte(value, '('); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	// Split the numeric prefix from the unit suffix.
	cut := len(value)
	for i, r := range value {
		if (r < '0' || r > '9') && r != '.' {
			cut = i
			break
		}
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(value[:cut]), 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(strings.TrimSpace(value[cut:]))
	var multiplier float64
	switch unit {
	case "", "b":
		multiplier = 1
	case "kb", "kib", "k":
		multiplier = 1000
	case "mb", "mib", "m":
		multiplier = 1000 * 1000
	case "gb", "gib", "g":
		multiplier = 1000 * 1000 * 1000
	case "tb", "tib", "t":
		multiplier = 1000 * 1000 * 1000 * 1000
	default:
		return 0
	}
	if number < 0 {
		return 0
	}
	return uint64(number * multiplier)
}
