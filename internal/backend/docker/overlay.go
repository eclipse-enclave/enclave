// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"errors"
	"os"
	"strings"

	"enclave/internal/backend/hoststore"
)

func overlayConfigDir(targetDir, sourceDir string, preservePaths []string) error {
	if strings.TrimSpace(targetDir) == "" {
		return errors.New("overlay target dir is empty")
	}
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return err
	}
	return hoststore.Overlay(targetDir, sourceDir, preservePaths)
}
