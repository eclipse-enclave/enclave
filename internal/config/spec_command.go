// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// specMetadataFields are the top-level keys that describe an extension rather
// than change what it does. They are the only keys a command extension accepts.
var specMetadataFields = map[string]bool{
	"schemaVersion": true,
	"kind":          true,
	"name":          true,
	"displayName":   true,
	"description":   true,
}

// specRuntimeFields lists the top-level keys doc sets beyond its metadata,
// spelled as in the document and sorted.
func specRuntimeFields(doc specDocument) []string {
	value := reflect.ValueOf(doc)
	typ := value.Type()
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		key, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if key == "" || key == "-" || specMetadataFields[key] || value.Field(i).IsZero() {
			continue
		}
		fields = append(fields, key)
	}
	sort.Strings(fields)
	return fields
}

// validateCommandSpec rejects every runtime field on a command extension. The
// deferred sbx fields only warn, but here a declared field can never take
// effect, since a command extension reaches no image and no session.
func validateCommandSpec(doc specDocument, specPath string) error {
	if doc.Kind != KindCommand {
		return nil
	}
	fields := specRuntimeFields(doc)
	if len(fields) == 0 {
		return nil
	}
	if doc.Commands != nil {
		// The sbx commands: key (install, startup, initFiles) and the commands/
		// directory share a name, so point at the one that ships verbs.
		return fmt.Errorf("%s: commands: (install/startup/initFiles) does not apply to kind %q; "+
			"ship executables in commands/host/ instead", specPath, KindCommand)
	}
	return fmt.Errorf("%s: kind %q accepts only schemaVersion, kind, name, displayName and description; "+
		"remove %s", specPath, KindCommand, strings.Join(fields, ", "))
}
