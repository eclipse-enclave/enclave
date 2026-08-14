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

	"enclave/internal/model"
)

// TestMergeDefaultsCoversAllFields uses reflection to verify that
// mergeDefaults handles every field in the Defaults struct.
// This test fails if a new field is added but forgotten in the merge function.
func TestMergeDefaultsCoversAllFields(t *testing.T) {
	// Create an override with every field set to a non-zero value
	trueVal := true
	override := Defaults{
		Tool:             "test-tool",
		ToolOverrides:    map[string]Defaults{"test-tool": {NoAPIKey: &trueVal}},
		Backend:          "docker",
		HostConfig:       model.HostConfigPassthrough,
		HostConfigPaths:  []string{"default", "+commands/"},
		Yolo:             &trueVal,
		Ephemeral:        &trueVal,
		AuthScope:        "test-auth-scope",
		AuthName:         "test-auth-name",
		SecretsScope:     "test-secrets-scope",
		ResetAuth:        &trueVal,
		NoAPIKey:         &trueVal,
		PassAPIKey:       &trueVal,
		PassEnv:          []string{"TEST_ENV"},
		AllowAllNetwork:  &trueVal,
		NoCache:          &trueVal,
		NoHistory:        &trueVal,
		NoMemory:         &trueVal,
		SessionMonitor:   &trueVal,
		ImageInbox:       &trueVal,
		BaseImage:        "test-base-image",
		Devcontainer:     &trueVal,
		Slim:             &trueVal,
		ImageName:        "test-image-name",
		Features:         []string{"test-feature"},
		UseRemoteUser:    &trueVal,
		CacheFrom:        []string{"test-cache"},
		Progress:         "verbose",
		NetworkLog:       model.NetworkLogRequests,
		Verbose:          &trueVal,
		Ports:            []string{"8080"},
		AddDirs:          []string{"/test"},
		AddReadonlyDirs:  []string{"/test-ro"},
		ProjectMount:     model.ProjectMountReadonly,
		WorktreeMetadata: model.WorktreeMetadataReadonly,
		AllowDomains:     []string{"api.test.com"},
		BridgePorts:      []string{"9800"},
		PlaywrightMCP:    &trueVal,
	}

	base := Defaults{} // All fields zero value
	result := mergeDefaults(base, override)

	// Use reflection to verify every field in result is non-zero
	v := reflect.ValueOf(result)
	typ := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldName := typ.Field(i).Name

		if field.IsZero() {
			t.Errorf("mergeDefaults: field %s was not merged (still zero value) - add it to mergeDefaults()", fieldName)
		}
	}

	// Also verify we haven't forgotten any fields in our test override
	overrideV := reflect.ValueOf(override)
	for i := 0; i < overrideV.NumField(); i++ {
		field := overrideV.Field(i)
		fieldName := typ.Field(i).Name

		if field.IsZero() {
			t.Errorf("Test setup error: override.%s is zero - add a test value to the override struct", fieldName)
		}
	}
}
