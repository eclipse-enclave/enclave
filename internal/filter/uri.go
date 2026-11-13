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
)

// pathCharacters are the RFC 3986 characters of a path besides unreserved
// characters and percent-encoded octets: sub-delims, ":", "@", and "/".
const pathCharacters = "!$&'()*+,;=:@/"

// queryParamNamePattern is the charset of query parameter names a governed
// host accepts. Google APIs read "$alt", "$fields", or "$ct" like the
// parameter without "$", so names with other characters are denied.
var queryParamNamePattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// CheckRawPath rejects an escaped request path with a character outside the
// RFC 3986 path grammar: unreserved characters, sub-delims, ":", "@", "/",
// and percent-encoded octets. A raw "#" ends the path for some servers.
func CheckRawPath(raw string) error {
	return checkURICharacters(raw, false)
}

// CheckRawQuery rejects a raw query string with a character outside the RFC
// 3986 query grammar, the path characters and "?". A raw "#" starts a
// fragment for some servers, which then drop every parameter after it.
func CheckRawQuery(raw string) error {
	return checkURICharacters(raw, true)
}

func checkURICharacters(raw string, query bool) error {
	part := "path"
	if query {
		part = "query"
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case isUnreserved(c), strings.IndexByte(pathCharacters, c) >= 0, c == '?' && query:
		case c == '%':
			if i+2 >= len(raw) || !isHex(raw[i+1]) || !isHex(raw[i+2]) {
				return fmt.Errorf("%s contains invalid percent-encoding", part)
			}
			i += 2
		default:
			return fmt.Errorf("%s contains a character RFC 3986 does not allow, such as \"#\"", part)
		}
	}
	return nil
}

// ValidQueryParamName reports whether a percent-decoded query parameter name
// uses only letters, digits, ".", "_", "~", and "-".
func ValidQueryParamName(name string) bool {
	return queryParamNamePattern.MatchString(name)
}

func isUnreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
