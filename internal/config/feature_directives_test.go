// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"reflect"
	"testing"
)

func TestSplitFeatureDirectives(t *testing.T) {
	additions, removals := SplitFeatureDirectives([]string{" node-dev ", "+ All", "-shell-extras", "- default", "+", "-", ""})
	if want := []string{"node-dev", "All"}; !reflect.DeepEqual(additions, want) {
		t.Errorf("additions = %v, want %v", additions, want)
	}
	if want := []string{"shell-extras", "default"}; !reflect.DeepEqual(removals, want) {
		t.Errorf("removals = %v, want %v", removals, want)
	}
}

func TestHasFeatureSelectors(t *testing.T) {
	cases := map[string]struct {
		values []string
		want   bool
	}{
		"bare":            {[]string{"default"}, true},
		"prefixed":        {[]string{"+ALL"}, true},
		"removal is not":  {[]string{"-default"}, false},
		"plain names":     {[]string{"node-dev", "+shell-extras"}, false},
		"empty selection": {[]string{}, false},
	}
	for name, tc := range cases {
		if got := HasFeatureSelectors(tc.values); got != tc.want {
			t.Errorf("%s: HasFeatureSelectors(%v) = %v, want %v", name, tc.values, got, tc.want)
		}
	}
}
