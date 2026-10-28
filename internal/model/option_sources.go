// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

type OptionSource uint8

const (
	SourceUnset OptionSource = iota
	SourceDefault
	SourceGlobal
	SourceProject
	SourceToolOverride
	SourceCLI
)

// ConfigScope names the config file a layer was read from.
type ConfigScope uint8

const (
	ScopeNone ConfigScope = iota
	ScopeGlobal
	ScopeProject
)

// EntryOrigin records which layer contributed one entry of a list option.
type EntryOrigin struct {
	Source OptionSource
	Scope  ConfigScope
	// Tool is set for entries from tool_overrides.<tool>.
	Tool string
	// Path is the config file, when known.
	Path string
}

// ListOrigins maps an option name to the origin of each entry in its resolved list.
type ListOrigins map[string]map[string]EntryOrigin

// Origin returns the recorded origin of entry in option, if any.
func (o ListOrigins) Origin(option string, entry string) (EntryOrigin, bool) {
	origin, ok := o[option][entry]
	return origin, ok
}

// ListProblem is a list entry that merging the layers rejected or found to have no effect.
type ListProblem struct {
	Option  string
	Entry   string
	Origin  EntryOrigin
	Message string
	Warning bool
}
