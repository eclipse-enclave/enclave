// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package termtint

import "os"

func interruptSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// reraise cannot re-send a signal to the current process on Windows, so exit
// with the conventional interrupted status once the tint is restored.
func reraise(os.Signal) {
	os.Exit(130)
}
