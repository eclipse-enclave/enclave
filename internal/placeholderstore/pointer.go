// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// visitFields visits present string fields; absent, null and empty fields carry
// no credential. Wildcards expand to concrete pointers before binding them.
func visitFields(doc any, parts []string, prefix string, visit func(string, string, func(string)) error) error {
	part := parts[0]
	switch node := doc.(type) {
	case map[string]any:
		keys := []string{part}
		if part == "*" {
			keys = make([]string, 0, len(node))
			for key := range node {
				keys = append(keys, key)
			}
			slices.Sort(keys)
		}
		for _, key := range keys {
			value, ok := node[key]
			if !ok || value == nil || value == "" {
				continue
			}
			pointer := prefix + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
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
	case []any:
		indices := []int{}
		if part == "*" {
			for i := range node {
				indices = append(indices, i)
			}
		} else {
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(node) || (len(part) > 1 && part[0] == '0') {
				return fmt.Errorf("invalid credential array index")
			}
			indices = append(indices, i)
		}
		for _, i := range indices {
			if node[i] == nil || node[i] == "" {
				continue
			}
			pointer := prefix + "/" + strconv.Itoa(i)
			if len(parts) > 1 {
				if err := visitFields(node[i], parts[1:], pointer, visit); err != nil {
					return err
				}
			} else if text, ok := node[i].(string); ok {
				if err := visit(pointer, text, func(replacement string) { node[i] = replacement }); err != nil {
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
