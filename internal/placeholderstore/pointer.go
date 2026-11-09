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
	part := parts[0]
	switch node := doc.(type) {
	case map[string]any:
		keys := []string{part}
		for _, key := range keys {
			value, ok := node[key]
			if !ok || value == nil || value == "" {
				continue
			}
			pointer := prefix + "/" + key
			if len(parts) > 1 {
				if err := visitFields(value, parts[1:], pointer, visit); err != nil {
					return err
				}
			} else if text, ok := value.(string); ok {
				if err := visit(pointer, text, func(replacement string) { node[key] = replacement }); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("credential field %s must be a string", pointer)
			}
		}
	default:
		return fmt.Errorf("credential pointer traverses a non-object")
	}
	return nil
}
