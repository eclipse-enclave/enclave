// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Directory modes. The status and audit directories are sticky and world
// writable so the root supervisor and the proxy uid can each replace only
// their own files by rename.
const (
	privateDirMode  os.FileMode = 0o700
	publicDirMode   os.FileMode = 0o755
	sharedDirMode               = os.ModeSticky | 0o777
	privateFileMode os.FileMode = 0o600
	publicFileMode  os.FileMode = 0o644
	secretsFileMode os.FileMode = 0o400
)

// writeFileAtomic replaces path via a temp file in the same directory, then
// fsyncs the directory so the rename survives a crash.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil { // #nosec G703 -- path is built from package path helpers.
		return err
	}
	done = true
	return syncDir(dir)
}

func writeJSONAtomic(path string, v any, mode os.FileMode) error {
	data, err := marshalJSON(v)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, mode)
}

func marshalJSON(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// writeFileSynced creates a new file (never replacing one) and fsyncs it;
// used inside a staging directory that is renamed into place as a whole.
func writeFileSynced(path string, data []byte, mode os.FileMode) error {
	// #nosec G304 -- path is built from package path helpers.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func syncDir(path string) error {
	d, err := os.Open(path) // #nosec G304 -- path is built from package path helpers.
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	// Some filesystems (and Windows) refuse fsync on directories.
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}

// ensureDir creates path and forces mode, since MkdirAll is subject to umask
// and never changes an existing directory.
func ensureDir(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode.Perm()); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode()&(os.ModePerm|os.ModeSticky) == mode {
		return nil
	}
	return os.Chmod(path, mode)
}

type schemaHeader struct {
	SchemaVersion string `json:"schemaVersion"`
}

// decodeJSON rejects unknown schema versions before decoding into v.
func decodeJSON(data []byte, v any) error {
	var h schemaHeader
	if err := json.Unmarshal(data, &h); err != nil {
		return err
	}
	if h.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %q", h.SchemaVersion)
	}
	return json.NewDecoder(bytes.NewReader(data)).Decode(v)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path) // #nosec G304 -- path is built from package path helpers.
	if err != nil {
		return err
	}
	if err := decodeJSON(data, v); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// checkSchema fills an empty schema version and refuses any other one.
func checkSchema(v *string) error {
	switch *v {
	case "":
		*v = SchemaVersion
	case SchemaVersion:
	default:
		return fmt.Errorf("unsupported schemaVersion %q", *v)
	}
	return nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return HashPrefix + hex.EncodeToString(sum[:])
}
