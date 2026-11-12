// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package hoststore

import (
	"errors"
	"io/fs"
	"os"
	"time"
)

// The Windows binary is a WSL launcher; native store mutation is unsupported.
// Keep these methods fail-closed when host packages are cross-compiled.
var errNativeStore = errors.New("native Windows stores are unsupported; run enclave inside WSL")

func (r *Files) ReadDir(string) ([]os.DirEntry, error)       { return nil, errNativeStore }
func (r *Files) ReadFile(string) ([]byte, error)             { return nil, errNativeStore }
func (r *Files) WriteFile(string, []byte, fs.FileMode) error { return errNativeStore }
func (r *Files) Readlink(string) (string, error)             { return "", errNativeStore }
func (r *Files) Symlink(string, string) error                { return errNativeStore }
func (r *Files) Chmod(string, fs.FileMode) error             { return errNativeStore }
func (r *Files) Chtimes(string, time.Time) error             { return errNativeStore }
