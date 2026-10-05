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
	"testing/fstest"

	"enclave/internal/model"
)

func TestEmbeddedSpecNames(t *testing.T) {
	files := fstest.MapFS{
		"extensions/tools/yaml/spec.yaml":        {},
		"extensions/tools/json/spec.json":        {},
		"extensions/tools/no-spec/README.md":     {},
		"extensions/tools/.incoming-x/spec.yaml": {},
		"extensions/tools/stray-file":            {},
		"extensions/features/feat/spec.yaml":     {},
	}
	got, err := embeddedSpecNames(files, model.KindTool)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"json", "yaml"}; !reflect.DeepEqual(got, want) {
		t.Errorf("embeddedSpecNames = %v, want %v", got, want)
	}
}

func TestMergeNamesSortsAndDeduplicates(t *testing.T) {
	got := mergeNames([]string{"b", "a"}, []string{"c", "a"})
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("mergeNames = %v, want %v", got, want)
	}
}
