// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

// OriginSchemaVersion is the only sidecar schema this code writes or accepts.
const OriginSchemaVersion = "1"

// Ref types recorded in a sidecar. Only RefTypeCommit is immutable, which is
// what lets update skip the network entirely for a pinned install.
const (
	RefTypeBranch = "branch"
	RefTypeTag    = "tag"
	RefTypeCommit = "commit"
)

// Origin is the recorded provenance of a managed extension.
type Origin struct {
	SchemaVersion string `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Remote        string `json:"remote"`
	Source        string `json:"source"`
	Subpath       string `json:"subpath,omitempty"`
	Ref           string `json:"ref"`
	RefType       string `json:"refType"`
	Commit        string `json:"commit"`
	InstalledAt   string `json:"installedAt"`
	InstalledBy   string `json:"installedBy"`
	TreeHash      string `json:"treeHash"`
}

// readOrigin loads the sidecar from extDir. A missing sidecar is not an error:
// it means the directory is unmanaged (hand-written, or installed before the
// installer existed), and the caller decides what that implies.
//
// A sidecar can reach disk without ever passing through Add/Update — a user
// can hand-author one, or clone a kit directly into their extensions
// directory — so its remote/ref/name are validated here on read rather than
// trusted as a writer-enforced invariant: updateOne feeds origin.Remote and
// origin.Ref straight to the fetcher, which hands them to git as bare
// operands.
func readOrigin(extDir string) (*Origin, error) {
	path := filepath.Join(extDir, model.ExtensionSourceFilename)
	// #nosec G304 -- path is derived from a caller-supplied extension dir.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var origin Origin
	if err := json.Unmarshal(data, &origin); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if origin.SchemaVersion != OriginSchemaVersion {
		return nil, fmt.Errorf("%s schemaVersion must be %q (got %q)", path, OriginSchemaVersion, origin.SchemaVersion)
	}
	if err := validateOriginRemote(origin.Remote); err != nil {
		return nil, fmt.Errorf("%s remote: %w", path, err)
	}
	if err := validateRefName(origin.Ref); err != nil {
		return nil, fmt.Errorf("%s ref %q: %w", path, origin.Ref, err)
	}
	if base := filepath.Base(extDir); origin.Name != "" && origin.Name != base {
		return nil, fmt.Errorf("%s name %q does not match its directory %q", path, origin.Name, base)
	}
	// The installer never records credentials, but a hand-authored or copied
	// sidecar can carry them, and these two fields are printed, serialized into
	// `--json`, and handed to git. Redacting here rather than at each of those
	// sinks means a new consumer cannot leak them by forgetting to.
	origin.Remote = RedactRemote(origin.Remote)
	origin.Source = RedactRemote(origin.Source)
	return &origin, nil
}

// validateOriginRemote rejects a sidecar-recorded remote that is unsafe to
// hand to the fetcher as a bare git operand, or that uses a transport this
// feature never fetches with in the first place: an unrecognized or
// `ext::`-style transport reaches git's remote-helper dispatch, which can run
// arbitrary commands.
func validateOriginRemote(remote string) error {
	trimmed := strings.TrimSpace(remote)
	if trimmed == "" {
		return fmt.Errorf("remote is empty")
	}
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("remote %q looks like a command-line option", trimmed)
	}
	switch {
	case hasScheme(trimmed), isSCPLike(trimmed), filepath.IsAbs(trimmed):
		return nil
	default:
		return fmt.Errorf("remote %q uses an unsupported transport", trimmed)
	}
}

// WriteOrigin writes the sidecar into extDir, replacing any existing one.
func WriteOrigin(extDir string, origin Origin) error {
	if origin.SchemaVersion == "" {
		origin.SchemaVersion = OriginSchemaVersion
	}
	data, err := json.MarshalIndent(origin, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return util.WriteFileAtomic(filepath.Join(extDir, model.ExtensionSourceFilename), data, 0o600)
}

// isLocallyModified reports whether extDir's content diverges from the tree
// hash recorded at install time. An origin without a recorded hash is treated
// as unmodified, since there is nothing to compare against.
func isLocallyModified(extDir string, origin Origin) (bool, error) {
	recorded := recordedTreeHash(origin)
	if recorded == "" {
		return false, nil
	}
	manifest, err := readTreeManifest(extDir)
	if err != nil {
		return false, err
	}
	return manifest.Hash != recorded, nil
}

// recordedTreeHash is the hash an extension's current content is compared
// against, or "" when the sidecar records none.
func recordedTreeHash(origin Origin) string {
	return strings.TrimSpace(origin.TreeHash)
}

// ShortCommit abbreviates a commit for display.
func ShortCommit(commit string) string {
	if len(commit) <= 7 {
		return commit
	}
	return commit[:7]
}
