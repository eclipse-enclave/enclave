// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "testing"

func TestShortHash(t *testing.T) {
	long := "0123456789abcdef0123" // 20 chars
	if got := ShortHash(long); got != long[:HashLength] {
		t.Fatalf("ShortHash(long) = %q, want %q", got, long[:HashLength])
	}
	if len(ShortHash(long)) != HashLength {
		t.Fatalf("ShortHash(long) length = %d, want %d", len(ShortHash(long)), HashLength)
	}

	exact := long[:HashLength] // exactly HashLength chars
	if got := ShortHash(exact); got != exact {
		t.Fatalf("ShortHash(exact) = %q, want unchanged %q", got, exact)
	}

	short := "abc"
	if got := ShortHash(short); got != short {
		t.Fatalf("ShortHash(short) = %q, want unchanged %q", got, short)
	}
	if got := ShortHash(""); got != "" {
		t.Fatalf("ShortHash(\"\") = %q, want empty", got)
	}
}
