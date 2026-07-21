// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"reflect"
	"testing"
)

// TestMergeOptionSourcesCoversAllFields uses reflection to verify that
// MergeOptionSources handles every field in the OptionSources struct.
// This test fails if a new field is added but forgotten in the merge function.
func TestMergeOptionSourcesCoversAllFields(t *testing.T) {
	// Create an override with every field set to SourceCLI (non-default value)
	override := OptionSources{
		GlobalOptionSources: GlobalOptionSources{
			Verbose: SourceCLI,
		},
		RunOptionSources: RunOptionSources{
			Tool:             SourceCLI,
			Backend:          SourceCLI,
			HostConfig:       SourceCLI,
			HostConfigPaths:  SourceCLI,
			Yolo:             SourceCLI,
			Ephemeral:        SourceCLI,
			AllowAllNetwork:  SourceCLI,
			NoCache:          SourceCLI,
			NoHistory:        SourceCLI,
			NoMemory:         SourceCLI,
			SessionMonitor:   SourceCLI,
			ImageInbox:       SourceCLI,
			NetworkLog:       SourceCLI,
			Ports:            SourceCLI,
			AddDirs:          SourceCLI,
			AddReadonlyDirs:  SourceCLI,
			ProjectMount:     SourceCLI,
			WorktreeMetadata: SourceCLI,
			AllowDomains:     SourceCLI,
			BridgePorts:      SourceCLI,
			SessionName:      SourceCLI,
			TheiaAPIPort:     SourceCLI,
			TheiaAPIToken:    SourceCLI,
			PlaywrightMCP:    SourceCLI,
		},
		AuthOptionSources: AuthOptionSources{
			AuthScope:    SourceCLI,
			AuthName:     SourceCLI,
			ResetAuth:    SourceCLI,
			NoAPIKey:     SourceCLI,
			PassAPIKey:   SourceCLI,
			SecretsScope: SourceCLI,
			PassEnv:      SourceCLI,
		},
		BuildOptionSources: BuildOptionSources{
			ForceRebuild:    SourceCLI,
			NoRebuild:       SourceCLI,
			ForceBaseImage:  SourceCLI,
			BaseImage:       SourceCLI,
			Devcontainer:    SourceCLI,
			Slim:            SourceCLI,
			ImageName:       SourceCLI,
			Features:        SourceCLI,
			UseRemoteUser:   SourceCLI,
			CacheFrom:       SourceCLI,
			BuildUID:        SourceCLI,
			BuildGID:        SourceCLI,
			RuntimeUIDRemap: SourceCLI,
			BuildxCacheDir:  SourceCLI,
			BuildxCacheFrom: SourceCLI,
			BuildxCacheTo:   SourceCLI,
			Progress:        SourceCLI,
		},
	}

	base := OptionSources{} // All fields SourceUnset (zero value)
	result := MergeOptionSources(base, override)

	// Use reflection to verify every OptionSource field in result is set (not SourceUnset)
	checkAllFieldsSet(t, "MergeOptionSources", result)
}

// TestDefaultOptionSourcesCoversAllFields verifies that DefaultOptionSources
// initializes every field to SourceDefault.
func TestDefaultOptionSourcesCoversAllFields(t *testing.T) {
	result := DefaultOptionSources()
	checkAllFieldsSet(t, "DefaultOptionSources", result)
}

// checkAllFieldsSet recursively checks that all OptionSource fields are non-zero.
func checkAllFieldsSet(t *testing.T, funcName string, obj any) {
	t.Helper()
	v := reflect.ValueOf(obj)
	checkFields(t, funcName, v, "")
}

func checkFields(t *testing.T, funcName string, v reflect.Value, prefix string) {
	t.Helper()
	typ := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldType := typ.Field(i)
		fieldName := fieldType.Name
		if prefix != "" {
			fieldName = prefix + "." + fieldName
		}

		// If it's an embedded struct, recurse into it
		if field.Kind() == reflect.Struct && fieldType.Anonymous {
			checkFields(t, funcName, field, fieldName)
			continue
		}

		// Check that the field is an OptionSource
		if field.Type() != reflect.TypeOf(OptionSource(0)) {
			t.Errorf("%s: field %s has unexpected type %v (expected OptionSource)", funcName, fieldName, field.Type())
			continue
		}

		// For MergeOptionSources test, check field equals SourceCLI
		// For DefaultOptionSources test, check field equals SourceDefault
		val := field.Interface().(OptionSource)
		if val == SourceUnset {
			t.Errorf("%s: field %s was not set (still SourceUnset) - add it to the function", funcName, fieldName)
		}
	}
}
