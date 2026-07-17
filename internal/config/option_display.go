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

	"enclave/internal/model"
)

// This file derives the per-option display values used by `enclave config`
// (default/defaults/cli/effective value and effective source) generically from
// an OptionDef, via reflection over the model.Options / Defaults /
// model.OptionSources field named by the def, keyed on the option Kind. It
// replaces five closures that scripts/gen_options.go previously emitted per
// option. Only the `yolo` kind needs bespoke handling.

func optField(v any, name string) reflect.Value {
	return reflect.ValueOf(v).FieldByName(name)
}

func optString(v any, name string) string { return optField(v, name).String() }

func optBool(v any, name string) bool { return optField(v, name).Bool() }

func optSlice(v any, name string) []string {
	f := optField(v, name)
	if f.IsNil() {
		return nil
	}
	return f.Interface().([]string)
}

func optBoolPtr(v any, name string) *bool {
	f := optField(v, name)
	if f.IsNil() {
		return nil
	}
	return f.Interface().(*bool)
}

func optSource(sources model.OptionSources, name string) model.OptionSource {
	return optField(sources, name).Interface().(model.OptionSource)
}

// OptionDefaultValue returns the built-in default value (from resolved Options)
// for display, with a bool reporting whether it is considered "set".
func OptionDefaultValue(def OptionDef, opts model.Options) (string, bool) {
	switch def.Kind {
	case OptionKindYolo:
		return "tool-default", true
	case OptionKindBool:
		return boolValue(optBool(opts, def.OptionField))
	case OptionKindStringSlice:
		return sliceValue(optSlice(opts, def.OptionField))
	default:
		return stringValue(optString(opts, def.OptionField), def.DefaultRequire)
	}
}

// OptionDefaultsValue returns the value a config Defaults layer (global,
// project, or tool override) contributes for display.
func OptionDefaultsValue(def OptionDef, defaults Defaults) (string, bool) {
	if def.DefaultsField == "" {
		return "", false
	}
	switch def.Kind {
	case OptionKindYolo, OptionKindBool:
		return boolPtrValue(optBoolPtr(defaults, def.DefaultsField))
	case OptionKindStringSlice:
		return sliceValue(optSlice(defaults, def.DefaultsField))
	default:
		return stringValue(optString(defaults, def.DefaultsField), false)
	}
}

// OptionCLIValue returns the value for display only when this option's effective
// source is the command line.
func OptionCLIValue(def OptionDef, opts model.Options) (string, bool) {
	if optSource(opts.Sources, def.SourceField) != model.SourceCLI {
		return "", false
	}
	switch def.Kind {
	case OptionKindYolo:
		if opts.YoloOverride != nil {
			return boolValue(*opts.YoloOverride)
		}
		return "", false
	case OptionKindBool:
		return boolValue(optBool(opts, def.OptionField))
	case OptionKindStringSlice:
		return sliceValue(optSlice(opts, def.OptionField))
	default:
		return stringValue(optString(opts, def.OptionField), true)
	}
}

// OptionEffectiveValue returns the resolved effective value for display.
func OptionEffectiveValue(def OptionDef, opts model.Options) string {
	switch def.Kind {
	case OptionKindYolo:
		if opts.YoloOverride != nil {
			return fmt.Sprintf("%t", *opts.YoloOverride)
		}
		if opts.ConfigDefaultYolo != nil {
			return fmt.Sprintf("%t", *opts.ConfigDefaultYolo)
		}
		return "tool-default"
	case OptionKindBool:
		return fmt.Sprintf("%t", optBool(opts, def.OptionField))
	case OptionKindStringSlice:
		return formatSlice(optSlice(opts, def.OptionField))
	default:
		return optString(opts, def.OptionField)
	}
}

// OptionEffectiveSource returns the source that won for this option.
func OptionEffectiveSource(def OptionDef, sources model.OptionSources) model.OptionSource {
	return optSource(sources, def.SourceField)
}
