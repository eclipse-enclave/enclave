// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"fmt"
)

// visitFields visits present string fields; absent, null and empty fields carry
// no credential. Only unescaped object fields are supported.
func visitFields(doc any, parts []string, prefix string, visit func(string, string, func(string)) error) error {
	node, ok := doc.(map[string]any)
	if !ok {
		return fmt.Errorf("credential pointer traverses a non-object")
	}
	key := parts[0]
	value, exists := node[key]
	if !exists || value == nil || value == "" {
		return nil
	}
	pointer := prefix + "/" + key
	if len(parts) > 1 {
		return visitFields(value, parts[1:], pointer, visit)
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("credential field %s must be a string", pointer)
	}
	return visit(pointer, text, func(replacement string) { node[key] = replacement })
}
