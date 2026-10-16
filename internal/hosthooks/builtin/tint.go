// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package builtin contains the session_tint compatibility provider.
package builtin

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"enclave/internal/hosthooks"
)

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func Tint() hosthooks.Provider {
	return hosthooks.Provider{ID: "builtin:session-tint", Phases: map[string]string{hosthooks.Enter: ""}, Builtin: func(_ context.Context, req hosthooks.Request) (hosthooks.Response, error) {
		response := hosthooks.Response{SchemaVersion: hosthooks.Version}
		color, _ := req.Settings["color"].(string)
		color = strings.TrimSpace(color)
		if color == "" {
			return response, nil
		}
		if !colorPattern.MatchString(color) {
			return response, fmt.Errorf("color must be #rrggbb")
		}
		if req.Terminal.ColorSuppressed {
			return response, nil
		}
		response.TerminalEffects = []hosthooks.Effect{{Resource: "background", Enter: "\x1b]11;" + color + "\x07", Leave: "\x1b]111\x07"}}
		return response, nil
	}}
}
func Compatibility(configs hosthooks.ConfigMap, color string) hosthooks.ConfigMap {
	defaults := hosthooks.ConfigMap{}
	color = strings.TrimSpace(color)
	if colorPattern.MatchString(color) {
		enabled := true
		defaults["builtin:session-tint"] = hosthooks.Config{Enabled: &enabled, Settings: map[string]any{"color": color}}
	}
	return hosthooks.Merge(defaults, configs)
}
