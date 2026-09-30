// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	hostLocaltimePath = "/etc/localtime"
	hostTimezonePath  = "/etc/timezone"
)

// containerTimeZone returns the configured timezone option, or the host zone
// when the option is unset.
func (r *Runtime) containerTimeZone() string {
	if r.run.Timezone != "" {
		return r.run.Timezone
	}
	return hostTimeZone()
}

// hostTimeZone returns the host time zone as a value for the container's TZ,
// or "" when it cannot be determined (the container then stays on UTC).
func hostTimeZone() string {
	return resolveHostTimeZone(os.LookupEnv, hostLocaltimePath, hostTimezonePath)
}

func resolveHostTimeZone(lookupEnv func(string) (string, bool), localtimePath, timezonePath string) string {
	if tz, ok := lookupEnv("TZ"); ok {
		tz = strings.TrimPrefix(tz, ":")
		if !filepath.IsAbs(tz) {
			// Zone names and POSIX rule strings are portable as-is.
			return tz
		}
		// A file path only exists on the host, so map it back to a zone name.
		// Resolve first: aliases like zoneinfo/localtime are not zone names.
		if resolved, err := filepath.EvalSymlinks(tz); err == nil {
			if name := zoneNameFromPath(resolved); name != "" {
				return name
			}
		}
		if name := zoneNameFromPath(tz); name != "" {
			return name
		}
		localtimePath = tz
	}
	if target, err := os.Readlink(localtimePath); err == nil {
		if name := zoneNameFromPath(target); name != "" {
			return name
		}
	}
	// #nosec G304 -- timezonePath is the fixed host /etc/timezone outside tests.
	if data, err := os.ReadFile(timezonePath); err == nil {
		if name := strings.TrimSpace(string(data)); validZoneName(name) {
			return name
		}
	}
	return ""
}

func zoneNameFromPath(path string) string {
	_, name, ok := strings.Cut(path, "zoneinfo/")
	if !ok {
		return ""
	}
	// Distros may nest the database, e.g. zoneinfo/posix/Europe/Vienna.
	name = strings.TrimPrefix(strings.TrimPrefix(name, "posix/"), "right/")
	if !validZoneName(name) {
		return ""
	}
	return name
}

func validZoneName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '/' || c == '_' || c == '-' || c == '+':
		default:
			return false
		}
	}
	return true
}
