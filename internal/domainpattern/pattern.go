// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package domainpattern

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

var hostLabelPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// Normalize validates and normalizes a domain pattern used in allowlist-style
// config. Empty input returns an empty pattern without error.
func Normalize(value string) (string, error) {
	pattern := strings.ToLower(strings.TrimSpace(value))
	if pattern == "" {
		return "", nil
	}
	pattern = strings.TrimSuffix(pattern, ".")

	if pattern == "*" {
		return "", fmt.Errorf("bare wildcard \"*\" is not allowed")
	}
	if strings.Contains(pattern, "://") {
		return "", fmt.Errorf("domain pattern must not include protocol")
	}
	if strings.ContainsAny(pattern, "/?#@[] \t\r\n") {
		return "", fmt.Errorf("domain pattern must be host-only")
	}
	if strings.Contains(pattern, ":") {
		return "", fmt.Errorf("domain pattern must not include a port")
	}
	if strings.HasPrefix(pattern, ".") {
		return "", fmt.Errorf("domain pattern must not start with \".\"")
	}

	if strings.Contains(pattern, "*") {
		if !strings.HasPrefix(pattern, "*.") {
			return "", fmt.Errorf("wildcards must use \"*.\" prefix")
		}
		if strings.Count(pattern, "*") > 1 {
			return "", fmt.Errorf("domain pattern contains multiple wildcards")
		}
		suffix := strings.TrimPrefix(pattern, "*.")
		if suffix == "" {
			return "", fmt.Errorf("wildcard domain suffix is required")
		}
		if strings.Count(suffix, ".") < 1 {
			return "", fmt.Errorf("wildcard suffix must include at least two labels")
		}
		if err := validateHost(suffix); err != nil {
			return "", err
		}
		return "*." + suffix, nil
	}

	if err := validateHost(pattern); err != nil {
		return "", err
	}
	return pattern, nil
}

// NormalizeHost normalizes a concrete host value used at runtime. Unlike
// Normalize, it accepts host:port forms and bracketed IPv6 literals.
func NormalizeHost(value string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(value))
	if host == "" {
		return "", fmt.Errorf("host is required")
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	host = strings.TrimLeft(host, ".")
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", fmt.Errorf("host is required")
	}
	return host, nil
}

// MatchNormalizedHost matches a normalized concrete host against a normalized
// allowlist-style pattern.
func MatchNormalizedHost(host string, pattern string) bool {
	if host == "" || pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*.")
		return strings.HasSuffix(host, "."+suffix)
	}
	if host == pattern {
		return true
	}
	return strings.HasSuffix(host, "."+pattern)
}

func validateHost(host string) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("host is required")
	}
	if len(host) > 253 {
		return fmt.Errorf("host is too long")
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" {
			return fmt.Errorf("host contains an empty label")
		}
		if len(label) > 63 {
			return fmt.Errorf("host label %q is too long", label)
		}
		if !hostLabelPattern.MatchString(label) {
			return fmt.Errorf("host label %q contains invalid characters", label)
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("host label %q must not start or end with \"-\"", label)
		}
	}
	return nil
}
