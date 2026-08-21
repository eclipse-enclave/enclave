// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"fmt"
	"io"
	"os"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

// parseEnvLines parses KEY=VALUE pairs from a reader.
func parseEnvLines(r io.Reader) (map[string]string, error) {
	return util.ParseEnv(r)
}

// ReadAllEnvFromFile reads all KEY=VALUE pairs from a .env file.
// Returns nil map and no error if the file does not exist.
func ReadAllEnvFromFile(path string) (map[string]string, error) {
	// #nosec G304 -- path is resolved from trusted secrets file locations.
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return parseEnvLines(file)
}

// SecretsLayer is one secrets file together with the values it contributes.
type SecretsLayer struct {
	Path   string
	Values map[string]string
}

// ResolveSecretsLayers reads the secrets files in layer order, lowest
// precedence first. Layer 1: global.env (always read). Layer 2:
// global/<tool>.env (when scope is "global" or "both"). Layer 3:
// projects/<hash>/<tool>.env (when scope is "project" or "both"). Later layers
// override earlier ones. Files that are missing or empty are omitted.
func ResolveSecretsLayers(home string, projectHash string, tool string, scope string) ([]SecretsLayer, error) {
	type candidate struct {
		path   string
		errMsg string
	}

	candidates := []candidate{{
		path:   config.HostSecretsGlobalSharedFile(home),
		errMsg: "read global secrets",
	}}
	if scope == model.SecretsScopeGlobal || scope == model.SecretsScopeBoth {
		candidates = append(candidates, candidate{
			path:   config.HostSecretsGlobalFile(home, tool),
			errMsg: "read global per-tool secrets",
		})
	}
	if scope == model.SecretsScopeProject || scope == model.SecretsScopeBoth {
		candidates = append(candidates, candidate{
			path:   config.HostSecretsProjectFile(home, projectHash, tool),
			errMsg: "read project per-tool secrets",
		})
	}

	layers := make([]SecretsLayer, 0, len(candidates))
	for _, candidate := range candidates {
		values, err := ReadAllEnvFromFile(candidate.path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", candidate.errMsg, err)
		}
		if len(values) == 0 {
			continue
		}
		layers = append(layers, SecretsLayer{Path: candidate.path, Values: values})
	}
	return layers, nil
}
