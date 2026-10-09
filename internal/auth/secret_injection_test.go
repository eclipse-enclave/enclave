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

	"enclave/internal/model"
)

func TestResolvePlaceholder(t *testing.T) {
	for _, encoding := range []string{"", "hex", "base64url", "alphanumeric"} {
		t.Run(encoding, func(t *testing.T) {
			var shape *model.SecretPlaceholderConfig
			prefix, suffix, alphabet, length := placeholderPrefix, "", "0123456789abcdef", 48
			if encoding != "" {
				shape = &model.SecretPlaceholderConfig{Prefix: "vendor_", Suffix: "_end", Random: model.SecretPlaceholderRandom{Encoding: encoding, Length: 48}}
				prefix, suffix = shape.Prefix, shape.Suffix
				alphabet, _ = shape.ValidatedAlphabet()
			}
			resolver := NewPlaceholderResolver()
			first, err := resolver.ResolvePlaceholder("api-key", shape)
			if err != nil {
				t.Fatal(err)
			}
			second, err := resolver.ResolvePlaceholder("api-key", shape)
			if err != nil || first != second {
				t.Fatalf("unstable placeholder: %q %q %v", first, second, err)
			}
			if !strings.HasPrefix(first, prefix) || !strings.HasSuffix(first, suffix) || len(first) != len(prefix)+length+len(suffix) {
				t.Fatalf("wrong shape: %q", first)
			}
			for _, c := range first[len(prefix) : len(first)-len(suffix)] {
				if !strings.ContainsRune(alphabet, c) {
					t.Fatalf("invalid character %q", c)
				}
			}
			other, err := resolver.ResolvePlaceholder("other-key", shape)
			if err != nil || other == first {
				t.Fatalf("duplicate placeholder: %v", err)
			}
			fresh, err := NewPlaceholderResolver().ResolvePlaceholder("api-key", shape)
			if err != nil || fresh == first {
				t.Fatalf("placeholder reused across sessions: %v", err)
			}
		})
	}
}

func TestResolvePlaceholderRejectsInvalidShape(t *testing.T) {
	_, err := NewPlaceholderResolver().ResolvePlaceholder("key", &model.SecretPlaceholderConfig{})
	if err == nil {
		t.Fatal("expected invalid shape error")
	}
}
