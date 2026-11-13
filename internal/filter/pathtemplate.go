// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var defaultCapturePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const maxTemplateLength = 2048

// literalForbidden lists characters a literal template part cannot contain:
// they would be template syntax, encoded forms, or never reach a segment.
const literalForbidden = "{}%*?#\\ "

// PathTemplate is a parsed route path template.
type PathTemplate struct {
	raw      string
	segments []templateSegment
	// trailingSlash is set when the template ends with "/".
	trailingSlash bool
	// rest is set when the template ends with "**".
	rest     bool
	captures []string
}

type templateSegment struct {
	literal string
	capture string
	// pattern is nil for the default capture class.
	pattern *regexp.Regexp
	suffix  string
}

// ParsePathTemplate parses a route path template such as
// "/v1/documents/{documentId}:batchUpdate".
func ParsePathTemplate(template string) (*PathTemplate, error) {
	if template == "" {
		return nil, fmt.Errorf("path must not be empty")
	}
	if len(template) > maxTemplateLength {
		return nil, fmt.Errorf("path is longer than %d characters", maxTemplateLength)
	}
	if !strings.HasPrefix(template, "/") {
		return nil, fmt.Errorf("path %q must start with \"/\"", template)
	}
	if hasControl(template) {
		return nil, fmt.Errorf("path %q must not contain control characters", template)
	}
	parts, err := splitTemplate(template[1:])
	if err != nil {
		return nil, fmt.Errorf("path %q: %w", template, err)
	}
	t := &PathTemplate{raw: template}
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		t.trailingSlash = true
		parts = parts[:len(parts)-1]
	}
	seen := map[string]bool{}
	for i, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("path %q contains an empty segment", template)
		}
		if part == "**" {
			if i != len(parts)-1 || t.trailingSlash {
				return nil, fmt.Errorf("path %q: \"**\" is only allowed as the final segment", template)
			}
			t.rest = true
			continue
		}
		seg, err := parseTemplateSegment(part)
		if err != nil {
			return nil, fmt.Errorf("path %q: %w", template, err)
		}
		if seg.capture != "" {
			if seen[seg.capture] {
				return nil, fmt.Errorf("path %q captures %q twice", template, seg.capture)
			}
			seen[seg.capture] = true
			t.captures = append(t.captures, seg.capture)
		}
		t.segments = append(t.segments, seg)
	}
	return t, nil
}

// splitTemplate splits on "/" outside of capture braces.
func splitTemplate(s string) ([]string, error) {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced \"}\"")
			}
		case '/':
			if depth > 0 {
				return nil, fmt.Errorf("capture patterns must not contain \"/\"")
			}
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced \"{\"")
	}
	return append(parts, s[start:]), nil
}

func parseTemplateSegment(part string) (templateSegment, error) {
	if !strings.HasPrefix(part, "{") {
		if strings.ContainsAny(part, literalForbidden) {
			return templateSegment{}, fmt.Errorf("literal segment %q must not contain any of %q", part, literalForbidden)
		}
		if part == "." || part == ".." {
			return templateSegment{}, fmt.Errorf("dot segments are not allowed")
		}
		return templateSegment{literal: part}, nil
	}
	end := matchingBrace(part)
	if end < 0 {
		return templateSegment{}, fmt.Errorf("segment %q has an unbalanced \"{\"", part)
	}
	inner, suffix := part[1:end], part[end+1:]
	name, expr, hasExpr := strings.Cut(inner, ":")
	if err := ValidateVarName(name); err != nil {
		return templateSegment{}, fmt.Errorf("segment %q: %w", part, err)
	}
	if strings.ContainsAny(suffix, literalForbidden) {
		return templateSegment{}, fmt.Errorf("segment %q: the literal after a capture must not contain any of %q", part, literalForbidden)
	}
	seg := templateSegment{capture: name, suffix: suffix}
	if hasExpr {
		if expr == "" {
			return templateSegment{}, fmt.Errorf("segment %q has an empty pattern", part)
		}
		re, err := regexp.Compile(`^(?:` + expr + `)$`)
		if err != nil {
			return templateSegment{}, fmt.Errorf("segment %q: invalid pattern: %w", part, err)
		}
		seg.pattern = re
		return seg, nil
	}
	if suffix != "" && defaultCapturePattern.MatchString(suffix[:1]) {
		return templateSegment{}, fmt.Errorf("segment %q: the literal after {%s} must not start with a letter, digit, \"_\", or \"-\"", part, name)
	}
	return seg, nil
}

func matchingBrace(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// String returns the template source.
func (t *PathTemplate) String() string { return t.raw }

// Captures lists the capture names in template order.
func (t *PathTemplate) Captures() []string {
	return append([]string(nil), t.captures...)
}

// Match matches a parsed request path and returns the captured values.
func (t *PathTemplate) Match(p RequestPath) (map[string]string, bool) {
	if t.rest {
		if p.TrailingSlash || len(p.Segments) <= len(t.segments) {
			return nil, false
		}
	} else if len(p.Segments) != len(t.segments) || p.TrailingSlash != t.trailingSlash {
		return nil, false
	}
	captures := map[string]string{}
	for i, seg := range t.segments {
		value := p.Segments[i]
		if seg.capture == "" {
			if value != seg.literal {
				return nil, false
			}
			continue
		}
		captured, ok := strings.CutSuffix(value, seg.suffix)
		if !ok || captured == "" {
			return nil, false
		}
		pattern := seg.pattern
		if pattern == nil {
			pattern = defaultCapturePattern
		}
		if !pattern.MatchString(captured) {
			return nil, false
		}
		captures[seg.capture] = captured
	}
	return captures, true
}

// RequestPath is a request path split into percent-decoded segments.
type RequestPath struct {
	Segments      []string
	TrailingSlash bool
}

// ParseRequestPath splits an escaped request path and rejects every form that
// a server could read differently from the filter: encoded "/", "\", or NUL,
// a literal "\", dot segments (also percent-encoded), empty segments, control
// characters, invalid percent-encoding, and characters outside the RFC 3986
// path grammar, such as "#".
func ParseRequestPath(escaped string) (RequestPath, error) {
	if !strings.HasPrefix(escaped, "/") {
		return RequestPath{}, fmt.Errorf("path must start with \"/\"")
	}
	if strings.Contains(escaped, "\\") {
		return RequestPath{}, fmt.Errorf("path contains a backslash")
	}
	if err := CheckRawPath(escaped); err != nil {
		return RequestPath{}, err
	}
	lower := strings.ToLower(escaped)
	for _, bad := range []string{"%2f", "%5c", "%00"} {
		if strings.Contains(lower, bad) {
			return RequestPath{}, fmt.Errorf("path contains an encoded separator or NUL")
		}
	}
	if escaped == "/" {
		return RequestPath{TrailingSlash: true}, nil
	}
	parts := strings.Split(escaped[1:], "/")
	result := RequestPath{}
	if parts[len(parts)-1] == "" {
		result.TrailingSlash = true
		parts = parts[:len(parts)-1]
	}
	for _, part := range parts {
		if part == "" {
			return RequestPath{}, fmt.Errorf("path contains an empty segment")
		}
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return RequestPath{}, fmt.Errorf("path contains invalid percent-encoding")
		}
		if decoded == "." || decoded == ".." {
			return RequestPath{}, fmt.Errorf("path contains a dot segment")
		}
		if hasControl(decoded) || strings.ContainsAny(decoded, "/\\") {
			return RequestPath{}, fmt.Errorf("path contains a control character or separator")
		}
		result.Segments = append(result.Segments, decoded)
	}
	return result, nil
}
