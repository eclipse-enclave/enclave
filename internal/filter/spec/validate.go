// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package spec

import (
	"encoding/json"
	"fmt"
	"strings"

	"enclave/internal/domainpattern"
	"enclave/internal/filter"
	"enclave/internal/model"
	"enclave/internal/secretfile"
)

// reservedAuthParams are set by the login flow and cannot be overridden.
// response_mode stays the default: the login callback reads the code from
// the redirect query only.
var reservedAuthParams = map[string]bool{
	"client_id": true, "redirect_uri": true, "response_type": true, "scope": true,
	"state": true, "code_challenge": true, "code_challenge_method": true, "response_mode": true,
}

func buildFilter(doc specDocument) (*Filter, error) {
	if doc.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("schemaVersion must be %q (got %q)", SchemaVersion, doc.SchemaVersion)
	}
	if doc.Kind != Kind {
		return nil, fmt.Errorf("kind must be %q (got %q)", Kind, doc.Kind)
	}
	if err := filter.ValidateName(doc.Name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	f := &Filter{
		Name:          doc.Name,
		DisplayName:   doc.DisplayName,
		Description:   doc.Description,
		DefaultPreset: doc.DefaultPreset,
	}
	if doc.DefaultPreset != "" {
		if err := filter.ValidateName(doc.DefaultPreset); err != nil {
			return nil, fmt.Errorf("defaultPreset: %w", err)
		}
	}
	if doc.JSONKeys != "" && doc.JSONKeys != filter.JSONKeysProtobuf {
		return nil, fmt.Errorf("jsonKeys: must be %q or unset (got %q)", filter.JSONKeysProtobuf, doc.JSONKeys)
	}
	f.JSONKeys = doc.JSONKeys
	hosts, err := normalizeHosts(doc.Hosts)
	if err != nil {
		return nil, fmt.Errorf("hosts: %w", err)
	}
	f.Hosts = hosts
	if f.Confine, err = normalizeConfine(doc.Confine, f.Hosts); err != nil {
		return nil, fmt.Errorf("confine: %w", err)
	}

	if doc.Credential == nil {
		return nil, fmt.Errorf("credential is required")
	}
	if f.Credential, err = buildCredential(*doc.Credential); err != nil {
		return nil, fmt.Errorf("credential: %w", err)
	}
	oauth := f.Credential.OAuth2

	f.Resolvers = map[string]Resolver{}
	for _, name := range filter.SortedKeys(doc.Resolvers) {
		r, err := buildResolver(name, doc.Resolvers[name], f.Hosts, oauth)
		if err != nil {
			return nil, fmt.Errorf("resolvers.%s: %w", name, err)
		}
		f.Resolvers[name] = r
	}

	f.Counters = map[string]filter.Counter{}
	for _, name := range filter.SortedKeys(doc.Counters) {
		if err := filter.ValidateName(name); err != nil {
			return nil, fmt.Errorf("counters: %w", err)
		}
		c := doc.Counters[name]
		f.Counters[name] = filter.Counter{Description: c.Description, Unit: c.Unit}
	}

	if len(doc.Capabilities) == 0 {
		return nil, fmt.Errorf("capabilities must not be empty")
	}
	f.Capabilities = map[string]Capability{}
	for _, name := range filter.SortedKeys(doc.Capabilities) {
		c, err := f.buildCapability(name, doc.Capabilities[name])
		if err != nil {
			return nil, fmt.Errorf("capabilities.%s: %w", name, err)
		}
		f.Capabilities[name] = c
	}

	for i, r := range doc.Routes {
		route, err := buildRoute(r)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filter.RouteLabel(i, r.Op), err)
		}
		f.Routes = append(f.Routes, route)
	}
	ctx := f.routeContext()
	if err := filter.ValidateRoutes(f.Routes, ctx); err != nil {
		return nil, err
	}
	f.Warnings = append(f.Warnings, filter.RouteWarnings(f.Routes, ctx)...)

	if doc.Deny != nil {
		f.Deny = &filter.DenyTemplate{ContentType: doc.Deny.ContentType, Body: doc.Deny.Body}
		if err := f.Deny.Validate(); err != nil {
			return nil, fmt.Errorf("deny: %w", err)
		}
	}
	return f, nil
}

func (f *Filter) routeContext() filter.RouteContext {
	ctx := filter.RouteContext{
		Hosts:        f.Hosts,
		Capabilities: map[string]filter.CapabilityDef{},
		Counters:     map[string]bool{},
	}
	for name, c := range f.Capabilities {
		def := filter.CapabilityDef{Public: c.Public, ConstraintVars: map[string]string{}}
		for cname, con := range c.Constraints {
			def.ConstraintVars[cname] = con.Var
		}
		ctx.Capabilities[name] = def
	}
	for name := range f.Counters {
		ctx.Counters[name] = true
	}
	return ctx
}

func normalizeHosts(hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return nil, fmt.Errorf("must list at least one host")
	}
	var out []string
	seen := map[string]bool{}
	for _, h := range hosts {
		normalized, err := domainpattern.Normalize(h)
		if err != nil {
			return nil, fmt.Errorf("invalid host %q: %w", h, err)
		}
		if normalized == "" {
			return nil, fmt.Errorf("empty host")
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeConfine(confine []string, hosts []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, d := range confine {
		normalized, err := domainpattern.Normalize(d)
		if err != nil {
			return nil, fmt.Errorf("invalid domain %q: %w", d, err)
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	if err := filter.ValidateConfine(out, hosts); err != nil {
		return nil, err
	}
	return out, nil
}

func buildCredential(doc specCredential) (Credential, error) {
	var c Credential
	if (doc.OAuth2 == nil) == (doc.Static == nil) {
		return c, fmt.Errorf("exactly one of oauth2 and static is required")
	}
	if doc.OAuth2 != nil {
		o, err := buildOAuth2(*doc.OAuth2)
		if err != nil {
			return c, fmt.Errorf("oauth2: %w", err)
		}
		c.Type = filter.CredentialOAuth2
		c.OAuth2 = o
	} else {
		s, err := buildStatic(*doc.Static)
		if err != nil {
			return c, fmt.Errorf("static: %w", err)
		}
		c.Type = filter.CredentialStatic
		c.Static = s
	}
	if doc.Inject == nil {
		return c, fmt.Errorf("inject is required")
	}
	c.Inject = filter.Inject{Header: strings.ToLower(strings.TrimSpace(doc.Inject.Header)), Format: doc.Inject.Format}
	if err := c.Inject.Validate(); err != nil {
		return c, fmt.Errorf("inject: %w", err)
	}
	if len(doc.Env) == 0 {
		return c, fmt.Errorf("env: must list at least one env var")
	}
	seen := map[string]bool{}
	for _, name := range doc.Env {
		if err := validateEnvName(name); err != nil {
			return c, fmt.Errorf("env: %w", err)
		}
		if seen[name] {
			return c, fmt.Errorf("env: duplicate env var %q", name)
		}
		seen[name] = true
		c.Env = append(c.Env, name)
	}
	if doc.Placeholder != nil {
		if _, err := doc.Placeholder.ValidatedAlphabet(); err != nil {
			return c, fmt.Errorf("placeholder: %w", err)
		}
		shape := *doc.Placeholder
		c.Placeholder = &shape
	}
	for _, h := range doc.DenyHeaders {
		c.DenyHeaders = append(c.DenyHeaders, strings.ToLower(strings.TrimSpace(h)))
	}
	if err := filter.ValidateDenyHeaders(c.DenyHeaders, c.Inject.Header); err != nil {
		return c, fmt.Errorf("denyHeaders: %w", err)
	}
	c.DenyParams = append(c.DenyParams, doc.DenyParams...)
	if err := filter.ValidateDenyParams(c.DenyParams); err != nil {
		return c, fmt.Errorf("denyParams: %w", err)
	}
	return c, nil
}

// validateEnvName checks a credential.env or credential.static.env name.
// Enclave owns the ENCLAVE_ prefix on the host and in the container.
func validateEnvName(name string) error {
	if err := filter.ValidateEnvVarName(name); err != nil {
		return err
	}
	if strings.HasPrefix(name, model.EnvPrefix) {
		return fmt.Errorf("%s uses the reserved %s prefix", name, model.EnvPrefix)
	}
	return nil
}

func buildOAuth2(doc specOAuth2) (*OAuth2, error) {
	o := &OAuth2{
		AuthURL:    doc.AuthURL,
		TokenURL:   doc.TokenURL,
		RevokeURL:  doc.RevokeURL,
		ClientHelp: doc.ClientHelp,
		AuthParams: map[string]string{},
	}
	if err := filter.ValidateHTTPSURL(doc.AuthURL); err != nil {
		return nil, fmt.Errorf("authUrl: %w", err)
	}
	if err := filter.ValidateHTTPSURL(doc.TokenURL); err != nil {
		return nil, fmt.Errorf("tokenUrl: %w", err)
	}
	if doc.RevokeURL != "" {
		if err := filter.ValidateHTTPSURL(doc.RevokeURL); err != nil {
			return nil, fmt.Errorf("revokeUrl: %w", err)
		}
	}
	for _, key := range filter.SortedKeys(doc.AuthParams) {
		value := doc.AuthParams[key]
		if key == "" || hasControlOrSpace(key) || hasControlOrSpace(value) {
			return nil, fmt.Errorf("authParams.%s: names and values must be printable without spaces", key)
		}
		if reservedAuthParams[key] {
			return nil, fmt.Errorf("authParams.%s: is set by enclave filters login", key)
		}
		o.AuthParams[key] = value
	}
	if doc.ScopePrefix != "" {
		if err := filter.ValidateHTTPSURL(doc.ScopePrefix); err != nil {
			return nil, fmt.Errorf("scopePrefix: %w", err)
		}
		o.ScopePrefix = doc.ScopePrefix
	}
	o.ScopeImplies = map[string][]string{}
	for _, key := range filter.SortedKeys(doc.ScopeImplies) {
		if err := validateScope(key); err != nil {
			return nil, fmt.Errorf("scopeImplies: %w", err)
		}
		full := fullScope(o.ScopePrefix, key)
		for _, implied := range doc.ScopeImplies[key] {
			if err := validateScope(implied); err != nil {
				return nil, fmt.Errorf("scopeImplies.%s: %w", key, err)
			}
			impliedFull := fullScope(o.ScopePrefix, implied)
			if impliedFull == full {
				return nil, fmt.Errorf("scopeImplies.%s: a scope cannot imply itself", key)
			}
			o.ScopeImplies[full] = appendUnique(o.ScopeImplies[full], impliedFull)
		}
	}
	if cycle := impliesCycle(o.ScopeImplies); cycle != "" {
		return nil, fmt.Errorf("scopeImplies: cycle through %q", cycle)
	}
	return o, nil
}

func buildStatic(doc specStatic) (*Static, error) {
	s := &Static{}
	seen := map[string]bool{}
	for _, name := range doc.Env {
		if err := validateEnvName(name); err != nil {
			return nil, fmt.Errorf("env: %w", err)
		}
		if !seen[name] {
			seen[name] = true
			s.Env = append(s.Env, name)
		}
	}
	if doc.File != nil {
		if strings.TrimSpace(doc.File.Path) == "" {
			return nil, fmt.Errorf("file.path is required")
		}
		if err := secretfile.ValidateFileParser(doc.File.Parser); err != nil {
			return nil, fmt.Errorf("file.parser: %w", err)
		}
		file := *doc.File
		s.File = &file
	}
	if len(s.Env) == 0 && s.File == nil {
		return nil, fmt.Errorf("needs env or file")
	}
	switch doc.Priority {
	case "":
		s.Priority = model.SecretPriorityEnvFirst
	case model.SecretPriorityEnvFirst, model.SecretPriorityFileFirst:
		s.Priority = doc.Priority
	default:
		return nil, fmt.Errorf("priority: must be %q or %q (got %q)", model.SecretPriorityEnvFirst, model.SecretPriorityFileFirst, doc.Priority)
	}
	return s, nil
}

func buildResolver(name string, doc specResolver, hosts []string, oauth *OAuth2) (Resolver, error) {
	if err := filter.ValidateIdent(name); err != nil {
		return Resolver{}, err
	}
	r := Resolver{Resolver: filter.Resolver{URL: doc.URL, Extract: doc.Extract, Recursive: doc.Recursive, MaxDepth: doc.MaxDepth}}
	switch {
	case !doc.Recursive && doc.MaxDepth > 1:
		return Resolver{}, fmt.Errorf("maxDepth needs recursive: true")
	case !doc.Recursive:
		r.MaxDepth = 1
	case doc.MaxDepth == 0:
		r.MaxDepth = filter.DefaultResolverDepth
	}
	if err := r.Validate(hosts); err != nil {
		return Resolver{}, err
	}
	scopes, err := buildScopes(doc.Scopes, oauth)
	if err != nil {
		return Resolver{}, fmt.Errorf("scopes: %w", err)
	}
	r.Scopes = scopes
	return r, nil
}

func (f *Filter) buildCapability(name string, doc specCapability) (Capability, error) {
	if err := filter.ValidateName(name); err != nil {
		return Capability{}, err
	}
	c := Capability{Description: doc.Description, Public: doc.Public, Constraints: map[string]Constraint{}}
	if doc.Public && len(doc.Scopes) > 0 {
		return Capability{}, fmt.Errorf("scopes: a public capability never uses the credential")
	}
	if doc.Public && len(doc.Constraints) > 0 {
		return Capability{}, fmt.Errorf("constraints: a public capability cannot have constraints")
	}
	scopes, err := buildScopes(doc.Scopes, f.Credential.OAuth2)
	if err != nil {
		return Capability{}, fmt.Errorf("scopes: %w", err)
	}
	c.Scopes = scopes
	resolvers := map[string]filter.Resolver{}
	for rname, r := range f.Resolvers {
		resolvers[rname] = r.Resolver
	}
	for _, cname := range filter.SortedKeys(doc.Constraints) {
		if err := filter.ValidateIdent(cname); err != nil {
			return Capability{}, fmt.Errorf("constraints: %w", err)
		}
		d := doc.Constraints[cname]
		if err := filter.ValidateConstraintDef(d.Kind, d.Var, d.Resolver, resolvers); err != nil {
			return Capability{}, fmt.Errorf("constraints.%s.%w", cname, err)
		}
		con := Constraint{Kind: d.Kind, Var: d.Var, Resolver: d.Resolver}
		if len(d.Default) > 0 {
			value, err := parseConstraintValue(d.Default, d.Kind, d.Resolver != "")
			if err != nil {
				return Capability{}, fmt.Errorf("constraints.%s.default: %w", cname, err)
			}
			con.Default = &value
		}
		c.Constraints[cname] = con
	}
	return c, nil
}

func buildRoute(doc specRoute) (filter.Route, error) {
	r := filter.Route{
		Op:               doc.Op,
		Action:           doc.Action,
		Reason:           doc.Reason,
		Capability:       doc.Capability,
		Path:             doc.Path,
		AllowFormBody:    doc.AllowFormBody,
		MultipartParts:   doc.MultipartParts,
		MaxBodyBytes:     doc.MaxBodyBytes,
		MaxResponseBytes: doc.MaxResponseBytes,
	}
	for _, m := range doc.Method {
		r.Methods = append(r.Methods, strings.ToUpper(strings.TrimSpace(m)))
	}
	for _, h := range doc.Host {
		normalized, err := domainpattern.Normalize(h)
		if err != nil {
			return r, fmt.Errorf("host: invalid host %q: %w", h, err)
		}
		r.Hosts = append(r.Hosts, normalized)
	}
	if r.Action == filter.ActionAllow {
		r.Action = ""
	}
	if doc.MaxBodyBytes < 0 || doc.MaxResponseBytes < 0 {
		return r, fmt.Errorf("maxBodyBytes, maxResponseBytes: must not be negative")
	}
	if r.MaxBodyBytes == 0 {
		r.MaxBodyBytes = filter.DefaultMaxBodyBytes
	}
	if r.MaxResponseBytes == 0 {
		r.MaxResponseBytes = filter.DefaultMaxResponseBytes
	}
	for _, name := range filter.SortedKeys(doc.Query) {
		cond, err := parseQueryCondition(doc.Query[name])
		if err != nil {
			return r, fmt.Errorf("query.%s: %w", name, err)
		}
		if r.Query == nil {
			r.Query = map[string]filter.QueryCondition{}
		}
		r.Query[name] = cond
	}
	if doc.JSONPresent != nil && len(doc.JSONPresent) == 0 {
		return r, fmt.Errorf("jsonPresent: must list at least one path")
	}
	if doc.WildcardOnly != nil && len(doc.WildcardOnly) == 0 {
		return r, fmt.Errorf("wildcardOnly: must list at least one constraint")
	}
	r.JSONPresent = append(r.JSONPresent, doc.JSONPresent...)
	r.WildcardOnly = append(r.WildcardOnly, doc.WildcardOnly...)
	for _, name := range filter.SortedKeys(doc.QueryMatch) {
		if r.QueryMatch == nil {
			r.QueryMatch = map[string]string{}
		}
		r.QueryMatch[name] = doc.QueryMatch[name]
	}
	for _, path := range filter.SortedKeys(doc.JSONMatch) {
		if r.JSONMatch == nil {
			r.JSONMatch = map[string]string{}
		}
		r.JSONMatch[path] = doc.JSONMatch[path]
	}
	for _, path := range filter.SortedKeys(doc.JSONAllowedKeys) {
		if r.JSONAllowedKeys == nil {
			r.JSONAllowedKeys = map[string][]string{}
		}
		r.JSONAllowedKeys[path] = append([]string{}, doc.JSONAllowedKeys[path]...)
	}
	for _, path := range filter.SortedKeys(doc.JSON) {
		cond, err := parseJSONCondition(doc.JSON[path])
		if err != nil {
			return r, fmt.Errorf("json.%s: %w", path, err)
		}
		if r.JSON == nil {
			r.JSON = map[string]filter.JSONCondition{}
		}
		r.JSON[path] = cond
	}
	for _, name := range filter.SortedKeys(doc.Bind) {
		var steps []filter.BindStep
		for i, raw := range doc.Bind[name] {
			step, err := parseBindStep(raw)
			if err != nil {
				return r, fmt.Errorf("bind.%s[%d]: %w", name, i, err)
			}
			steps = append(steps, step)
		}
		if r.Bind == nil {
			r.Bind = map[string][]filter.BindStep{}
		}
		r.Bind[name] = steps
	}
	for _, c := range doc.Count {
		r.Count = append(r.Count, filter.Count(c))
	}
	return r, nil
}

// rawValues splits a scalar-or-list YAML value into its elements.
func rawValues(raw json.RawMessage) []json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err == nil {
			return list
		}
	}
	return []json.RawMessage{raw}
}

func parseQueryCondition(raw json.RawMessage) (filter.QueryCondition, error) {
	var cond filter.QueryCondition
	values := rawValues(raw)
	if len(values) == 0 {
		return cond, fmt.Errorf("must list allowed values, ~ for absent, or \"*\"")
	}
	for _, v := range values {
		decoded, err := filter.DecodeJSON(v)
		if err != nil {
			return cond, err
		}
		switch x := decoded.(type) {
		case nil:
			cond.Absent = true
		case string:
			if x == "*" {
				cond.Any = true
				continue
			}
			cond.Values = append(cond.Values, x)
		case json.Number:
			cond.Values = append(cond.Values, x.String())
		case bool:
			cond.Values = append(cond.Values, fmt.Sprint(x))
		default:
			return cond, fmt.Errorf("values must be strings, numbers, booleans, or ~")
		}
	}
	if cond.Any {
		return filter.QueryCondition{Any: true}, nil
	}
	return cond, nil
}

func parseJSONCondition(raw json.RawMessage) (filter.JSONCondition, error) {
	var cond filter.JSONCondition
	values := rawValues(raw)
	if len(values) == 0 {
		return cond, fmt.Errorf("must list allowed values or ~ for absent")
	}
	for _, v := range values {
		decoded, err := filter.DecodeJSON(v)
		if err != nil {
			return cond, err
		}
		switch decoded.(type) {
		case nil:
			cond.Absent = true
		case string, json.Number, bool:
			compact, err := json.Marshal(decoded)
			if err != nil {
				return cond, err
			}
			cond.Values = append(cond.Values, compact)
		default:
			return cond, fmt.Errorf("values must be JSON scalars or ~")
		}
	}
	return cond, nil
}

func parseBindStep(raw json.RawMessage) (filter.BindStep, error) {
	var step map[string]json.RawMessage
	if err := json.Unmarshal(raw, &step); err != nil || len(step) != 1 {
		return filter.BindStep{}, fmt.Errorf("a step is a map with exactly one key, such as {json: \"$.id\"}")
	}
	for op, arg := range step {
		if !filter.KnownStep(op) {
			return filter.BindStep{}, fmt.Errorf("unknown step %q", op)
		}
		if !filter.StepTakesArg(op) {
			var empty map[string]json.RawMessage
			trimmed := strings.TrimSpace(string(arg))
			if trimmed != "null" && (json.Unmarshal(arg, &empty) != nil || len(empty) != 0) {
				return filter.BindStep{}, fmt.Errorf("%s takes no argument, use {}", op)
			}
			return filter.BindStep{Op: op}, nil
		}
		if op == filter.StepMultipart && strings.HasPrefix(strings.TrimSpace(string(arg)), "{") {
			var selector struct {
				Index *int `json:"index"`
			}
			if err := decodeJSONExact(arg, &selector); err != nil {
				return filter.BindStep{}, fmt.Errorf("multipart takes a content type prefix or {index: n}: %w", err)
			}
			if selector.Index == nil {
				return filter.BindStep{}, fmt.Errorf("multipart takes a content type prefix or {index: n}")
			}
			return filter.BindStep{Op: op, Index: selector.Index}, nil
		}
		var s string
		if err := json.Unmarshal(arg, &s); err != nil {
			return filter.BindStep{}, fmt.Errorf("%s needs a string argument", op)
		}
		return filter.BindStep{Op: op, Arg: s}, nil
	}
	return filter.BindStep{}, fmt.Errorf("empty step")
}

// parseConstraintValue parses a preset or default constraint value: "*", a
// list of strings for email and value, or {ids, within} for resource.
func parseConstraintValue(raw json.RawMessage, kind string, hasResolver bool) (filter.ConstraintValue, error) {
	var v filter.ConstraintValue
	decoded, err := filter.DecodeJSON(raw)
	if err != nil {
		return v, err
	}
	switch x := decoded.(type) {
	case string:
		if x != "*" {
			return v, fmt.Errorf("the only string value is \"*\", %s", constraintShape(kind))
		}
		v.Any = true
	case []any:
		if kind == filter.KindResource {
			return v, fmt.Errorf("%s", constraintShape(kind))
		}
		strs, err := stringElements(x)
		if err != nil {
			return v, err
		}
		v.Values = strs
	case map[string]any:
		if kind != filter.KindResource {
			return v, fmt.Errorf("%s", constraintShape(kind))
		}
		for key, val := range x {
			list, ok := val.([]any)
			if !ok {
				return v, fmt.Errorf("%s must be a list of strings", key)
			}
			strs, err := stringElements(list)
			if err != nil {
				return v, fmt.Errorf("%s: %w", key, err)
			}
			switch key {
			case "ids":
				v.IDs = strs
			case "within":
				v.Within = strs
			default:
				return v, fmt.Errorf("unknown key %q, use ids and within", key)
			}
		}
	default:
		return v, fmt.Errorf("%s", constraintShape(kind))
	}
	if err := v.Validate(kind, hasResolver); err != nil {
		return v, err
	}
	return v, nil
}

func constraintShape(kind string) string {
	switch kind {
	case filter.KindEmail:
		return "use \"*\" or a list such as [\"user@example.com\", \"@example.com\", \"@*.example.com\"]"
	case filter.KindResource:
		return "use \"*\" or {ids: [...], within: [...]}"
	default:
		return "use \"*\" or a list of strings"
	}
}

func stringElements(list []any) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("list elements must be strings")
		}
		out = append(out, s)
	}
	return out, nil
}

func hasControlOrSpace(s string) bool {
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func appendUnique(list []string, value string) []string {
	for _, v := range list {
		if v == value {
			return list
		}
	}
	return append(list, value)
}
