// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const maxNameLength = 128

var (
	namePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	identPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	varPattern        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	envVarPattern     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	lowerCamelSegment = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

// NameCharset describes the name rule in user-facing words.
const NameCharset = "must start with a lowercase letter or digit and use only lowercase letters, digits, \".\", \"-\", and \"_\""

// ValidateName checks a filter, preset, capability, or counter name.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if len(name) > maxNameLength {
		return fmt.Errorf("name %q is longer than %d characters", name, maxNameLength)
	}
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid name %q: %s", name, NameCharset)
	}
	return nil
}

// ValidateIdent checks an operation, resolver, or constraint name. Unlike
// ValidateName it allows uppercase letters, as in "docs.documents.batchUpdate".
func ValidateIdent(name string) error {
	if len(name) > maxNameLength || !identPattern.MatchString(name) {
		return fmt.Errorf("invalid name %q: must start with a letter or digit and use only letters, digits, \".\", \"-\", and \"_\"", name)
	}
	return nil
}

// ValidateVarName checks a bound variable or path capture name.
func ValidateVarName(name string) error {
	if !varPattern.MatchString(name) || len(name) > maxNameLength {
		return fmt.Errorf("invalid variable name %q: use letters, digits, and \"_\", not starting with a digit", name)
	}
	return nil
}

// ValidateHeaderName checks an HTTP header field name.
func ValidateHeaderName(name string) error {
	if !headerNamePattern.MatchString(name) {
		return fmt.Errorf("invalid header name %q", name)
	}
	return nil
}

// ValidateEnvVarName checks a container or host environment variable name.
func ValidateEnvVarName(name string) error {
	if !envVarPattern.MatchString(name) {
		return fmt.Errorf("invalid env var name %q", name)
	}
	return nil
}

// QueryParamNames returns the request parameter names that carry the route
// parameter name: the name itself and, when keys is JSONKeysProtobuf, its
// protobuf snake_case alias. Query conditions and binds read the values of
// all of them.
func QueryParamNames(name, keys string) []string {
	if keys == JSONKeysProtobuf {
		if alias, ok := ProtobufParamAlias(name); ok {
			return []string{name, alias}
		}
	}
	return []string{name}
}

// ProtobufParamAlias returns the original protobuf field name of a
// lowerCamelCase parameter name, the inverse of ProtobufJSONName: "_" and
// the lowercase letter replace each uppercase ASCII letter. Each "."
// separated segment is converted on its own, so "requestMask.includeField"
// becomes "request_mask.include_field". Segments that are not lowerCamelCase
// stay as they are. ok is false when the alias equals name.
func ProtobufParamAlias(name string) (alias string, ok bool) {
	segments := strings.Split(name, ".")
	for i, seg := range segments {
		if lowerCamelSegment.MatchString(seg) {
			segments[i] = snakeCase(seg)
		}
	}
	alias = strings.Join(segments, ".")
	return alias, alias != name
}

func snakeCase(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			b.WriteByte('_')
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// FoldParam folds a query parameter name for ambiguity checks: Unicode case
// folding, without "_", whitespace, and control characters. A server may
// read a request parameter that folds like a parameter the filter checks as
// that parameter, or as an unknown one and fall back to a default, so the
// engine denies such spellings instead of guessing.
func FoldParam(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r != '_' && !unicode.IsSpace(r) && !unicode.IsControl(r) {
			b.WriteRune(foldRune(r))
		}
	}
	return b.String()
}

// foldRune maps r to the smallest rune of its case folding orbit, so two
// runes fold alike exactly when strings.EqualFold treats them as equal.
func foldRune(r rune) rune {
	smallest := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < smallest {
			smallest = f
		}
	}
	return smallest
}

// asciiLower lowercases ASCII letters only.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
