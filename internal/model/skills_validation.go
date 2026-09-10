// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "strings"

// SkillsValidationMode resolves the shared skill validation mode used at runtime.
// Empty or unknown values retain strict validation.
func SkillsValidationMode(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == SkillsValidationAgent {
		return SkillsValidationAgent
	}
	return SkillsValidationStrict
}
