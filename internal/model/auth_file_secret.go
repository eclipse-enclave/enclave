// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"fmt"
	"io/fs"
	"strings"
)

// AuthFileSecret describes the credential fields in one native auth file.
type AuthFileSecret struct {
	File   string            `json:"file"`
	Codec  string            `json:"codec"`
	Fields []AuthSecretField `json:"fields"`
}

type AuthSecretField struct {
	Pointer     string                   `json:"pointer"`
	Kind        string                   `json:"kind"`
	Release     string                   `json:"release,omitempty"`
	Placeholder *SecretPlaceholderConfig `json:"placeholder,omitempty"`
}

// TokenEndpoint describes the fixed exchange intercepted by a protected gateway.
type TokenEndpoint struct {
	Host   string            `json:"host"`
	Path   string            `json:"path"`
	Body   string            `json:"body"`
	Grants []string          `json:"grants"`
	Fields map[string]string `json:"fields"`
	Deny   []AuthEndpoint    `json:"deny,omitempty"`
}

type AuthEndpoint struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

// AuthPointerSegments parses an RFC 6901 pointer with at most one wildcard.
func AuthPointerSegments(pointer string) ([]string, error) {
	if !strings.HasPrefix(pointer, "/") || strings.ContainsRune(pointer, '\x00') {
		return nil, fmt.Errorf("auth secret pointer must start with /")
	}
	parts := strings.Split(pointer[1:], "/")
	if len(parts) > 32 {
		return nil, fmt.Errorf("auth secret pointer nesting limit exceeded")
	}
	wildcards := 0
	for i, part := range parts {
		for j := 0; j < len(part); j++ {
			if part[j] == '~' {
				j++
				if j == len(part) || (part[j] != '0' && part[j] != '1') {
					return nil, fmt.Errorf("invalid auth secret pointer escape")
				}
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		if parts[i] == "*" {
			wildcards++
		}
	}
	if wildcards > 1 {
		return nil, fmt.Errorf("auth secret pointer permits only one wildcard")
	}
	return parts, nil
}

// ValidAuthFilePath also excludes delimiters used by the entrypoint's auth lists.
func ValidAuthFilePath(file string) bool {
	if !fs.ValidPath(file) || file == "." || strings.ContainsAny(file, "\\,:|\t\r\n") {
		return false
	}
	for _, r := range file {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
