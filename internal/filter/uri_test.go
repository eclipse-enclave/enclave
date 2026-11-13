// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import "testing"

func TestCheckRawPathAndQuery(t *testing.T) {
	allPath := "/AZaz09-._~!$&'()*+,;=:@%2a%C3%A4/x"
	if err := CheckRawPath(allPath); err != nil {
		t.Fatalf("CheckRawPath(%q) = %v", allPath, err)
	}
	if err := CheckRawQuery(allPath[1:] + "?/"); err != nil {
		t.Fatalf("CheckRawQuery = %v", err)
	}
	if err := CheckRawPath("/a?b"); err == nil {
		t.Fatal("CheckRawPath accepted \"?\"")
	}
	for _, bad := range []string{"#", "[", "]", " ", "\"", "<", ">", "\\", "^", "`", "{", "|", "}", "\x00", "\x7f", "\xc3\xa4", "%", "%2", "%zz"} {
		if err := CheckRawPath("/a" + bad); err == nil {
			t.Errorf("CheckRawPath accepted %q", bad)
		}
		if err := CheckRawQuery("a=" + bad); err == nil {
			t.Errorf("CheckRawQuery accepted %q", bad)
		}
	}
}

func TestValidQueryParamName(t *testing.T) {
	for _, name := range []string{"alt", "requestMask.includeField", "page_token", "a~b-c", "X1"} {
		if !ValidQueryParamName(name) {
			t.Errorf("ValidQueryParamName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "$alt", "a b", "a+b", "ids[]", "förmat", "a\x00"} {
		if ValidQueryParamName(name) {
			t.Errorf("ValidQueryParamName(%q) = true", name)
		}
	}
}
