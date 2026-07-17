// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	_ "embed"
	"fmt"
	"path"
	"strings"
)

//go:embed gateway_proxy_build_inputs.txt
var gatewayProxyBuildInputsManifest string

var gatewayProxyBuildInputs = mustParseGatewayProxyBuildInputs(gatewayProxyBuildInputsManifest)

func mustParseGatewayProxyBuildInputs(data string) []string {
	paths, err := parseGatewayProxyBuildInputs(data)
	if err != nil {
		panic(err)
	}
	return paths
}

func parseGatewayProxyBuildInputs(data string) ([]string, error) {
	lines := strings.Split(data, "\n")
	paths := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		clean := path.Clean(line)
		if clean == "." || clean != line || path.IsAbs(line) || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("invalid gateway proxy build input on line %d: %q", idx+1, raw)
		}
		if _, exists := seen[line]; exists {
			return nil, fmt.Errorf("duplicate gateway proxy build input on line %d: %q", idx+1, line)
		}
		seen[line] = struct{}{}
		paths = append(paths, line)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("gateway proxy build inputs manifest is empty")
	}
	return paths, nil
}
