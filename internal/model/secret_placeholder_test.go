// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"strings"
	"testing"
)

func TestSecretPlaceholderValidation(t *testing.T) {
	for _, tc := range []struct {
		encoding string
		length   int
		literal  string
		valid    bool
	}{
		{"hex", 32, "vendor_", true}, {"hex", 31, "", false},
		{"base64url", 22, "", true}, {"base64url", 21, "", false},
		{"alphanumeric", 22, "", true}, {"alphanumeric", 21, "", false},
		{"hex", 1024, "", true}, {"hex", 1025, "", false},
		{"", 48, "", false}, {"unknown", 48, "", false}, {"hex", 0, "", false}, {"hex", -1, "", false},
		{"hex", 32, " ", false}, {"hex", 32, "\r\n", false}, {"hex", 32, "\x00", false},
		{"hex", 32, "é", false}, {"hex", 32, strings.Repeat("x", 256), true}, {"hex", 32, strings.Repeat("x", 257), false},
	} {
		for _, suffix := range []bool{false, true} {
			c := SecretPlaceholderConfig{Random: SecretPlaceholderRandom{Encoding: tc.encoding, Length: tc.length}}
			if suffix {
				c.Suffix = tc.literal
			} else {
				c.Prefix = tc.literal
			}
			_, err := c.ValidatedAlphabet()
			if (err == nil) != tc.valid {
				t.Fatalf("%+v: error = %v", tc, err)
			}
		}
	}
}
