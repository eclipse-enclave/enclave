// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

const unknown = "unknown"

// These values are populated with -ldflags by release and package builds.
var (
	version string
	commit  string
	date    string
)

type Info struct {
	Version string
	Commit  string
	Date    string
}

func Read() Info {
	goInfo, _ := debug.ReadBuildInfo()
	return resolve(Info{Version: version, Commit: commit, Date: date}, goInfo)
}

func (i Info) String() string {
	return fmt.Sprintf("%s (%s, %s)", i.Version, i.Commit, i.Date)
}

func resolve(info Info, goInfo *debug.BuildInfo) Info {
	// Explicit stamps describe the source being packaged, even when packaging
	// modifies the checkout. Only fallback VCS metadata reflects checkout state.
	commitFromBuildInfo := isUnknown(info.Commit)
	modified := false
	if goInfo != nil {
		if isUnknown(info.Version) && goInfo.Main.Version != "(devel)" {
			info.Version = goInfo.Main.Version
		}
		for _, setting := range goInfo.Settings {
			switch setting.Key {
			case "vcs.revision":
				if isUnknown(info.Commit) {
					info.Commit = setting.Value
				}
			case "vcs.time":
				if isUnknown(info.Date) {
					info.Date = setting.Value
				}
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}

	info.Version = valueOrUnknown(info.Version)
	info.Commit = normalizeCommit(info.Commit, commitFromBuildInfo && modified)
	info.Date = normalizeDate(info.Date)
	return info
}

func isUnknown(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == unknown
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return unknown
	}
	return value
}

func normalizeCommit(value string, modified bool) string {
	value = strings.TrimSpace(value)
	if value == "" || value == unknown {
		return unknown
	}

	dirty := strings.HasSuffix(value, "-dirty")
	commit := strings.TrimSuffix(value, "-dirty")
	if (len(commit) == 40 || len(commit) == 64) && isHex(commit) {
		commit = commit[:7]
	}
	if (dirty || modified) && commit != unknown {
		commit += "-dirty"
	}
	return commit
}

func isHex(value string) bool {
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return false
		}
	}
	return value != ""
}

func normalizeDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == unknown {
		return unknown
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.Format(time.DateOnly)
	}
	return value
}
