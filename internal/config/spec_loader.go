// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"enclave/internal/model"
)

// LoadSpec resolves and parses the spec.yaml (or spec.json) document for the
// extension named name, expecting the given kind (KindSandbox or KindMixin).
// It returns os.ErrNotExist if neither file is present.
func LoadSpec(paths model.Paths, name string, kind string) (specDocument, error) {
	specPath, ok := resolveSpecFile(paths, name, kind)
	if !ok {
		return specDocument{}, os.ErrNotExist
	}

	// #nosec G304 -- specPath is resolved from trusted extension roots.
	data, err := os.ReadFile(specPath)
	if err != nil {
		return specDocument{}, err
	}

	// Strict parsing: unknown keys and duplicate keys are load errors, so a
	// typo'd field (e.g. a misspelled network: or deniedDomains:) can never
	// silently disable deny rules or credential-release validation.
	var doc specDocument
	if err := yaml.UnmarshalStrict(data, &doc); err != nil {
		return specDocument{}, fmt.Errorf("parse %s: %w", specPath, err)
	}

	if doc.SchemaVersion != SpecSchemaVersion {
		return specDocument{}, fmt.Errorf("%s schemaVersion must be %q (got %q)", specPath, SpecSchemaVersion, doc.SchemaVersion)
	}
	if doc.Kind != kind {
		return specDocument{}, fmt.Errorf("%s kind must be %q", specPath, kind)
	}
	if doc.Name != name {
		return specDocument{}, fmt.Errorf("%s name must be %q", specPath, name)
	}
	if err := validateStartupCommands(doc, specPath); err != nil {
		return specDocument{}, err
	}
	if err := validateServiceAuthMappings(doc, specPath); err != nil {
		return specDocument{}, err
	}
	if err := validateProxyManaged(doc, specPath); err != nil {
		return specDocument{}, err
	}
	if err := validateEntrypointArgv(doc, specPath); err != nil {
		return specDocument{}, err
	}

	warnReservedFields(doc, name, specWarn)

	warnUnknownFilesEntries(filepath.Join(filepath.Dir(specPath), "files"), name, specWarn)

	return doc, nil
}

// warnUnknownFilesEntries warns for any entry directly under an extension's
// files/ directory that the loader does not honor. files/home (baked into the
// image) and files/workspace (copied into the project at startup) are honored;
// anything else is an unknown files/ subdir or a stray file and is flagged so a
// misplaced payload is not silently ignored.
func warnUnknownFilesEntries(filesDir string, name string, warn func(string)) {
	if !isDir(filesDir) {
		return
	}
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		warn(fmt.Sprintf("extension %q: cannot read %s: %v", name, filesDir, err))
		return
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "home", "workspace":
			continue
		default:
			warn(fmt.Sprintf("extension %q: %s is not a recognized files/ entry (only home and workspace are honored); ignoring", name, filepath.Join(filesDir, entry.Name())))
		}
	}
}

// resolveSpecFile finds spec.yaml, falling back to spec.json, via the same
// user-override-then-built-in resolution used for extension files.
func resolveSpecFile(paths model.Paths, name string, kind string) (string, bool) {
	var resolve func(model.Paths, string, string) (string, bool)
	switch kind {
	case KindSandbox:
		resolve = ResolveToolFile
	case KindMixin:
		resolve = ResolveFeatureFile
	default:
		return "", false
	}
	if p, ok := resolve(paths, name, SpecFilename); ok {
		return p, true
	}
	if p, ok := resolve(paths, name, SpecFilenameJSON); ok {
		return p, true
	}
	return "", false
}

// hasSpecFile reports whether a spec.yaml/spec.json exists for name, without
// parsing it. Used by the List* helpers to discover extensions.
func hasSpecFile(paths model.Paths, name string, kind string) bool {
	_, ok := resolveSpecFile(paths, name, kind)
	return ok
}

// specWarn is the sink for non-fatal warnings emitted while loading a spec
// document (reserved-field usage, unused files/ sibling directories, etc).
//
// It deliberately writes to STDERR rather than using logx.Warnf (which writes
// to stdout). ListTools/ListFeatures are invoked from Cobra dynamic-completion
// callbacks (internal/cli/completion.go), and Cobra's __complete protocol
// parses all of stdout as completion candidates — any warning printed to
// stdout during spec loading would corrupt shell completion output.
func specWarn(msg string) {
	_, _ = fmt.Fprintf(os.Stderr, "warn: %s\n", msg)
}

// specSourceLabel produces a human-readable identifier for a spec-sourced
// extension, used in place of a manifest file path in validation error
// messages (LoadSpec intentionally does not expose the resolved path).
func specSourceLabel(name string) string {
	return fmt.Sprintf("%s/%s", name, SpecFilename)
}

// loadSpecExtension loads a spec.yaml/spec.json document as name and maps it
// onto a validated model.Extension.
func loadSpecExtension(paths model.Paths, name string, kind string, expectedType string) (model.Extension, error) {
	doc, err := LoadSpec(paths, name, kind)
	if err != nil {
		return model.Extension{}, err
	}

	ext, state := specToExtension(doc)
	ext = applyExtensionDefaults(ext, name, expectedType, state)

	label := specSourceLabel(name)
	if err := validateExtensionIdentity(ext, name, expectedType, label); err != nil {
		return model.Extension{}, err
	}
	if err := validateAndNormalizeExtension(&ext, label); err != nil {
		return model.Extension{}, err
	}

	return ext, nil
}

// loadSpecProfile loads a sandbox spec.yaml/spec.json document as name and
// maps it onto a validated model.Profile.
func loadSpecProfile(paths model.Paths, name string) (model.Profile, error) {
	doc, err := LoadSpec(paths, name, KindSandbox)
	if err != nil {
		return model.Profile{}, err
	}

	profile := specToProfile(doc)
	if err := validateAndNormalizeProfile(&profile); err != nil {
		return model.Profile{}, fmt.Errorf("invalid profile %s: %w", specSourceLabel(name), err)
	}

	return profile, nil
}
