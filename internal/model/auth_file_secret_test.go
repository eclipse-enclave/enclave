// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"reflect"
	"strings"
	"testing"
)

func TestAuthPointerSegments(t *testing.T) {
	for _, tc := range []struct {
		pointer string
		want    []string
	}{{"/", []string{""}}, {"/a~1b/~0/*", []string{"a/b", "~", "*"}}, {"/tokens/0/access", []string{"tokens", "0", "access"}}} {
		got, err := AuthPointerSegments(tc.pointer)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("parse %q: %v, %v", tc.pointer, got, err)
		}
	}
	for _, pointer := range []string{"", "not/a/pointer", "/bad~2escape", "/bad~", "/*/again/*", "/nul\x00", strings.Repeat("/a", 33)} {
		if _, err := AuthPointerSegments(pointer); err == nil {
			t.Fatalf("accepted invalid pointer %q", pointer)
		}
	}
}
