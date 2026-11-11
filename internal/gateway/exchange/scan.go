// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"enclave/internal/placeholderstore"
	"encoding/json"
	"net/http"
)

func contained(data []byte, headers http.Header, credentials []string) bool {
	var doc any
	if json.Unmarshal(data, &doc) != nil {
		return false
	}
	for _, value := range credentials {
		if placeholderstore.ContainsCredential(doc, value) {
			return false
		}
		for key, values := range headers {
			if placeholderstore.ContainsCredential(key, value) {
				return false
			}
			for _, header := range values {
				if placeholderstore.ContainsCredential(header, value) {
					return false
				}
			}
		}
	}
	return true
}
