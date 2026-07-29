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
	// driveRemote is a mapped network drive, which has no meaningful path
	// inside a distribution.
	driveRemote
)

// driveTyper classifies a drive root such as `C:\`. It exists as a function
// type so the classifier can be tested on any host: this is the one part of the
// launcher that must call Win32.
type driveTyper func(root string) driveKind
