// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"fmt"
	"os"
	"path/filepath"

	"enclave/internal/backend"
	"enclave/internal/filter"
)

// checkFilterEnforcement refuses request filters where no restricted gateway
// enforces them. The host checks this before it builds the request; this is
// the last line, so a filter is never silently dropped.
func checkFilterEnforcement(req backend.Request) error {
	if len(req.Filters) == 0 {
		return nil
	}
	if req.Network.Mode == backend.NetworkModeUnrestricted || !req.Network.Gateway {
		return fmt.Errorf("request filters need restricted networking with the gateway")
	}
	return nil
}

// writeFilterBundles writes the request filters for the gateway into a fresh
// private temp directory: one "<name>.policy.json" and one
// "<name>.credential.json" per filter, the directory 0700 and the files 0600.
// The credential files hold real credential material. The caller removes the
// directory when the gateway stops. It returns "" when there are no filters.
func writeFilterBundles(bundles []backend.FilterBundle) (string, error) {
	if len(bundles) == 0 {
		return "", nil
	}
	seen := map[string]bool{}
	for _, b := range bundles {
		if err := filter.ValidateName(b.Name); err != nil {
			return "", fmt.Errorf("request filter name %q: %w", b.Name, err)
		}
		if seen[b.Name] {
			return "", fmt.Errorf("request filter %s is selected twice", b.Name)
		}
		seen[b.Name] = true
		if len(b.Policy) == 0 || len(b.Credential) == 0 || len(b.Hosts) == 0 {
			return "", fmt.Errorf("request filter %s has no policy, credential, or governed hosts", b.Name)
		}
	}
	dir, err := os.MkdirTemp("", "enclave-filters-*")
	if err != nil {
		return "", fmt.Errorf("create request filter directory: %w", err)
	}
	if err := writeFilterFiles(dir, bundles); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func writeFilterFiles(dir string, bundles []backend.FilterBundle) error {
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- the directory must stay private to the user.
		return fmt.Errorf("chmod request filter directory: %w", err)
	}
	for _, b := range bundles {
		if err := writePrivateFile(filepath.Join(dir, filter.PolicyFileName(b.Name)), b.Policy); err != nil {
			return fmt.Errorf("write request filter %s policy: %w", b.Name, err)
		}
		if err := writePrivateFile(filepath.Join(dir, filter.CredentialFileName(b.Name)), b.Credential); err != nil {
			return fmt.Errorf("write request filter %s credential: %w", b.Name, err)
		}
	}
	return nil
}

// writePrivateFile creates path with mode 0600. It fails if path exists.
func writePrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path is inside a directory created by os.MkdirTemp.
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
