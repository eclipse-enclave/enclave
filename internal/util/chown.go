// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"fmt"
	"strconv"
	"strings"
)

// ChownSpec returns a validated "uid:gid" ownership spec, or "" when either
// value is not a non-negative integer.
func ChownSpec(uid string, gid string) string {
	uidValue, err := strconv.Atoi(strings.TrimSpace(uid))
	if err != nil || uidValue < 0 {
		return ""
	}
	gidValue, err := strconv.Atoi(strings.TrimSpace(gid))
	if err != nil || gidValue < 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", uidValue, gidValue)
}
