// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"encoding/json"
	"net/http"
	"strings"
)

func contains(value any, credential string) bool {
	if credential == "" {
		return false
	}
	switch v := value.(type) {
	case string:
		return strings.Contains(v, credential)
	case map[string]any:
		for key, child := range v {
			if strings.Contains(key, credential) || contains(child, credential) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if contains(child, credential) {
				return true
			}
		}
	}
	return false
}

func contained(data []byte, headers http.Header, credentials []string) bool {
	var doc any
	if json.Unmarshal(data, &doc) != nil {
		return false
	}
	for _, value := range credentials {
		if contains(doc, value) {
			return false
		}
		for key, values := range headers {
			if contains(key, value) {
				return false
			}
			for _, header := range values {
				if contains(header, value) {
					return false
				}
			}
		}
	}
	return true
}
