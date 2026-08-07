// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

// driveKind is the subset of Win32 drive types the launcher distinguishes.
type driveKind int

const (
	// driveOther covers every type the launcher treats alike: fixed,
	// removable, RAM disks, and drives Windows could not classify.
	driveOther driveKind = iota
	// driveRemote is a mapped network drive. Whether it is usable depends on
	// what it maps to, which driveResolver answers.
	driveRemote
)

// hostCalls are the two Win32 questions the launcher has to ask about a drive
// letter. They are function types so the classifier can be tested on any host:
// this is the only part of the launcher that must call Win32 at all.
type (
	// driveTyper classifies a drive root such as `C:\`.
	driveTyper func(root string) driveKind

	// driveResolver reports the UNC path a drive letter is mapped to, given a
	// bare letter such as "Z". It returns the empty string when the letter is
	// not a network mapping or Windows cannot say what it points at.
	driveResolver func(letter string) string
)
