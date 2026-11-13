// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import "strings"

// The host hands the active filters to the gateway as one directory with two
// files per filter: the compiled policy and the credential payload.
const (
	PolicyFileSuffix     = ".policy.json"
	CredentialFileSuffix = ".credential.json" // #nosec G101 -- file name suffix, not a credential.
)

// PolicyFileName is the bundle file name of a filter's compiled policy.
func PolicyFileName(name string) string { return name + PolicyFileSuffix }

// CredentialFileName is the bundle file name of a filter's credential payload.
func CredentialFileName(name string) string { return name + CredentialFileSuffix }

// ParseBundleFileName splits a bundle file name into the filter name and
// whether it names the credential payload. ok is false for any other name.
func ParseBundleFileName(file string) (name string, credential bool, ok bool) {
	if name, found := strings.CutSuffix(file, PolicyFileSuffix); found {
		return name, false, ValidateName(name) == nil
	}
	if name, found := strings.CutSuffix(file, CredentialFileSuffix); found {
		return name, true, ValidateName(name) == nil
	}
	return "", false, false
}
