// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"reflect"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestOptionSpecsCoverDefaults(t *testing.T) {
	specs := OptionSpecs()
	specNames := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if _, exists := specNames[spec.Name]; exists {
			t.Errorf("OptionSpecs: duplicate name %q", spec.Name)
			continue
		}
		specNames[spec.Name] = struct{}{}
	}

	defaultsType := reflect.TypeOf(Defaults{})
	for i := 0; i < defaultsType.NumField(); i++ {
		field := defaultsType.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if tag == "tool_overrides" {
			continue
		}
		if _, ok := specNames[tag]; !ok {
			t.Errorf("Defaults field %s (json %q) has no OptionSpec", field.Name, tag)
		}
	}
}

func TestOptionSpecsHaveSourceFields(t *testing.T) {
	sourceFields := optionSourceFieldNames()
	for _, spec := range OptionSpecs() {
		fieldName := optionFieldName(spec.Name)
		if _, ok := sourceFields[fieldName]; !ok {
			t.Errorf("OptionSpec %q missing OptionSources field %q", spec.Name, fieldName)
		}
	}
}

func optionSourceFieldNames() map[string]struct{} {
	return collectFieldNames(reflect.TypeOf(model.OptionSources{}))
}

func collectFieldNames(t reflect.Type) map[string]struct{} {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	fields := map[string]struct{}{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous {
			for name := range collectFieldNames(field.Type) {
				fields[name] = struct{}{}
			}
			continue
		}
		fields[field.Name] = struct{}{}
	}
	return fields
}

func optionFieldName(name string) string {
	parts := strings.Split(name, "_")
	var builder strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if initialism, ok := optionInitialisms[part]; ok {
			builder.WriteString(initialism)
			continue
		}
		builder.WriteString(strings.ToUpper(part[:1]))
		if len(part) > 1 {
			builder.WriteString(strings.ToLower(part[1:]))
		}
	}
	return builder.String()
}

var optionInitialisms = map[string]string{
	"api": "API",
	"dns": "DNS",
	"gid": "GID",
	"mcp": "MCP",
	"ssh": "SSH",
	"uid": "UID",
}
