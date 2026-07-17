// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

const actionContinue = "continue"

func isRunAction(action string) bool {
	return action == "run" || action == "shell" || action == actionContinue || action == "resume"
}
