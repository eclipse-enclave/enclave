// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"errors"
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
func resolveTarget(cwd string, env lookupFunc, driveType driveTyper, resolveDrive driveResolver) (target, error) {
	path := stripLongPathPrefix(normalizeSeparators(cwd))

	switch {
	case isUNC(path):
		return uncTarget(path, env)
	case hasDriveLetter(path):
		return driveTarget(path, env, driveType, resolveDrive)
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
	if !isWSLHost(host) {
		return target{}, fmt.Errorf("the current directory %q is on a network share, which has no "+
			"meaningful path inside a WSL distribution. Run enclave from a directory inside the "+
			"distribution instead", path)
	}

	distro, sub := splitFirst(tail)
	if distro == "" || isDotComponent(distro) {
		return target{}, fmt.Errorf("the current directory %q names no distribution. "+
			`Use a path of the form \\wsl.localhost\<Distro>\<path>`, path)
	}

	linuxPath, err := toLinuxPath(sub)
	if err != nil {
		return target{}, refuseDotDot(path, err)
	}

	return target{
		Distro:    distro,
		LinuxPath: linuxPath,
		Warnings:  distroConflictWarnings(distro, env),
	}, nil
}

// refuseDotDot phrases the one error toLinuxPath produces, so every caller
// reports it the same way.
func refuseDotDot(path string, err error) error {
	return fmt.Errorf("the current directory %q %w. Run enclave from the directory itself "+
		"rather than through a path that points above it", path, err)
}

// distroConflictWarnings reports a configured distribution that the working
// directory overrides. The distribution the files live in wins, because another
// one simply cannot see them.
func distroConflictWarnings(distro string, env lookupFunc) []string {
	requested, ok := env(envDistro)
	if !ok || requested == "" || strings.EqualFold(requested, distro) {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s is set to %q but the current directory is inside %q, which cannot be reached from %q. Using %q.",
		envDistro, requested, distro, requested, distro)}
}

// driveTarget handles a Windows drive path. A local drive is refused by default:
// /mnt/c is reachable but crosses the interop layer on every file access, which
// is slow enough to matter for a repository an agent is working in. A mapped
// network drive is refused unless it turns out to be a WSL share.
func driveTarget(path string, env lookupFunc, driveType driveTyper, resolveDrive driveResolver) (target, error) {
	letter := strings.ToLower(path[:1])
	if driveType(strings.ToUpper(path[:1])+`:\`) == driveRemote {
		return remoteDriveTarget(path, env, resolveDrive)
	}

	if !truthy(env(envAllowWindowsPath)) {
		return target{}, fmt.Errorf("the current directory %q is on a Windows drive. Enclave would have "+
			"to mount it through /mnt/%s, where every file access crosses the WSL interop layer and is "+
			"markedly slower. Move the project into the distribution's filesystem (for example under ~/) "+
			"and run enclave from there, or set %s=1 to accept the slowdown", path, letter, envAllowWindowsPath)
	}

	linuxPath, err := toLinuxPath(dropDrive(path))
	if err != nil {
		return target{}, refuseDotDot(path, err)
	}

	t := target{LinuxPath: "/mnt/" + letter + trimEmpty(linuxPath)}
	if requested, ok := env(envDistro); ok && requested != "" {
		t.Distro = requested
	}
	return t, nil
}

// remoteDriveTarget handles a drive letter that maps a network share.
//
// cmd.exe cannot hold a UNC working directory, so `pushd \\wsl.localhost\...`
// assigns a free drive letter instead. That letter is an alias for a path the
// launcher already accepts, so it resolves the mapping and treats it as the UNC
// path it is. Anything else is refused: a real file server has no path inside a
// distribution, and guessing one would bind-mount the wrong directory into a
// container that an agent can write to.
func remoteDriveTarget(path string, env lookupFunc, resolveDrive driveResolver) (target, error) {
	refuse := func(detail string) (target, error) {
		return target{}, fmt.Errorf("the current directory %q is on a mapped network drive that %s, "+
			"so it has no meaningful path inside a WSL distribution. Run enclave from a directory inside "+
			"the distribution instead: PowerShell takes a "+`\\wsl.localhost\<Distro>\<path> `+
			"path as its working directory directly, and in cmd.exe `pushd` on that path works", path, detail)
	}

	unc := normalizeSeparators(resolveDrive(strings.ToUpper(path[:1])))
	if unc == "" {
		return refuse("Windows could not resolve")
	}
	if !isUNC(unc) {
		return refuse("points at " + unc)
	}

	host, tail := splitFirst(strings.TrimPrefix(unc, `\\`))
	if !isWSLHost(host) {
		return refuse("points at " + unc)
	}

	// The distribution must be named explicitly. Without this check a mapping to
	// the provider root would silently promote the first path component to a
	// distribution name.
	distro, mapped := splitFirst(tail)
	if distro == "" || isDotComponent(distro) {
		return refuse("points at " + unc + ", which names no distribution")
	}

	// The letter may be mapped below the distribution root, and the working
	// directory may sit deeper still. Unlike the working directory, the mapping
	// is whatever Windows recorded and has not been canonicalized by Getwd.
	linuxPath, err := toLinuxPath(mapped + `\` + dropDrive(path))
	if err != nil {
		return refuse("points at " + unc + ", which does not unambiguously name a directory")
	}

	return target{
		Distro:    distro,
		LinuxPath: linuxPath,
		Warnings:  distroConflictWarnings(distro, env),
	}, nil
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
	case strings.HasPrefix(path, extendedPrefix):
		return path[len(extendedPrefix):]
	case strings.HasPrefix(path, deviceExtendedPrefix):
		return path[len(deviceExtendedPrefix):]
	default:
		return path
	}
}

func isUNC(path string) bool {
	return strings.HasPrefix(path, `\\`)
}

// isWSLHost reports whether a UNC host is the WSL filesystem provider under
// either of its names.
func isWSLHost(host string) bool {
	return strings.EqualFold(host, uncHostWSLLocal) || strings.EqualFold(host, uncHostWSLLegacy)
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

// errDotDot refuses a path the classifier will not resolve. Getwd returns a
// canonicalized path, so it should not produce one — but a drive mapping read
// back from Windows is not Getwd output, and resolving ".." here could name a
// directory in a different distribution than the one the target was derived
// from. Silently bind-mounting the wrong directory into a container an agent can
// write to is worse than refusing.
var errDotDot = errors.New(`contains a ".." component, which enclave does not resolve`)

// toLinuxPath rewrites a Windows path tail as an absolute Linux path, dropping
// empty and "." components. Other components pass through verbatim: rewriting
// one could change which file is meant.
func toLinuxPath(tail string) (string, error) {
	var parts []string
	for _, part := range strings.Split(tail, `\`) {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", errDotDot
		}
		parts = append(parts, part)
	}
	return "/" + strings.Join(parts, "/"), nil
}

// isDotComponent reports a component that names a directory relative to another
// one. It can never be a distribution name.
func isDotComponent(part string) bool {
	return part == "." || part == ".."
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
