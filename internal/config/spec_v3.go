// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"bytes"
	"fmt"
	"io"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	yamlv3 "gopkg.in/yaml.v3"
)

func decodeV3Spec(data []byte, specPath string) (specDocument, error) {
	// yq reads every YAML document, whereas the upstream decoder reads one.
	// Refuse trailing documents so build/startup cannot see unvalidated config.
	decoder := yamlv3.NewDecoder(bytes.NewReader(data))
	var node yamlv3.Node
	if err := decoder.Decode(&node); err != nil {
		return specDocument{}, fmt.Errorf("parse %s: %w", specPath, err)
	}
	if err := decoder.Decode(&node); err != io.EOF {
		return specDocument{}, fmt.Errorf("%s: expected a single YAML document", specPath)
	}
	descriptor, err := spec.Decode(data)
	if err != nil {
		return specDocument{}, fmt.Errorf("parse %s: %w", specPath, err)
	}
	warnings, err := spec.ValidateRaw(data, descriptor)
	if err != nil {
		return specDocument{}, fmt.Errorf("invalid %s: %w", specPath, err)
	}
	for _, warning := range warnings {
		specWarn(fmt.Sprintf("%s: %s", specPath, warning))
	}
	// Enclave builds from sibling assets and selects features explicitly.
	// Reject declarations whose semantics would otherwise be silently lost.
	if descriptor.Kind == spec.KindSet || len(descriptor.Kits) != 0 || descriptor.Build != "" || descriptor.Dockerfile != "" || len(descriptor.Args) != 0 {
		return specDocument{}, fmt.Errorf("%s: Enclave does not support kit sets, build recipes, or args; use extension sibling assets", specPath)
	}
	if len(descriptor.Provides) != 0 || len(descriptor.Requires) != 0 || len(descriptor.Integrates) != 0 || len(descriptor.Conflicts) != 0 {
		return specDocument{}, fmt.Errorf("%s: Enclave does not support sbx dependency declarations; select tools and features explicitly", specPath)
	}
	doc := specDocument{
		SchemaVersion: SpecSchemaVersion,
		Kind:          KindMixin,
		DisplayName:   descriptor.DisplayName,
		Description:   descriptor.Description,
	}
	if descriptor.Kind == spec.KindWorkload {
		doc.Kind = KindSandbox
	}
	found := false
	for _, capability := range descriptor.Capabilities {
		if capability.Group != nil {
			return specDocument{}, fmt.Errorf("%s: Enclave does not support capability groups", specPath)
		}
		if capability.Type != EnclaveRuntimeCapability {
			if !capability.Optional {
				return specDocument{}, fmt.Errorf("%s: unsupported required capability %q", specPath, capability.Type)
			}
			specWarn(fmt.Sprintf("%s: skipping unsupported optional capability %q", specPath, capability.Type))
			continue
		}
		if found || capability.Optional {
			return specDocument{}, fmt.Errorf("%s: exactly one required %s capability is needed", specPath, EnclaveRuntimeCapability)
		}
		if err := spec.DecodeCapabilityConfig(capability, &doc.specRuntime); err != nil {
			return specDocument{}, fmt.Errorf("parse %s: %w", specPath, err)
		}
		found = true
	}
	if !found || doc.Name == "" {
		return specDocument{}, fmt.Errorf("%s: a required %s capability with config.name is needed", specPath, EnclaveRuntimeCapability)
	}
	return doc, nil
}
