// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"enclave/internal/domainpattern"
)

const (
	maxReasonLength = 256
	maxMatchLength  = 1024
)

// paramNameRule describes the query parameter names ValidQueryParamName
// accepts.
const paramNameRule = "letters, digits, \".\", \"_\", \"~\", and \"-\""

// CompileMatch compiles the RE2 expression of a queryMatch or jsonMatch
// entry. It matches anywhere in a value unless it anchors itself.
func CompileMatch(expr string) (*regexp.Regexp, error) {
	if len(expr) > maxMatchLength {
		return nil, fmt.Errorf("regular expression is longer than %d characters", maxMatchLength)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression: %w", err)
	}
	return re, nil
}

// RouteContext is what route validation checks references against.
type RouteContext struct {
	Hosts        []string
	Capabilities map[string]CapabilityDef
	Counters     map[string]bool
}

// CapabilityDef is the part of a capability that routes depend on.
type CapabilityDef struct {
	Public bool
	// Disabled marks a capability a compiled policy does not grant. Its
	// constraints are dropped at compile time, so constraint references
	// cannot be checked here. The spec loader checks them against the spec.
	Disabled bool
	// ConstraintVars maps constraint names to the variable each checks.
	ConstraintVars map[string]string
}

// RouteLabel names a route in messages.
func RouteLabel(i int, op string) string {
	if op == "" {
		return fmt.Sprintf("routes[%d]", i)
	}
	return fmt.Sprintf("routes[%d] (%s)", i, op)
}

// ValidateRoutes checks every route and the consistency between them.
func ValidateRoutes(routes []Route, ctx RouteContext) error {
	if len(routes) == 0 {
		return fmt.Errorf("routes must not be empty")
	}
	ops := map[string]int{}
	keyed := map[string]bool{}
	keyedSeen := map[string]bool{}
	for i, r := range routes {
		label := RouteLabel(i, r.Op)
		if err := ValidateRoute(r, ctx); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if prev, dup := ops[r.Op]; dup {
			return fmt.Errorf("%s: op %q is already used by routes[%d]", label, r.Op, prev)
		}
		ops[r.Op] = i
		for j, c := range r.Count {
			isKeyed := c.Key != ""
			if keyedSeen[c.Counter] && keyed[c.Counter] != isKeyed {
				return fmt.Errorf("%s: count[%d]: counter %q is counted both with and without a key", label, j, c.Counter)
			}
			keyedSeen[c.Counter] = true
			keyed[c.Counter] = isKeyed
		}
	}
	return nil
}

// ValidateRoute checks one route.
func ValidateRoute(r Route, ctx RouteContext) error {
	if err := ValidateIdent(r.Op); err != nil {
		return fmt.Errorf("op: %w", err)
	}
	var capDef CapabilityDef
	switch r.Action {
	case "", ActionAllow:
		if r.Reason != "" {
			return fmt.Errorf("reason is only allowed with action %s", ActionDeny)
		}
		def, ok := ctx.Capabilities[r.Capability]
		if !ok {
			return fmt.Errorf("capability %q is not declared in capabilities", r.Capability)
		}
		capDef = def
	case ActionDeny:
		if r.Capability != "" {
			return fmt.Errorf("a route with action %s has no capability", ActionDeny)
		}
		if len(r.Bind) > 0 || len(r.Count) > 0 {
			return fmt.Errorf("a route with action %s has no bind or count", ActionDeny)
		}
		if len(r.Reason) > maxReasonLength || hasControl(r.Reason) {
			return fmt.Errorf("reason must be at most %d printable characters", maxReasonLength)
		}
	default:
		return fmt.Errorf("action must be %s or %s (got %q)", ActionAllow, ActionDeny, r.Action)
	}
	if r.MultipartParts < 0 || r.MultipartParts > MaxMultipartParts {
		return fmt.Errorf("multipartParts must be between 1 and %d", MaxMultipartParts)
	}
	if err := validateMethods(r.Methods); err != nil {
		return fmt.Errorf("method: %w", err)
	}
	if err := validateRouteHosts(r.Hosts, ctx.Hosts); err != nil {
		return fmt.Errorf("host: %w", err)
	}
	tpl, err := ParsePathTemplate(r.Path)
	if err != nil {
		return fmt.Errorf("path: %w", err)
	}
	captures := map[string]bool{}
	for _, name := range tpl.Captures() {
		captures[name] = true
	}
	for _, name := range SortedKeys(r.Query) {
		if !ValidQueryParamName(name) {
			return fmt.Errorf("query: parameter name %q must use only %s", name, paramNameRule)
		}
		if err := r.Query[name].validate(); err != nil {
			return fmt.Errorf("query.%s: %w", name, err)
		}
	}
	for _, name := range SortedKeys(r.QueryMatch) {
		if !ValidQueryParamName(name) {
			return fmt.Errorf("queryMatch: parameter name %q must use only %s", name, paramNameRule)
		}
		if _, err := CompileMatch(r.QueryMatch[name]); err != nil {
			return fmt.Errorf("queryMatch.%s: %w", name, err)
		}
	}
	for _, path := range SortedKeys(r.JSON) {
		if _, err := ParseJSONPath(path); err != nil {
			return fmt.Errorf("json: %w", err)
		}
		if err := r.JSON[path].validate(); err != nil {
			return fmt.Errorf("json.%s: %w", path, err)
		}
	}
	for i, path := range r.JSONPresent {
		if _, err := ParseJSONPath(path); err != nil {
			return fmt.Errorf("jsonPresent[%d]: %w", i, err)
		}
	}
	for _, path := range SortedKeys(r.JSONMatch) {
		if _, err := ParseJSONPath(path); err != nil {
			return fmt.Errorf("jsonMatch: %w", err)
		}
		if _, err := CompileMatch(r.JSONMatch[path]); err != nil {
			return fmt.Errorf("jsonMatch.%s: %w", path, err)
		}
	}
	for _, path := range SortedKeys(r.JSONAllowedKeys) {
		if _, err := ParseJSONPath(path); err != nil {
			return fmt.Errorf("jsonAllowedKeys: %w", err)
		}
		seen := map[string]bool{}
		for _, key := range r.JSONAllowedKeys[path] {
			if !ValidJSONName(key) {
				return fmt.Errorf("jsonAllowedKeys.%s: key %q must use only letters, digits, \"_\", and \"-\"", path, key)
			}
			if seen[key] {
				return fmt.Errorf("jsonAllowedKeys.%s: duplicate key %q", path, key)
			}
			seen[key] = true
		}
	}
	requestVars := map[string]bool{}
	responseVars := map[string]bool{}
	for name := range captures {
		requestVars[name] = true
	}
	for _, name := range SortedKeys(r.Bind) {
		if err := ValidateVarName(name); err != nil {
			return fmt.Errorf("bind: %w", err)
		}
		steps := r.Bind[name]
		if len(steps) == 0 {
			return fmt.Errorf("bind.%s: must have at least one step", name)
		}
		if err := ValidateBind(steps, captures); err != nil {
			return fmt.Errorf("bind.%s%w", name, err)
		}
		if IsResponseBind(steps) {
			responseVars[name] = true
			delete(requestVars, name)
		} else {
			requestVars[name] = true
		}
	}
	if capDef.Public && len(r.Count) > 0 {
		return fmt.Errorf("count is not allowed on a route of public capability %q", r.Capability)
	}
	for j, c := range r.Count {
		if !ctx.Counters[c.Counter] {
			return fmt.Errorf("count[%d]: counter %q is not declared in counters", j, c.Counter)
		}
		if c.Key != "" && !requestVars[c.Key] && !responseVars[c.Key] {
			return fmt.Errorf("count[%d]: key %q is neither a path capture nor a bind of this route", j, c.Key)
		}
	}
	for _, cname := range SortedKeys(capDef.ConstraintVars) {
		if v := capDef.ConstraintVars[cname]; responseVars[v] {
			return fmt.Errorf("bind.%s reads the response, so constraint %s of capability %s cannot check it", v, cname, r.Capability)
		}
	}
	if err := validateWildcardOnly(r, capDef, requestVars); err != nil {
		return fmt.Errorf("wildcardOnly: %w", err)
	}
	if r.MaxBodyBytes < 1 || r.MaxBodyBytes > MaxInspectBytes {
		return fmt.Errorf("maxBodyBytes must be between 1 and %d", MaxInspectBytes)
	}
	if r.MaxResponseBytes < 1 || r.MaxResponseBytes > MaxInspectBytes {
		return fmt.Errorf("maxResponseBytes must be between 1 and %d", MaxInspectBytes)
	}
	return nil
}

// validateWildcardOnly checks that wildcardOnly names constraints of the
// route's capability whose variable the route does not bind.
func validateWildcardOnly(r Route, capDef CapabilityDef, requestVars map[string]bool) error {
	if len(r.WildcardOnly) == 0 {
		return nil
	}
	if capDef.Disabled {
		return nil
	}
	if r.Action == ActionDeny {
		return fmt.Errorf("a route with action %s checks no constraints", ActionDeny)
	}
	seen := map[string]bool{}
	for _, cname := range r.WildcardOnly {
		v, ok := capDef.ConstraintVars[cname]
		switch {
		case !ok:
			return fmt.Errorf("%q is not a constraint of capability %s", cname, r.Capability)
		case seen[cname]:
			return fmt.Errorf("duplicate constraint %q", cname)
		case requestVars[v]:
			return fmt.Errorf("the route binds %q, so constraint %s is checked, remove it", v, cname)
		}
		seen[cname] = true
	}
	return nil
}

// RouteWarnings reports constraint variables a route does not bind and does
// not list in wildcardOnly. Such a route only passes the constraint when the
// preset sets "*".
func RouteWarnings(routes []Route, ctx RouteContext) []string {
	var warnings []string
	for i, r := range routes {
		capDef, ok := ctx.Capabilities[r.Capability]
		if !ok {
			continue
		}
		bound := map[string]bool{}
		if tpl, err := ParsePathTemplate(r.Path); err == nil {
			for _, name := range tpl.Captures() {
				bound[name] = true
			}
		}
		for name, steps := range r.Bind {
			if !IsResponseBind(steps) {
				bound[name] = true
			}
		}
		for _, cname := range SortedKeys(capDef.ConstraintVars) {
			if v := capDef.ConstraintVars[cname]; !bound[v] && !slices.Contains(r.WildcardOnly, cname) {
				warnings = append(warnings, fmt.Sprintf("%s: does not bind %q, so constraint %s of capability %s only passes with \"*\" (list it in wildcardOnly if intended)",
					RouteLabel(i, r.Op), v, cname, r.Capability))
			}
		}
	}
	return warnings
}

func validateMethods(methods []string) error {
	if len(methods) == 0 {
		return fmt.Errorf("at least one method is required")
	}
	seen := map[string]bool{}
	for _, m := range methods {
		if !ValidMethod(m) {
			return fmt.Errorf("unsupported method %q (want GET, HEAD, POST, PUT, PATCH, DELETE, or OPTIONS)", m)
		}
		if seen[m] {
			return fmt.Errorf("duplicate method %q", m)
		}
		seen[m] = true
	}
	return nil
}

func validateRouteHosts(hosts []string, governed []string) error {
	if len(hosts) == 0 {
		return fmt.Errorf("at least one host is required")
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		normalized, err := domainpattern.Normalize(h)
		if err != nil {
			return fmt.Errorf("invalid host %q: %w", h, err)
		}
		if normalized == "" || normalized != h {
			return fmt.Errorf("host %q is not normalized (want %q)", h, normalized)
		}
		if !patternCovered(h, governed) {
			return fmt.Errorf("host %q is not a governed host", h)
		}
		if seen[h] {
			return fmt.Errorf("duplicate host %q", h)
		}
		seen[h] = true
	}
	return nil
}

func (q QueryCondition) validate() error {
	if q.Any {
		if q.Absent || len(q.Values) > 0 {
			return fmt.Errorf("\"*\" cannot be combined with other values")
		}
		return nil
	}
	if !q.Absent && len(q.Values) == 0 {
		return fmt.Errorf("must allow at least one value or absence")
	}
	for _, v := range q.Values {
		if hasControl(v) {
			return fmt.Errorf("value %q contains control characters", v)
		}
	}
	return nil
}

func (c JSONCondition) validate() error {
	if !c.Absent && len(c.Values) == 0 {
		return fmt.Errorf("must allow at least one value or absence")
	}
	for _, raw := range c.Values {
		v, err := DecodeJSON(raw)
		if err != nil {
			return err
		}
		switch v.(type) {
		case string, bool, json.Number:
		case nil:
			return fmt.Errorf("null must be expressed as absent")
		default:
			return fmt.Errorf("value %s is not a JSON scalar", string(raw))
		}
	}
	return nil
}

// AllowsValue reports whether value satisfies the query condition.
func (q QueryCondition) AllowsValue(value string) bool {
	if q.Any {
		return true
	}
	for _, v := range q.Values {
		if v == value {
			return true
		}
	}
	return false
}

// AllowsJSON reports whether a decoded JSON value satisfies the condition.
func (c JSONCondition) AllowsJSON(value any) bool {
	for _, raw := range c.Values {
		allowed, err := DecodeJSON(raw)
		if err != nil {
			return false
		}
		if JSONScalarEqual(allowed, value) {
			return true
		}
	}
	return false
}
