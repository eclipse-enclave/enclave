//go:build windows

// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"fmt"
	"os"
)

func preserveOwner(_ *os.File, _ os.FileInfo) error {
	return fmt.Errorf("native credential writes require a Unix host")
}

func renameNative(_ *os.File, _, _ string) error {
	return fmt.Errorf("native credential writes require a Unix host")
}
