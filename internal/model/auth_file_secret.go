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
	// CheckOnly inspects a config-store path without projecting or reconciling it.
	CheckOnly bool              `json:"checkOnly,omitempty"`
	File      string            `json:"file"`
	Codec     string            `json:"codec"`
	Fields    []AuthSecretField `json:"fields"`
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

// AuthPointerSegments decodes RFC 6901 object fields and at most one wildcard segment.
func AuthPointerSegments(pointer string) ([]string, error) {
	if !strings.HasPrefix(pointer, "/") || strings.ContainsRune(pointer, 0) {
		return nil, fmt.Errorf("auth secret pointer must name object fields")
	}
	parts := strings.Split(pointer[1:], "/")
	if len(parts) > 32 {
		return nil, fmt.Errorf("unsupported auth secret pointer depth")
	}
	wildcards := 0
	for i, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("auth secret pointer field is empty")
		}
		for j := 0; j < len(part); j++ {
			if part[j] == '~' {
				if j+1 == len(part) || (part[j+1] != '0' && part[j+1] != '1') {
					return nil, fmt.Errorf("invalid auth pointer escape")
				}
				j++
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		if part == "*" {
			wildcards++
		}
	}
	if wildcards > 1 {
		return nil, fmt.Errorf("auth secret pointer permits one wildcard")
	}
	return parts, nil
}

func EscapeAuthPointer(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

func AuthPointerMatches(pattern, pointer string) bool {
	a, err := AuthPointerSegments(pattern)
	b, other := AuthPointerSegments(pointer)
	if err != nil || other != nil || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != "*" && a[i] != b[i] {
			return false
		}
	}
	return true
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
