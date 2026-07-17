// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"os"
	"strings"
	"sync"

	"enclave/internal/logx"
)

const selinuxEnforcePath = "/sys/fs/selinux/enforce"

var (
	selinuxOnce      sync.Once
	selinuxEnforcing bool
)

// IsSELinuxEnforcing returns true when SELinux is in enforcing mode.
// The result is cached for the process lifetime since enforcement mode
// does not change while the process is running.
func IsSELinuxEnforcing() bool {
	selinuxOnce.Do(func() {
		selinuxEnforcing = readSELinuxEnforce(selinuxEnforcePath)
		if selinuxEnforcing {
			logx.Infof("SELinux enforcing detected; bind mounts will use :z relabeling")
		}
	})
	return selinuxEnforcing
}

func readSELinuxEnforce(path string) bool {
	// #nosec G304 -- path is a fixed kernel pseudo-file.
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}
