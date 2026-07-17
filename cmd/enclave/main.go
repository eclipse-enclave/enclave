// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"os"

	"enclave/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:]))
}
