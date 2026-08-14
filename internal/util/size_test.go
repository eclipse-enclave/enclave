// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import "testing"

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
