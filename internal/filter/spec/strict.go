// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"sigs.k8s.io/yaml"

	"enclave/internal/filter"
)

var rawMessageType = reflect.TypeFor[json.RawMessage]()

// decodeYAML decodes a spec or preset document into out. sigs.k8s.io/yaml
// decodes through encoding/json, which matches field names
// case-insensitively, so every object key is first checked against the exact
// json tag of its target field.
func decodeYAML(data []byte, out any) error {
	jsonData, err := yaml.YAMLToJSONStrict(data)
	if err != nil {
		return fmt.Errorf("error converting YAML to JSON: %w", err)
	}
	if err := checkExactKeys(jsonData, reflect.TypeOf(out)); err != nil {
		return err
	}
	return yaml.UnmarshalStrict(data, out)
}

// decodeJSONExact decodes a JSON value into out, rejecting unknown and
// differently cased field names.
func decodeJSONExact(data []byte, out any) error {
	if err := checkExactKeys(data, reflect.TypeOf(out)); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func checkExactKeys(jsonData []byte, t reflect.Type) error {
	var doc any
	if err := json.Unmarshal(jsonData, &doc); err != nil {
		return err
	}
	return checkKeys(doc, t, "")
}

// checkKeys walks a decoded JSON value along the Go type it decodes into.
// Struct keys must equal a json field name. Map keys are free, their values
// are checked against the element type. json.RawMessage values are checked by
// the code that decodes them. A type with its own UnmarshalJSON is walked as
// its Go type, which fits stringList.
func checkKeys(value any, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessageType {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		fields := jsonFields(t)
		for _, key := range filter.SortedKeys(obj) {
			field, ok := fields[key]
			if !ok {
				return unknownFieldError(path, key, filter.SortedKeys(fields))
			}
			if err := checkKeys(obj[key], field, joinKeyPath(path, key)); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for _, key := range filter.SortedKeys(obj) {
			if err := checkKeys(obj[key], t.Elem(), joinKeyPath(path, key)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		list, ok := value.([]any)
		if !ok {
			return nil
		}
		for i, item := range list {
			if err := checkKeys(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonFields maps the json names of a struct's fields to their types,
// flattening untagged embedded structs as encoding/json does.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := range t.NumField() {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		embedded := field.Type
		if embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		if field.Anonymous && name == "" && embedded.Kind() == reflect.Struct {
			for inner, typ := range jsonFields(embedded) {
				if _, shadowed := fields[inner]; !shadowed {
					fields[inner] = typ
				}
			}
			continue
		}
		if !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

func unknownFieldError(path string, key string, known []string) error {
	err := fmt.Errorf("unknown field %q%s", key, didYouMean(key, known))
	if path == "" {
		return err
	}
	return fmt.Errorf("%s: %w", path, err)
}

// didYouMean suggests the name in known that differs from key only in case.
func didYouMean(key string, known []string) string {
	for _, name := range known {
		if strings.EqualFold(name, key) {
			return fmt.Sprintf(" (did you mean %q?)", name)
		}
	}
	return ""
}

func joinKeyPath(path string, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
