// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"strings"
	"testing"
)

func TestResolvePlaceholderStablePerEnvVar(t *testing.T) {
	resolver := NewPlaceholderResolver()
	first, err := resolver.ResolvePlaceholder("API_KEY")
	if err != nil {
		t.Fatalf("ResolvePlaceholder() error = %v", err)
	}
	second, err := resolver.ResolvePlaceholder("API_KEY")
	if err != nil {
		t.Fatalf("ResolvePlaceholder() second call error = %v", err)
	}
	if first != second {
		t.Fatalf("placeholder changed for same env var: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, placeholderPrefix) {
		t.Fatalf("placeholder %q missing prefix %q", first, placeholderPrefix)
	}
}

func TestResolvePlaceholderDiffersAcrossEnvVars(t *testing.T) {
	resolver := NewPlaceholderResolver()

	first, err := resolver.ResolvePlaceholder("API_KEY")
	if err != nil {
		t.Fatalf("ResolvePlaceholder(API_KEY) error = %v", err)
	}
	second, err := resolver.ResolvePlaceholder("OTHER_API_KEY")
	if err != nil {
		t.Fatalf("ResolvePlaceholder(OTHER_API_KEY) error = %v", err)
	}
	if first == second {
		t.Fatalf("different env vars produced same placeholder %q", first)
	}
}
