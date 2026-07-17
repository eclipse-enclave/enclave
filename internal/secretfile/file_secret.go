// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package secretfile resolves a secret value from a host-side credential file
// and validates the parser spec. It is intentionally a leaf package (depends
// only on internal/util) so both the runtime (internal/auth) and the config
// loader (internal/config) can share the exact same parser validation without
// an import cycle — internal/auth imports internal/config, so the validator
// cannot live in internal/auth.
package secretfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"enclave/internal/util"
)

// jsonParserPrefix marks a credentials file parser as a dot-path JSON
// extractor: "json:auth.token" pulls doc["auth"]["token"].
const jsonParserPrefix = "json:"

// ResolveFileSecret reads a host-side credential file and extracts the secret
// value according to parser. path may start with "~" (expanded to home) or "~/".
//
// The returned bool reports whether a value was obtained: a missing file — or
// an existing file that simply does not contain the dot-path key yet (e.g. a
// partially populated auth.json) — yields ("", false, nil) so callers can fall
// back to another source. A malformed parser or file content, however, fails
// loudly with a non-nil error rather than silently returning nothing.
func ResolveFileSecret(home, path, parser string) (string, bool, error) {
	// Validate the parser up front so a malformed parser is loud even when the
	// file is absent (it is a spec error, not a missing-value condition).
	if err := ValidateFileParser(parser); err != nil {
		return "", false, err
	}
	resolved := util.ExpandTilde(path, home)
	// #nosec G304 -- path comes from a trusted extension spec, resolved against home.
	data, err := os.ReadFile(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read credentials file %q: %w", resolved, err)
	}
	return parseFileSecret(data, parser)
}

// ValidateFileParser rejects an unsupported parser without needing the file
// contents. Supported forms: "" (raw) and "json:<dot.path>". Exported so the
// config loader can reject a bad parser at spec-load time (loud, actionable)
// rather than leaving it to fail later at runtime resolution.
func ValidateFileParser(parser string) error {
	parser = strings.TrimSpace(parser)
	if parser == "" {
		return nil
	}
	if strings.HasPrefix(parser, "/") {
		return fmt.Errorf("credentials file parser %q looks like an RFC 6901 JSON pointer; use dot-path form %q", parser, "json:a.b.c")
	}
	if !strings.HasPrefix(parser, jsonParserPrefix) {
		return fmt.Errorf("unsupported credentials file parser %q (expected \"\" or %q)", parser, "json:<dot.path>")
	}
	dotPath := strings.TrimSpace(strings.TrimPrefix(parser, jsonParserPrefix))
	if dotPath == "" {
		return fmt.Errorf("credentials file parser %q requires a dot-path (e.g. %q)", parser, "json:auth.token")
	}
	if strings.HasPrefix(dotPath, "/") {
		return fmt.Errorf("credentials file parser dot-path %q must not begin with '/'; RFC 6901 pointers are not supported here, use dot-path form (e.g. %q)", dotPath, "json:a.b.c")
	}
	return nil
}

// parseFileSecret extracts the secret value from raw file bytes per parser.
// An empty parser returns the trimmed raw contents; "json:<dot.path>" extracts
// the scalar at that dot-separated path from a JSON document. The bool reports
// whether the value was present (see ResolveFileSecret).
func parseFileSecret(data []byte, parser string) (string, bool, error) {
	if err := ValidateFileParser(parser); err != nil {
		return "", false, err
	}
	parser = strings.TrimSpace(parser)
	if parser == "" {
		return strings.TrimSpace(string(data)), true, nil
	}
	dotPath := strings.TrimSpace(strings.TrimPrefix(parser, jsonParserPrefix))
	return resolveJSONDotPath(data, dotPath)
}

// resolveJSONDotPath walks a dot-separated path (e.g. "auth.token") through a
// JSON document and returns the scalar found there. It is intentionally
// distinct from the RFC 6901 JSON Pointer resolver used by authSession checks:
// segments are split on '.', not '/', and there is no ~0/~1 escaping.
//
// A key that is absent anywhere along the path reports ("", false, nil): a
// partially populated credentials file is a normal state, not an error, and
// must not block session start when another source can supply the value.
// Malformed JSON and a path that resolves to a non-scalar stay loud errors.
func resolveJSONDotPath(data []byte, dotPath string) (string, bool, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", false, fmt.Errorf("credentials file is not valid JSON: %w", err)
	}
	current := doc
	parts := strings.Split(dotPath, ".")
	for i, part := range parts {
		obj, ok := current.(map[string]any)
		if !ok {
			return "", false, fmt.Errorf("credentials file JSON path %q: %q is not an object", dotPath, strings.Join(parts[:i], "."))
		}
		next, ok := obj[part]
		if !ok {
			return "", false, nil
		}
		current = next
	}
	value, err := jsonScalarString(current, dotPath)
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// jsonScalarString renders a JSON scalar as a string. Objects, arrays, and null
// are rejected: a credential must be a concrete value.
func jsonScalarString(v any, dotPath string) (string, error) {
	switch value := v.(type) {
	case string:
		return value, nil
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(value), nil
	default:
		return "", fmt.Errorf("credentials file JSON path %q does not resolve to a scalar value", dotPath)
	}
}
