// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tools

import (
	"fmt"

	"enclave/internal/model"
)

// A2ASpec is the "a2a" block of a tool's spec.yaml (proposal 9, 20). A tool
// without it cannot run in serve mode. Defined and validated in model so the
// spec loader can check it without importing this package.
type A2ASpec = model.A2ASpec

// A2ASkillSpec is one agent card skill.
type A2ASkillSpec = model.A2ASkillSpec

// Version and VersionRange are the harness version pin types.
type (
	Version      = model.A2AVersion
	VersionRange = model.A2AVersionRange
)

const (
	SessionIDPlaceholder        = model.A2ASessionIDPlaceholder
	EventFormatClaudeStreamJSON = model.A2AEventFormatClaudeStreamJSON
	EventFormatCodexExecJSON    = model.A2AEventFormatCodexExecJSON
	EventFormatPiJSON           = model.A2AEventFormatPiJSON
	EventFormatCopilotJSON      = model.A2AEventFormatCopilotJSON
)

// ValidateA2ASpec checks an a2a block; errors name the field as "a2a.<field>".
func ValidateA2ASpec(spec *A2ASpec) error { return model.ValidateA2ASpec(spec) }

// ParseVersionRange parses a range such as ">=2.1.306 <3.0.0".
func ParseVersionRange(s string) (VersionRange, error) { return model.ParseA2AVersionRange(s) }

// ParseVersion parses "major.minor.patch" exactly.
func ParseVersion(s string) (Version, error) { return model.ParseA2AVersion(s) }

// ExtractVersion finds the first major.minor.patch in a version command's output.
func ExtractVersion(output string) (Version, error) { return model.ExtractA2AVersion(output) }

// RequireA2A returns the profile's a2a block or the error serve mode reports for tools without one.
func RequireA2A(profile model.Profile) (*A2ASpec, error) {
	if profile.A2A == nil {
		return nil, fmt.Errorf("tool %q cannot run in serve mode: its spec.yaml has no sandbox.a2a block", profile.Name)
	}
	return profile.A2A, nil
}
