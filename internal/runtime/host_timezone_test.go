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
	"testing"

	"enclave/internal/model"
)

func TestResolveHostTimeZone(t *testing.T) {
	dir := t.TempDir()
	symlink := func(name, target string) string {
		path := filepath.Join(dir, name)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	file := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	linuxLocaltime := symlink("linux", "/usr/share/zoneinfo/Europe/Vienna")
	macLocaltime := symlink("mac", "/var/db/timezone/zoneinfo/America/New_York")
	posixLocaltime := symlink("posix", "../usr/share/zoneinfo/posix/Asia/Tokyo")
	strayLocaltime := symlink("stray", "/opt/zones/Europe/Berlin")
	copiedLocaltime := file("copied", "TZif")
	if err := os.MkdirAll(filepath.Join(dir, "zoneinfo", "America"), 0o755); err != nil {
		t.Fatal(err)
	}
	zoneFile := file("zoneinfo/America/Chicago", "TZif")
	zoneAlias := symlink("zoneinfo/localtime", symlink("etc-localtime", zoneFile))
	timezoneFile := file("timezone", "Europe/Paris\n")
	missing := filepath.Join(dir, "missing")

	tests := []struct {
		name      string
		env       map[string]string
		localtime string
		timezone  string
		want      string
	}{
		{name: "linux symlink", localtime: linuxLocaltime, timezone: missing, want: "Europe/Vienna"},
		{name: "macOS symlink", localtime: macLocaltime, timezone: missing, want: "America/New_York"},
		{name: "posix subtree", localtime: posixLocaltime, timezone: missing, want: "Asia/Tokyo"},
		{name: "etc timezone fallback", localtime: copiedLocaltime, timezone: timezoneFile, want: "Europe/Paris"},
		{name: "symlink outside zoneinfo", localtime: strayLocaltime, timezone: timezoneFile, want: "Europe/Paris"},
		{name: "undetectable", localtime: missing, timezone: missing, want: ""},
		{name: "TZ zone name wins", env: map[string]string{"TZ": "Asia/Kolkata"}, localtime: linuxLocaltime, timezone: missing, want: "Asia/Kolkata"},
		{name: "TZ colon prefix", env: map[string]string{"TZ": ":Europe/Rome"}, localtime: linuxLocaltime, timezone: missing, want: "Europe/Rome"},
		{name: "TZ posix rule", env: map[string]string{"TZ": "CET-1CEST,M3.5.0,M10.5.0/3"}, localtime: linuxLocaltime, timezone: missing, want: "CET-1CEST,M3.5.0,M10.5.0/3"},
		{name: "TZ empty means UTC", env: map[string]string{"TZ": ""}, localtime: linuxLocaltime, timezone: missing, want: ""},
		{name: "TZ symlink path", env: map[string]string{"TZ": ":" + macLocaltime}, localtime: linuxLocaltime, timezone: missing, want: "America/New_York"},
		{name: "TZ zoneinfo alias", env: map[string]string{"TZ": ":" + zoneAlias}, localtime: linuxLocaltime, timezone: timezoneFile, want: "America/Chicago"},
		{name: "TZ zoneinfo file path", env: map[string]string{"TZ": ":" + zoneFile}, localtime: linuxLocaltime, timezone: timezoneFile, want: "America/Chicago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				v, ok := tt.env[key]
				return v, ok
			}
			if got := resolveHostTimeZone(lookup, tt.localtime, tt.timezone); got != tt.want {
				t.Fatalf("resolveHostTimeZone() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContainerEnvPassesHostTimeZone(t *testing.T) {
	t.Setenv("TZ", "Europe/Vienna")
	r := &Runtime{project: model.Project{Dir: t.TempDir()}}

	env := r.containerEnv(&ExecutionContext{}, false)

	if got, _ := lookupEnv(env, "TZ"); got != "Europe/Vienna" {
		t.Fatalf("TZ = %q, want Europe/Vienna", got)
	}
}

func TestContainerEnvKeepsExplicitTimeZone(t *testing.T) {
	t.Setenv("TZ", "Europe/Vienna")
	r := &Runtime{project: model.Project{Dir: t.TempDir()}}

	env := r.containerEnv(&ExecutionContext{Env: []string{"TZ=UTC"}}, true)

	var values []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "TZ=") {
			values = append(values, entry)
		}
	}
	if len(values) != 1 || values[0] != "TZ=UTC" {
		t.Fatalf("TZ entries = %v, want [TZ=UTC]", values)
	}
}
