// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import "testing"

func TestParseSizeAcceptedForms(t *testing.T) {
	cases := map[string]int64{
		"":       0,
		"0":      0,
		"off":    0,
		"OFF":    0,
		"none":   0,
		"512":    512,
		"512B":   512,
		"512KB":  512 * 1024,
		"512kb":  512 * 1024,
		"512 KB": 512 * 1024,
		"32MB":   32 * 1024 * 1024,
		"32M":    32 * 1024 * 1024,
		"1GB":    1024 * 1024 * 1024,
		"1g":     1024 * 1024 * 1024,
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := ParseSize(input)
			if err != nil {
				t.Fatalf("ParseSize(%q) error = %v", input, err)
			}
			if got != want {
				t.Fatalf("ParseSize(%q) = %d, want %d", input, got, want)
			}
		})
	}
}

func TestParseSizeRejectsMalformedValues(t *testing.T) {
	for _, input := range []string{"MB", "-1", "-1MB", "1.5MB", "12TB", "twelve", "1 2 MB", "1KBB"} {
		t.Run(input, func(t *testing.T) {
			if got, err := ParseSize(input); err == nil {
				t.Fatalf("ParseSize(%q) = %d, want an error", input, got)
			}
		})
	}
}

func TestFormatBytesUsesTheSameBinaryBase(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		4300:    "4.2 KB",
		18432:   "18 KB",
		186368:  "182 KB",
		1468006: "1.4 MB",
	}
	for input, want := range cases {
		if got := FormatBytes(input); got != want {
			t.Fatalf("FormatBytes(%d) = %q, want %q", input, got, want)
		}
	}
}
