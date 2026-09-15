// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package main

import (
	"os"

	"enclave/internal/wslshim"
)

// On Windows, enclave is a launcher rather than an implementation: it forwards
// every argument verbatim to the Linux enclave binary inside a WSL2
// distribution. It deliberately imports neither the embedded runtime assets nor
// the tool extensions, because it never builds an image itself.
func main() {
	os.Exit(wslshim.Run(os.Args[1:]))
}
