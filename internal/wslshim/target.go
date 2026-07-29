// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"fmt"
	"strings"
)

// Environment variables the launcher itself interprets. They are Windows-side
// controls, so they are not forwarded into the distribution.
const (
	envDistro           = "ENCLAVE_WSL_DISTRO"
	envAllowWindowsPath = "ENCLAVE_WSL_ALLOW_WINDOWS_PATH"
	envForwardEnv       = "ENCLAVE_WSL_FORWARD_ENV"
)

// Path prefixes the classifier recognizes. wsl.localhost is the current name of
// the WSL filesystem provider and wsl$ the original one; both are still served.
const (
	uncHostWSLLocal      = `wsl.localhost`
	uncHostWSLLegacy     = `wsl$`
	extendedPrefix       = `\\?\`
	extendedUNCPrefix    = `\\?\UNC\`
	deviceExtendedPrefix = `\\.\`
)

// target is where the session will run: which distribution, and the Linux path
// that corresponds to the Windows working directory.
type target struct {
	// Distro is empty when the WSL default distribution should be used.
	Distro    string
	LinuxPath string
	Warnings  []string
}

func (t target) describe() string {
	if t.Distro == "" {
		return "the default WSL distribution"
	}
	return "WSL distribution " + t.Distro
}

// lookupFunc reads one environment variable, reporting whether it was set at
// all so an empty value stays distinguishable from an absent one.
type lookupFunc func(name string) (string, bool)

func lookupIn(environ []string) lookupFunc {
	return func(name string) (string, bool) {
		// Windows environment variable names are case-insensitive, and the
		// block is ordered, so the first match wins as it does on the host.
		for _, entry := range environ {
			key, value, found := strings.Cut(entry, "=")
			if found && strings.EqualFold(key, name) {
				return value, true
			}
		}
		return "", false
	}
}

// resolveTarget decides the distribution and Linux path from the working
// directory. The working directory decides, because that is where the code
// lives: a distribution can only see its own filesystem.
func resolveTarget(cwd string, env lookupFunc, driveType driveTyper) (target, error) {
	path := stripLongPathPrefix(normalizeSeparators(cwd))

	switch {
	case isUNC(path):
		return uncTarget(path, env)
	case hasDriveLetter(path):
		return driveTarget(path, env, driveType)
	default:
		return target{}, fmt.Errorf("cannot map the current directory %q to a path inside WSL. "+
			"Run enclave from a directory inside a WSL distribution, for example "+
			`\\wsl.localhost\Ubuntu\home\you\project`, cwd)
	}
}

// uncTarget handles the happy path: a working directory served by the WSL
// filesystem provider names its own distribution.
func uncTarget(path string, env lookupFunc) (target, error) {
	host, tail := splitFirst(strings.TrimPrefix(path, `\\`))
	if !strings.EqualFold(host, uncHostWSLLocal) && !strings.EqualFold(host, uncHostWSLLegacy) {
		return target{}, fmt.Errorf("the current directory %q is on a network share, which has no "+
			"meaningful path inside a WSL distribution. Run enclave from a directory inside the "+
			"distribution instead", path)
	}

	distro, sub := splitFirst(tail)
	if distro == "" {
		return target{}, fmt.Errorf("the current directory %q names no distribution. "+
			`Use a path of the form \\wsl.localhost\<Distro>\<path>`, path)
	}

	t := target{Distro: distro, LinuxPath: toLinuxPath(sub)}
	if requested, ok := env(envDistro); ok && requested != "" && !strings.EqualFold(requested, distro) {
		// The distribution the files live in wins: another distribution simply
		// cannot see them.
		t.Warnings = append(t.Warnings, fmt.Sprintf(
			"%s is set to %q but the current directory is inside %q, which cannot be reached from %q. Using %q.",
			envDistro, requested, distro, requested, distro))
	}
	return t, nil
}

// driveTarget handles a Windows drive path. It is refused by default: /mnt/c is
// reachable but crosses the interop layer on every file access, which is slow
// enough to matter for a repository an agent is working in.
func driveTarget(path string, env lookupFunc, driveType driveTyper) (target, error) {
	letter := strings.ToLower(path[:1])
	if driveType(strings.ToUpper(path[:1])+`:\`) == driveRemote {
		return target{}, fmt.Errorf("the current directory %q is on a mapped network drive, which has "+
			"no meaningful path inside a WSL distribution. If it maps a WSL share, run enclave from the "+
			`\\wsl.localhost\<Distro>\... path itself, which PowerShell supports as a working directory`, path)
	}

	if !truthy(env(envAllowWindowsPath)) {
		return target{}, fmt.Errorf("the current directory %q is on a Windows drive. Enclave would have "+
			"to mount it through /mnt/%s, where every file access crosses the WSL interop layer and is "+
			"markedly slower. Move the project into the distribution's filesystem (for example under ~/) "+
			"and run enclave from there, or set %s=1 to accept the slowdown", path, letter, envAllowWindowsPath)
	}

	t := target{LinuxPath: "/mnt/" + letter + trimEmpty(toLinuxPath(dropDrive(path)))}
	if requested, ok := env(envDistro); ok && requested != "" {
		t.Distro = requested
	}
	return t, nil
}

// normalizeSeparators lets the classifier work on one separator. Windows
// accepts both, and Go's Getwd can return either depending on how the working
// directory was set.
func normalizeSeparators(path string) string {
	return strings.ReplaceAll(path, "/", `\`)
}

// stripLongPathPrefix unwraps the extended-length forms. Getwd does not
// normally produce them, but a caller that set the working directory through a
// raw Win32 call can.
func stripLongPathPrefix(path string) string {
	switch {
	case strings.HasPrefix(path, extendedUNCPrefix):
		return `\\` + path[len(extendedUNCPrefix):]
	case strings.HasPrefix(path, extendedPrefix), strings.HasPrefix(path, deviceExtendedPrefix):
		return path[len(extendedPrefix):]
	default:
		return path
	}
}

func isUNC(path string) bool {
	return strings.HasPrefix(path, `\\`)
}

func hasDriveLetter(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	c := path[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func dropDrive(path string) string {
	return path[2:]
}

// splitFirst splits off the first path component, tolerating repeated and
// trailing separators.
func splitFirst(path string) (string, string) {
	path = strings.TrimLeft(path, `\`)
	head, tail, _ := strings.Cut(path, `\`)
	return head, tail
}

// toLinuxPath rewrites a Windows path tail as an absolute Linux path, dropping
// empty and "." components. Other components pass through verbatim: Getwd
// returns a resolved path, so there is nothing further to normalize, and
// rewriting a component could change which file is meant.
func toLinuxPath(tail string) string {
	var parts []string
	for _, part := range strings.Split(tail, `\`) {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	return "/" + strings.Join(parts, "/")
}

// trimEmpty turns the root path into the empty string so it can be appended to
// a /mnt/<letter> prefix without producing a trailing slash.
func trimEmpty(path string) string {
	if path == "/" {
		return ""
	}
	return path
}

func truthy(value string, set bool) bool {
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
