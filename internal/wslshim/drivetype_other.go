// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package wslshim

// The launcher only runs on Windows. These stubs exist so the rest of the
// package compiles and is testable on Linux, where drive letters cannot occur
// anyway; the tests inject their own answers.
func systemDriveType(string) driveKind {
	return driveOther
}

func systemDriveResolver(string) string {
	return ""
}
