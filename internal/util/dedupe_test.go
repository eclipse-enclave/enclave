// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"reflect"
	"testing"
)

func TestDedupe(t *testing.T) {
	// First-seen order is preserved.
	if got := Dedupe([]string{"b", "a", "b", "c", "a"}); !reflect.DeepEqual(got, []string{"b", "a", "c"}) {
		t.Fatalf("Dedupe order = %v, want [b a c]", got)
	}
	// Empty and nil input return nil (not an empty non-nil slice).
	if got := Dedupe([]string{}); got != nil {
		t.Fatalf("Dedupe(empty) = %v, want nil", got)
	}
	if got := Dedupe[string](nil); got != nil {
		t.Fatalf("Dedupe(nil) = %v, want nil", got)
	}
	// Works for any comparable type.
	if got := Dedupe([]int{1, 1, 2, 3, 2}); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("Dedupe ints = %v, want [1 2 3]", got)
	}
}
