// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"fmt"
	"io"

	"enclave/internal/extinstall"
	"enclave/internal/model"
)

// extensionSchemaVersion is the envelope version shared by `list --json` and
// the add/update/remove result envelope. Both envelopes are integration
// contracts: add fields, never rename or remove them.
const extensionSchemaVersion = "1"

// extensionListEnvelope is the JSON shape of `list --json`.
type extensionListEnvelope struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Kind          string                 `json:"kind"`
	Extensions    []extensionListJSONExt `json:"extensions"`
}

// extensionListJSONExt is the JSON shape of one extension in `list --json`.
type extensionListJSONExt struct {
	Name        string               `json:"name"`
	DisplayName string               `json:"displayName,omitempty"`
	Description string               `json:"description,omitempty"`
	Source      string               `json:"source"`
	Enabled     bool                 `json:"enabled"`
	Managed     bool                 `json:"managed"`
	Origin      *extensionListOrigin `json:"origin,omitempty"`
	// OriginError carries extinstall.Managed.Problem: provenance for a managed
	// directory could not be read (e.g. a corrupt sidecar). When it is set,
	// Origin is nil and Managed is false. Additive to the v1 contract.
	OriginError string `json:"originError,omitempty"`
}

// extensionListOrigin mirrors extinstall.Origin plus locallyModified, which is
// derived rather than stored on the sidecar.
type extensionListOrigin struct {
	Remote          string `json:"remote"`
	Source          string `json:"source"`
	Subpath         string `json:"subpath,omitempty"`
	Ref             string `json:"ref"`
	RefType         string `json:"refType"`
	Commit          string `json:"commit"`
	InstalledAt     string `json:"installedAt,omitempty"`
	LocallyModified bool   `json:"locallyModified"`
}

// extensionListJSONEntries projects extensions and their inventory into the
// `list --json` entry shape. enabled reports whether an extension would be
// active for this invocation.
func extensionListJSONEntries(exts []model.Extension, inventory map[string]extinstall.Managed, enabled func(name string) bool) []extensionListJSONExt {
	entries := make([]extensionListJSONExt, 0, len(exts))
	for _, ext := range exts {
		managed := inventory[ext.Name]
		item := extensionListJSONExt{
			Name:        ext.Name,
			DisplayName: ext.DisplayName,
			Description: ext.Description,
			Source:      managed.Source,
			Enabled:     enabled(ext.Name),
			Managed:     managed.Origin != nil,
			OriginError: managed.Problem,
		}
		if managed.Origin != nil {
			item.Origin = &extensionListOrigin{
				Remote:          managed.Origin.Remote,
				Source:          managed.Origin.Source,
				Subpath:         managed.Origin.Subpath,
				Ref:             managed.Origin.Ref,
				RefType:         managed.Origin.RefType,
				Commit:          managed.Origin.Commit,
				InstalledAt:     managed.Origin.InstalledAt,
				LocallyModified: managed.Modified,
			}
		}
		entries = append(entries, item)
	}
	return entries
}

func renderExtensionListJSON(w io.Writer, kind model.ExtensionKind, entries []extensionListJSONExt) error {
	if entries == nil {
		// "extensions" is always a JSON array, never null.
		entries = []extensionListJSONExt{}
	}
	return encodeIndentedJSON(w, extensionListEnvelope{
		SchemaVersion: extensionSchemaVersion,
		Kind:          string(kind),
		Extensions:    entries,
	})
}

// extensionResultsEnvelope is the JSON shape of the add/update/remove result
// envelope.
type extensionResultsEnvelope struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Kind          string                    `json:"kind"`
	Results       []extinstall.ActionResult `json:"results"`
}

func writeExtensionResultsJSON(w io.Writer, kind model.ExtensionKind, results []extinstall.ActionResult) error {
	if results == nil {
		// "results" is always a JSON array, never null.
		results = []extinstall.ActionResult{}
	}
	return encodeIndentedJSON(w, extensionResultsEnvelope{
		SchemaVersion: extensionSchemaVersion,
		Kind:          string(kind),
		Results:       results,
	})
}

func encodeIndentedJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// provenanceSuffix renders the bracketed provenance appended to a listing line
// for a managed extension: the text counterpart of the origin object in
// `list --json`. Unmanaged and built-in entries get nothing.
func provenanceSuffix(entry extinstall.Managed) string {
	if entry.Origin == nil {
		if entry.Problem != "" {
			return " [provenance unreadable]"
		}
		return ""
	}
	label := entry.Origin.Source
	if label == "" {
		label = entry.Origin.Remote
	}
	suffix := fmt.Sprintf(" [%s@%s", label, extinstall.ShortCommit(entry.Origin.Commit))
	if entry.Modified {
		suffix += ", modified"
	}
	return suffix + "]"
}
