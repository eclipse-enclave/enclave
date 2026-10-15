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
	"strings"

	"enclave/internal/extinstall"
	"enclave/internal/model"
	"enclave/internal/network"
)

// extensionSchemaVersion is the envelope version shared by `list --json` and
// the add/update/remove result envelope. Both envelopes are integration
// contracts: add fields, never rename or remove them.
const extensionSchemaVersion = "1"

// extensionListEnvelope is the JSON shape of `list --json`.
type extensionListEnvelope struct {
	SchemaVersion string `json:"schemaVersion"`
	Kind          string `json:"kind"`
	// Selection is set for features only. Additive to the v1 contract.
	Selection  *extensionListSelection `json:"selection,omitempty"`
	Extensions []extensionListJSONExt  `json:"extensions"`
}

// extensionListSelection describes the feature selection that produced each
// entry's enabled flag. Directives is null when no layer configures features.
type extensionListSelection struct {
	Directives []string `json:"directives"`
	Source     string   `json:"source"`
	Tool       *string  `json:"tool"`
}

// extensionListJSONExt is the JSON shape of one extension in `list --json`.
type extensionListJSONExt struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Enabled     bool   `json:"enabled"`
	Managed     bool   `json:"managed"`
	// HostCommands are the enclave verbs this extension contributes, which run
	// on the host outside the sandbox. They resolve whenever the extension is
	// installed, enabled or not, so a caller auditing which installed
	// extensions can execute host code reads this rather than Enabled.
	// Additive to the v1 contract.
	HostCommands []string             `json:"hostCommands,omitempty"`
	Origin       *extensionListOrigin `json:"origin,omitempty"`
	// OriginError carries extinstall.Managed.Problem: provenance for a managed
	// directory could not be read (e.g. a corrupt sidecar). When it is set,
	// Origin is nil and Managed is false. Additive to the v1 contract.
	OriginError string `json:"originError,omitempty"`
	// The remaining fields are set for features only. Additive to the v1
	// contract.
	DefaultEnabled *bool               `json:"defaultEnabled,omitempty"`
	NeedsRoot      *bool               `json:"needsRoot,omitempty"`
	Ports          []extensionListPort `json:"ports,omitempty"`
	AllowedDomains []string            `json:"allowedDomains,omitempty"`
	DeniedDomains  []string            `json:"deniedDomains,omitempty"`
	ReleaseHosts   []string            `json:"releaseHosts,omitempty"`
}

// extensionListPort mirrors model.PortConfig with camelCase keys.
type extensionListPort struct {
	Container      int    `json:"container"`
	Publish        bool   `json:"publish"`
	HostAllocation string `json:"hostAllocation"`
	Label          string `json:"label,omitempty"`
	OpenURL        string `json:"openUrl,omitempty"`
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
			Name:         ext.Name,
			DisplayName:  ext.DisplayName,
			Description:  ext.Description,
			Source:       managed.Source,
			Enabled:      enabled(ext.Name),
			Managed:      managed.Origin != nil,
			HostCommands: managed.HostCommands,
			OriginError:  managed.Problem,
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

// featureListJSONEntries is extensionListJSONEntries plus the feature-only
// fields.
func featureListJSONEntries(features []model.Extension, inventory map[string]extinstall.Managed, enabled func(name string) bool) []extensionListJSONExt {
	entries := extensionListJSONEntries(features, inventory, enabled)
	for i, feature := range features {
		entry := &entries[i]
		entry.DefaultEnabled = &feature.DefaultEnabled
		entry.NeedsRoot = &feature.NeedsRoot
		for _, port := range feature.Ports {
			allocation := port.HostAllocation
			if allocation == "" {
				allocation = model.HostAllocationFixed
			}
			entry.Ports = append(entry.Ports, extensionListPort{
				Container:      port.Container,
				Publish:        port.Publish,
				HostAllocation: allocation,
				Label:          port.Label,
				OpenURL:        port.OpenURL,
			})
		}
		entry.AllowedDomains = network.NormalizeSpecDomains(feature.AllowedDomains)
		entry.DeniedDomains = network.NormalizeSpecDomains(feature.DeniedDomains)
		entry.ReleaseHosts = model.ReleaseHosts(feature.Secrets)
	}
	return entries
}

func renderExtensionListJSON(w io.Writer, kind model.ExtensionKind, selection *extensionListSelection, entries []extensionListJSONExt) error {
	if entries == nil {
		// "extensions" is always a JSON array, never null.
		entries = []extensionListJSONExt{}
	}
	return encodeIndentedJSON(w, extensionListEnvelope{
		SchemaVersion: extensionSchemaVersion,
		Kind:          string(kind),
		Selection:     selection,
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

// listingSuffix renders everything appended to one extension's listing line:
// its provenance, then the host commands it contributes. Both are the text
// counterpart of a `list --json` field, so the two views stay in step.
func listingSuffix(entry extinstall.Managed) string {
	suffix := provenanceSuffix(entry)
	if len(entry.HostCommands) > 0 {
		suffix += fmt.Sprintf(" [host: %s]", strings.Join(entry.HostCommands, ", "))
	}
	return suffix
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
