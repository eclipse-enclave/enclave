// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

// listEntryValidators normalize the value of one extras list entry, or
// explain why it is invalid. Paths and cache refs only need to be non-empty;
// whether a directory exists is checked when it is mounted.
var listEntryValidators = map[string]func(string) (string, error){
	"ports": func(value string) (string, error) {
		if _, _, _, ok := util.ParsePortSpec(value); !ok {
			return "", errors.New("expected PORT, HOST:CONTAINER, or IP:HOST:CONTAINER")
		}
		return value, nil
	},
	"allow_domains": func(value string) (string, error) {
		value = strings.ToLower(value)
		if !IsValidDomain(value) {
			return "", errors.New("not a valid domain name")
		}
		return value, nil
	},
	"bridge_ports": func(value string) (string, error) {
		if !util.IsPortNumber(value) {
			return "", errors.New("must be a port number (1-65535)")
		}
		return value, nil
	},
	"pass_env": func(value string) (string, error) {
		if !isValidEnvKey(value) {
			return "", errors.New("not a valid environment variable name")
		}
		return value, nil
	},
}

// ValidateListEntry validates one raw entry of an extras list option. It
// strips one +/- prefix and returns the prefix, if any, and the normalized
// value. The none keyword is returned as is.
func ValidateListEntry(option string, raw string) (op string, value string, err error) {
	d, isNone := parseListDirective(raw)
	if isNone {
		return "", listNone, nil
	}
	if d.op != 0 {
		op = string(d.op)
	}
	if d.value == "" {
		return op, "", errors.New("value cannot be empty")
	}
	validate, ok := listEntryValidators[option]
	if !ok {
		return op, d.value, nil
	}
	value, err = validate(d.value)
	return op, value, err
}

// IsValidDomain reports whether s is a bare DNS name: lowercase ASCII
// letters/digits/hyphens, dots between labels, no scheme/path/port.
func IsValidDomain(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	if strings.ContainsAny(s, "/:@?#") {
		return false
	}
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// RedactListEntry returns an entry fit for a message. A pass_env entry
// written as NAME=value may carry a secret, so its value is elided.
func RedactListEntry(option string, raw string) string {
	value := strings.TrimSpace(raw)
	if option == "pass_env" {
		if name, _, hasValue := strings.Cut(value, "="); hasValue {
			return name + "=…"
		}
	}
	return value
}

// ListOriginLabel names where a list entry was set, as a user would look for
// it: the CLI flag, or the key and the config file (its scope when the path
// is unknown).
func ListOriginLabel(option string, origin model.EntryOrigin) string {
	if origin.Source == model.SourceCLI {
		for _, def := range OptionDefs() {
			if def.Name == option && len(def.CLIFlags) > 0 {
				return def.CLIFlags[0].Name
			}
		}
		return option
	}
	key := option
	if origin.Tool != "" {
		key = "tool_overrides." + origin.Tool + "." + option
	}
	if origin.Path != "" {
		return key + " in " + origin.Path
	}
	switch origin.Scope {
	case model.ScopeGlobal:
		return key + " in global config"
	case model.ScopeProject:
		return key + " in project config"
	default:
		return key
	}
}

// ListEntryOriginLabel is ListOriginLabel for an entry of a resolved list.
func ListEntryOriginLabel(option string, origins model.ListOrigins, entry string) string {
	origin, ok := origins.Origin(option, entry)
	if !ok {
		return option
	}
	return ListOriginLabel(option, origin)
}

// CheckListProblems reports what merging the list layers found: the first
// invalid entry as an error, and entries that had no effect as warnings.
func CheckListProblems(opts model.Options) ([]string, error) {
	var warnings []string
	for _, problem := range opts.ListProblems {
		message := ListOriginLabel(problem.Option, problem.Origin) + ": " + problem.Message
		if !problem.Warning {
			return warnings, errors.New(message)
		}
		warnings = append(warnings, message)
	}
	return warnings, nil
}
