// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"enclave/internal/domainpattern"
	"enclave/internal/model"
)

func validateAuthSecrets(doc specDocument, specPath string) error {
	for _, provider := range doc.Providers {
		files := map[string]bool{}
		for _, file := range provider.AuthSecrets {
			if !model.ValidAuthFilePath(file.File) || (!file.CheckOnly && !slices.Contains(provider.AuthFiles, file.File)) || files[file.File] {
				return fmt.Errorf("%s: provider %s authSecrets file %q must name a unique authFiles entry unless checkOnly", specPath, provider.Name, file.File)
			}
			files[file.File] = true
			if !slices.Contains([]string{"claude-credentials", "claude-config"}, file.Codec) || len(file.Fields) == 0 {
				return fmt.Errorf("%s: authSecrets %s requires a supported codec and fields", specPath, file.File)
			}
			pointers := map[string]bool{}
			for _, field := range file.Fields {
				if _, err := model.AuthPointerSegments(field.Pointer); err != nil || pointers[field.Pointer] {
					return fmt.Errorf("%s: authSecrets %s has an invalid or duplicate pointer %q", specPath, file.File, field.Pointer)
				}
				pointers[field.Pointer] = true
				if err := validateAuthSecretField(doc, field); err != nil {
					return fmt.Errorf("%s: authSecrets %s %s: %w", specPath, file.File, field.Pointer, err)
				}
			}
		}
		if provider.TokenEndpoint != nil {
			if len(provider.AuthSecrets) == 0 {
				return fmt.Errorf("%s: provider %s tokenEndpoint requires authSecrets", specPath, provider.Name)
			}
			if err := validateTokenEndpoint(*provider.TokenEndpoint); err != nil {
				return fmt.Errorf("%s: provider %s tokenEndpoint: %w", specPath, provider.Name, err)
			}
		}
	}
	return nil
}

func validateAuthSecretField(doc specDocument, field model.AuthSecretField) error {
	switch field.Kind {
	case "access":
		if field.Release == "" {
			return fmt.Errorf("access requires release")
		}
	case "refresh", "id":
		if field.Release != "" {
			return fmt.Errorf("%s is not an API release credential", field.Kind)
		}
	case "apiKey":
		if field.Release == "" || field.Placeholder != nil {
			return fmt.Errorf("apiKey requires release and uses its existing env placeholder")
		}
	default:
		return fmt.Errorf("kind must be access, refresh, id, or apiKey")
	}
	if field.Release != "" {
		if doc.Network == nil {
			return fmt.Errorf("release %q requires network.serviceAuth", field.Release)
		}
		if _, ok := doc.Network.ServiceAuth[field.Release]; !ok {
			return fmt.Errorf("release %q has no network.serviceAuth entry", field.Release)
		}
	}
	if shape := field.Placeholder; shape != nil {
		if _, err := shape.ValidatedAlphabet(); err != nil {
			return err
		}
		if shape.Random.Encoding == "base64url" && shape.Random.Length < len(model.SecretPlaceholderMarker)+22 {
			return fmt.Errorf("placeholder body must retain at least 128 random bits after its marker")
		}
	}
	return nil
}

func validateTokenEndpoint(endpoint model.TokenEndpoint) error {
	for _, target := range append([]model.AuthEndpoint{{Host: endpoint.Host, Path: endpoint.Path}}, endpoint.Deny...) {
		host, err := domainpattern.NormalizeHost(target.Host)
		if err != nil || host == "" || host != target.Host || strings.Contains(host, "*") || !strings.HasPrefix(target.Path, "/") || path.Clean(target.Path) != target.Path || strings.ContainsAny(target.Path, "?#\x00") {
			return fmt.Errorf("requires exact normalized hosts and absolute paths")
		}
	}
	if endpoint.Body != "json" || len(endpoint.Grants) == 0 || endpoint.Fields["access"] == "" {
		return fmt.Errorf("requires body json, grants, and fields.access")
	}
	for name, value := range endpoint.Fields {
		if !slices.Contains([]string{"access", "refresh", "expiresIn", "scope"}, name) || value == "" {
			return fmt.Errorf("invalid token field %q", name)
		}
	}
	for _, grant := range endpoint.Grants {
		if grant == "" || strings.ContainsAny(grant, " \t\r\n\x00") {
			return fmt.Errorf("grant names must not be empty or contain whitespace")
		}
	}
	return nil
}
