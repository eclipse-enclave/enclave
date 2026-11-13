// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxJSONDepth = 256

// DecodeJSON decodes a JSON document into map[string]any, []any, string,
// json.Number, bool, and nil. Unlike encoding/json it rejects duplicate
// object keys, since a server may honor a different occurrence than the
// filter checked, and trailing data after the document.
func DecodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeJSONValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid JSON: trailing data after the document")
	}
	return value, nil
}

func decodeJSONValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, fmt.Errorf("invalid JSON: nested deeper than %d levels", maxJSONDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("invalid JSON: empty document")
		}
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("invalid JSON: %w", err)
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("invalid JSON: object key is not a string")
				}
				if _, dup := obj[key]; dup {
					return nil, fmt.Errorf("invalid JSON: duplicate key %q", key)
				}
				value, err := decodeJSONValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				obj[key] = value
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("invalid JSON: %w", err)
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				value, err := decodeJSONValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, value)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("invalid JSON: %w", err)
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("invalid JSON: unexpected %q", v)
		}
	default:
		return v, nil
	}
}

// JSONPath is a parsed path of the supported subset: "$" followed by ".name",
// "..name", "[n]", and "[*]" steps.
type JSONPath struct {
	raw   string
	steps []jsonStep
}

type jsonStep struct {
	name string
	// fold is name folded by FoldParam. An object key that folds alike but
	// differs from name makes the document ambiguous.
	fold string
	// descend selects name in the current value and in every value below
	// it, the recursive descent "..name".
	descend  bool
	index    int
	wildcard bool
	isIndex  bool
}

var (
	jsonNamePattern     = regexp.MustCompile(`^[A-Za-z0-9_-]+`)
	jsonFullNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// ValidJSONName reports whether name is a JSON path member name: letters,
// digits, "_", and "-".
func ValidJSONName(name string) bool { return jsonFullNamePattern.MatchString(name) }

const maxJSONPathLength = 512

// ParseJSONPath parses a path such as "$.messages[*].id".
func ParseJSONPath(path string) (JSONPath, error) {
	if len(path) > maxJSONPathLength {
		return JSONPath{}, fmt.Errorf("JSON path is longer than %d characters", maxJSONPathLength)
	}
	if path == "" || path[0] != '$' {
		return JSONPath{}, fmt.Errorf("JSON path %q must start with \"$\"", path)
	}
	p := JSONPath{raw: path}
	rest := path[1:]
	for rest != "" {
		switch rest[0] {
		case '.':
			prefix := "."
			if strings.HasPrefix(rest, "..") {
				prefix = ".."
			}
			name := jsonNamePattern.FindString(rest[len(prefix):])
			if name == "" {
				return JSONPath{}, fmt.Errorf("JSON path %q: expected a name after %q", path, prefix)
			}
			p.steps = append(p.steps, jsonStep{name: name, fold: FoldParam(name), descend: prefix == ".."})
			rest = rest[len(prefix)+len(name):]
		case '[':
			end := bytes.IndexByte([]byte(rest), ']')
			if end < 0 {
				return JSONPath{}, fmt.Errorf("JSON path %q: unterminated \"[\"", path)
			}
			inner := rest[1:end]
			if inner == "*" {
				p.steps = append(p.steps, jsonStep{wildcard: true})
			} else {
				n, err := strconv.Atoi(inner)
				if err != nil || n < 0 || strconv.Itoa(n) != inner {
					return JSONPath{}, fmt.Errorf("JSON path %q: %q is not \"*\" or a non-negative index", path, inner)
				}
				p.steps = append(p.steps, jsonStep{index: n, isIndex: true})
			}
			rest = rest[end+1:]
		default:
			return JSONPath{}, fmt.Errorf("JSON path %q: unexpected %q", path, rest[:1])
		}
	}
	return p, nil
}

// String returns the path source.
func (p JSONPath) String() string { return p.raw }

// Select returns the values the path reaches in doc. Absent members, indexes
// out of range, and null values select nothing. A name step on a value that
// is neither an object nor null, or an index step on a value that is neither
// an array nor null, is an error: the document does not have the shape the
// path expects. Name steps match keys exactly. A recursive descent "..name"
// selects the members called name of the value and of every object below
// it, and never fails on the shape.
//
// An object a name step reads, or that a recursive descent passes, must not
// have a key that folds like the name (FoldParam) but differs from it, such
// as "Trashed" for "trashed". A server may read that key as the name, so
// Select returns an *AmbiguousKeyError.
func (p JSONPath) Select(doc any) ([]any, error) {
	nodes, err := p.selectNodes(doc)
	if err != nil {
		return nil, err
	}
	selected := nodes[:0]
	for _, node := range nodes {
		if node != nil {
			selected = append(selected, node)
		}
	}
	return selected, nil
}

// SelectPresent is Select that keeps null values: it returns every value the
// path reaches, null included.
func (p JSONPath) SelectPresent(doc any) ([]any, error) {
	return p.selectNodes(doc)
}

func (p JSONPath) selectNodes(doc any) ([]any, error) {
	current := []any{doc}
	for i, step := range p.steps {
		var next []any
		for _, node := range current {
			// A null reached by the last step is a value. One reached
			// earlier has nothing below it.
			if node == nil && i > 0 {
				continue
			}
			switch {
			case step.descend:
				var err error
				if next, err = descend(node, step, next); err != nil {
					return nil, err
				}
			case step.name != "":
				if node == nil {
					continue
				}
				obj, ok := node.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s: %q applied to a non-object", p.raw, step.name)
				}
				if err := checkKeySpelling(obj, step); err != nil {
					return nil, err
				}
				if value, ok := obj[step.name]; ok {
					next = append(next, value)
				}
			default:
				if node == nil {
					continue
				}
				arr, ok := node.([]any)
				if !ok {
					return nil, fmt.Errorf("%s: index applied to a non-array", p.raw)
				}
				if step.wildcard {
					next = append(next, arr...)
				} else if step.index < len(arr) {
					next = append(next, arr[step.index])
				}
			}
		}
		current = next
	}
	if len(p.steps) == 0 && doc == nil {
		return nil, nil
	}
	return current, nil
}

// descend appends the members called step.name of node and of every object
// below it, in document order with object keys sorted. It fails on an object
// with a key spelled like step.name, see checkKeySpelling.
func descend(node any, step jsonStep, out []any) ([]any, error) {
	var err error
	switch v := node.(type) {
	case map[string]any:
		if err := checkKeySpelling(v, step); err != nil {
			return nil, err
		}
		if value, ok := v[step.name]; ok {
			out = append(out, value)
		}
		for _, key := range SortedKeys(v) {
			if out, err = descend(v[key], step, out); err != nil {
				return nil, err
			}
		}
	case []any:
		for _, item := range v {
			if out, err = descend(item, step, out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// AmbiguousKeyError reports an object key that folds like a name a JSON path
// reads but is spelled differently.
type AmbiguousKeyError struct {
	Key  string
	Name string
}

const maxErrorKeyLength = 120

func (e *AmbiguousKeyError) Error() string {
	key := e.Key
	if len(key) > maxErrorKeyLength {
		key = key[:maxErrorKeyLength] + "..."
	}
	return fmt.Sprintf("JSON key %s is ambiguous with %q", strconv.Quote(key), e.Name)
}

// checkKeySpelling fails when obj has a key that folds like step.name without
// being step.name. Of several such keys it names the smallest.
func checkKeySpelling(obj map[string]any, step jsonStep) error {
	conflict := ""
	for key := range obj {
		if key != step.name && foldsTo(key, step.fold) && (conflict == "" || key < conflict) {
			conflict = key
		}
	}
	if conflict != "" {
		return &AmbiguousKeyError{Key: conflict, Name: step.name}
	}
	return nil
}

// foldsTo reports whether FoldParam(s) equals fold, without allocating.
func foldsTo(s, fold string) bool {
	i := 0
	for _, r := range s {
		if r == '_' || unicode.IsSpace(r) || unicode.IsControl(r) {
			continue
		}
		f, size := utf8.DecodeRuneInString(fold[i:])
		if size == 0 || foldRune(r) != f {
			return false
		}
		i += size
	}
	return i == len(fold)
}

// SelectStrings is Select for binds: strings as they are, numbers and
// booleans in their JSON spelling. An object or array is an error.
func (p JSONPath) SelectStrings(doc any) ([]string, error) {
	nodes, err := p.Select(doc)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(nodes))
	for _, node := range nodes {
		switch v := node.(type) {
		case string:
			values = append(values, v)
		case json.Number:
			values = append(values, v.String())
		case bool:
			values = append(values, strconv.FormatBool(v))
		default:
			return nil, fmt.Errorf("%s selects an object or array, not a value", p.raw)
		}
	}
	return values, nil
}

// JSONScalarEqual compares two decoded JSON scalars as JSON values: numbers by
// numeric value, strings and booleans exactly.
func JSONScalarEqual(a, b any) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case json.Number:
		bv, ok := b.(json.Number)
		if !ok {
			return false
		}
		x, okA := new(big.Float).SetString(av.String())
		y, okB := new(big.Float).SetString(bv.String())
		return okA && okB && x.Cmp(y) == 0
	default:
		return false
	}
}

// WithProtobufNames returns the path with each name step renamed to its
// protobuf JSON name, for use on documents read with JSONKeysProtobuf.
func (p JSONPath) WithProtobufNames() JSONPath {
	out := JSONPath{raw: p.raw, steps: make([]jsonStep, len(p.steps))}
	for i, step := range p.steps {
		if step.name != "" {
			step.name = ProtobufJSONName(step.name)
			step.fold = FoldParam(step.name)
		}
		out.steps[i] = step
	}
	return out
}

// ProtobufJSONName maps a field name to its protobuf JSON name, the way
// protoc derives json_name: each "_" is dropped and the next letter is
// uppercased. lowerCamelCase names are returned unchanged.
func ProtobufJSONName(name string) string {
	var b strings.Builder
	upper := false
	for _, r := range name {
		if r == '_' {
			upper = true
			continue
		}
		if upper {
			r = unicode.ToUpper(r)
			upper = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DecodeJSONKeys is DecodeJSON followed, when keys is JSONKeysProtobuf, by
// renaming every object key to its protobuf JSON name. Two keys of one object
// that rename to the same name make the document invalid, since a protobuf
// parser would read only one of them.
func DecodeJSONKeys(data []byte, keys string) (any, error) {
	doc, err := DecodeJSON(data)
	if err != nil || keys != JSONKeysProtobuf {
		return doc, err
	}
	return normalizeProtobufKeys(doc)
}

func normalizeProtobufKeys(node any) (any, error) {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		origin := make(map[string]string, len(v))
		for _, key := range SortedKeys(v) {
			name := ProtobufJSONName(key)
			if prev, dup := origin[name]; dup {
				return nil, fmt.Errorf("invalid JSON: keys %q and %q both mean %q", prev, key, name)
			}
			value, err := normalizeProtobufKeys(v[key])
			if err != nil {
				return nil, err
			}
			origin[name] = key
			out[name] = value
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			value, err := normalizeProtobufKeys(item)
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	default:
		return node, nil
	}
}
