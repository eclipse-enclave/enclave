// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package engine enforces a compiled filter policy on requests and responses.
// The gateway creates one Engine per active filter and calls Evaluate for each
// request to a governed host.
package engine

import (
	"fmt"
	"net/http"
	"regexp"
	"time"

	"enclave/internal/filter"
	"enclave/internal/filter/token"
)

// Options configures an Engine. Either Credential (with Client) or Tokens is
// required. Credential builds the token source and, with Client, the resolver
// fetcher. Tokens and Fetcher replace them, for tests or custom transports.
type Options struct {
	Policy     *filter.Policy
	Credential *filter.Credential
	// Client sends token refreshes and resolver lookups. It must verify TLS.
	Client  *http.Client
	Tokens  token.Source
	Fetcher Fetcher
	// Now defaults to time.Now.
	Now func() time.Time
}

// Engine evaluates requests against one filter policy. It is safe for
// concurrent use. Quota state lives in the Engine for the session.
type Engine struct {
	policy      *filter.Policy
	tokens      token.Source
	routes      []*route
	resolvers   map[string]*resolver
	ledger      *Ledger
	denyHeaders map[string]bool
	denyParams  map[string]bool
	// formatted is the inject format applied to the placeholder.
	formatted string
}

type route struct {
	filter.Route
	methods     map[string]bool
	template    *filter.PathTemplate
	query       []queryCheck
	queryMatch  []queryMatch
	params      []routeParam
	json        []jsonCheck
	jsonPresent []filter.JSONPath
	jsonMatch   []jsonMatch
	jsonKeys    []jsonKeysCheck
	enabled     bool
	public      bool
	checks      []constraintCheck
	reqVars     []string
	reqCounts   []filter.Count
	respVars    []string
	resCounts   []filter.Count
	readsBody   bool
}

type queryCheck struct {
	// names are the request parameter names the condition reads.
	names []string
	cond  filter.QueryCondition
}

// routeParam is a query parameter a route constrains or binds. A request
// parameter that folds like it but is none of its names is ambiguous.
type routeParam struct {
	name  string
	fold  string
	names []string
}

// queryMatch is a queryMatch entry: some value of the parameter must match.
type queryMatch struct {
	names []string
	re    *regexp.Regexp
}

type jsonCheck struct {
	path filter.JSONPath
	cond filter.JSONCondition
}

// jsonMatch is a jsonMatch entry: some scalar at the path must match.
type jsonMatch struct {
	path filter.JSONPath
	re   *regexp.Regexp
}

// jsonKeysCheck is a jsonAllowedKeys entry: every object at the path has
// only the listed keys.
type jsonKeysCheck struct {
	path filter.JSONPath
	keys map[string]bool
}

type constraintCheck struct {
	name       string
	constraint filter.Constraint
	// bound is false when the route does not bind the constraint's variable.
	bound bool
}

// New validates the policy and builds an Engine. It fails on any error, so the
// gateway fails closed.
func New(opts Options) (*Engine, error) {
	if opts.Policy == nil {
		return nil, fmt.Errorf("filter engine: policy is required")
	}
	p := opts.Policy
	if err := p.Validate(); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	tokens := opts.Tokens
	if tokens == nil {
		if err := p.CheckCredential(opts.Credential); err != nil {
			return nil, err
		}
		src, err := token.New(opts.Credential, opts.Client)
		if err != nil {
			return nil, fmt.Errorf("filter %s: %w", p.Filter, err)
		}
		tokens = src
	} else if opts.Credential != nil {
		return nil, fmt.Errorf("filter %s: set either Credential or Tokens", p.Filter)
	}
	fetcher := opts.Fetcher
	if fetcher == nil {
		fetcher = &HTTPFetcher{Client: opts.Client, Tokens: tokens, Inject: p.Inject}
	}

	e := &Engine{
		policy:      p,
		tokens:      tokens,
		resolvers:   map[string]*resolver{},
		denyHeaders: map[string]bool{},
		denyParams:  map[string]bool{},
		formatted:   p.Inject.Apply(p.Placeholder),
	}
	for _, h := range p.DenyHeaders {
		e.denyHeaders[h] = true
	}
	for _, name := range p.DenyParams {
		e.denyParams[filter.FoldParam(name)] = true
	}
	cache := newParentCache(now)
	for name, def := range p.Resolvers {
		extract, err := filter.ParseJSONPath(def.Extract)
		if err != nil {
			return nil, fmt.Errorf("filter %s: resolvers.%s: %w", p.Filter, name, err)
		}
		if p.JSONKeys == filter.JSONKeysProtobuf {
			extract = extract.WithProtobufNames()
		}
		e.resolvers[name] = &resolver{name: name, def: def, extract: extract, fetch: fetcher, cache: cache, jsonKeys: p.JSONKeys}
	}
	limits := map[string]int64{}
	keyed := map[string]bool{}
	for name, c := range p.Counters {
		if c.Quota != nil {
			limits[name] = *c.Quota
		}
	}
	for i, r := range p.Routes {
		compiled, err := e.compileRoute(r)
		if err != nil {
			return nil, fmt.Errorf("filter %s: %s: %w", p.Filter, filter.RouteLabel(i, r.Op), err)
		}
		e.routes = append(e.routes, compiled)
		for _, c := range r.Count {
			if c.Key != "" {
				keyed[c.Counter] = true
			}
		}
	}
	e.ledger = NewLedger(limits, keyed)
	return e, nil
}

func (e *Engine) compileRoute(r filter.Route) (*route, error) {
	tpl, err := filter.ParsePathTemplate(r.Path)
	if err != nil {
		return nil, err
	}
	capability := e.policy.Capabilities[r.Capability]
	cr := &route{
		Route:    r,
		methods:  map[string]bool{},
		template: tpl,
		enabled:  capability.Enabled,
		public:   capability.Public,
	}
	for _, m := range r.Methods {
		cr.methods[m] = true
	}
	params := map[string]bool{}
	for _, name := range filter.SortedKeys(r.Query) {
		cr.query = append(cr.query, queryCheck{names: filter.QueryParamNames(name, e.policy.JSONKeys), cond: r.Query[name]})
		params[name] = true
	}
	for _, name := range filter.SortedKeys(r.QueryMatch) {
		re, err := filter.CompileMatch(r.QueryMatch[name])
		if err != nil {
			return nil, fmt.Errorf("queryMatch.%s: %w", name, err)
		}
		cr.queryMatch = append(cr.queryMatch, queryMatch{names: filter.QueryParamNames(name, e.policy.JSONKeys), re: re})
		params[name] = true
	}
	for _, steps := range r.Bind {
		for _, step := range steps {
			if step.Op == filter.StepQuery {
				params[step.Arg] = true
			}
		}
	}
	for _, name := range filter.SortedKeys(params) {
		cr.params = append(cr.params, routeParam{name: name, fold: filter.FoldParam(name), names: filter.QueryParamNames(name, e.policy.JSONKeys)})
	}
	for _, raw := range filter.SortedKeys(r.JSON) {
		path, err := e.jsonPath(raw)
		if err != nil {
			return nil, err
		}
		cr.json = append(cr.json, jsonCheck{path: path, cond: r.JSON[raw]})
	}
	for _, raw := range r.JSONPresent {
		path, err := e.jsonPath(raw)
		if err != nil {
			return nil, err
		}
		cr.jsonPresent = append(cr.jsonPresent, path)
	}
	for _, raw := range filter.SortedKeys(r.JSONMatch) {
		path, err := e.jsonPath(raw)
		if err != nil {
			return nil, err
		}
		re, err := filter.CompileMatch(r.JSONMatch[raw])
		if err != nil {
			return nil, fmt.Errorf("jsonMatch.%s: %w", raw, err)
		}
		cr.jsonMatch = append(cr.jsonMatch, jsonMatch{path: path, re: re})
	}
	for _, raw := range filter.SortedKeys(r.JSONAllowedKeys) {
		path, err := e.jsonPath(raw)
		if err != nil {
			return nil, err
		}
		keys := map[string]bool{}
		for _, key := range r.JSONAllowedKeys[raw] {
			if e.policy.JSONKeys == filter.JSONKeysProtobuf {
				key = filter.ProtobufJSONName(key)
			}
			keys[key] = true
		}
		cr.jsonKeys = append(cr.jsonKeys, jsonKeysCheck{path: path, keys: keys})
	}
	if r.Action == filter.ActionDeny {
		return cr, nil
	}
	requestBound := map[string]bool{}
	for _, name := range tpl.Captures() {
		requestBound[name] = true
	}
	responseBound := map[string]bool{}
	for name, steps := range r.Bind {
		if filter.IsResponseBind(steps) {
			responseBound[name] = true
			delete(requestBound, name)
		} else {
			requestBound[name] = true
		}
	}
	needReq := map[string]bool{}
	if capability.Enabled && !capability.Public {
		for _, cname := range filter.SortedKeys(capability.Constraints) {
			con := capability.Constraints[cname]
			if con.Value.Any {
				continue
			}
			check := constraintCheck{name: cname, constraint: con, bound: requestBound[con.Var]}
			cr.checks = append(cr.checks, check)
			if check.bound {
				needReq[con.Var] = true
			}
		}
	}
	needResp := map[string]bool{}
	for _, c := range r.Count {
		counter := e.policy.Counters[c.Counter]
		if counter.Quota == nil {
			continue
		}
		switch {
		case c.Key == "" || requestBound[c.Key]:
			cr.reqCounts = append(cr.reqCounts, c)
			if c.Key != "" {
				needReq[c.Key] = true
			}
		case responseBound[c.Key]:
			cr.resCounts = append(cr.resCounts, c)
			needResp[c.Key] = true
		}
	}
	cr.reqVars = filter.SortedKeys(needReq)
	cr.respVars = filter.SortedKeys(needResp)
	for _, v := range cr.reqVars {
		if filter.ReadsRequestBody(r.Bind[v]) {
			cr.readsBody = true
		}
	}
	return cr, nil
}

// jsonPath parses a JSON path of the policy, renamed for protobuf keys when
// the policy reads JSON that way.
func (e *Engine) jsonPath(raw string) (filter.JSONPath, error) {
	path, err := filter.ParseJSONPath(raw)
	if err != nil {
		return filter.JSONPath{}, err
	}
	if e.policy.JSONKeys == filter.JSONKeysProtobuf {
		path = path.WithProtobufNames()
	}
	return path, nil
}

// readsJSON reports whether matching the route evaluates the request body
// as JSON.
func (r *route) readsJSON() bool {
	return len(r.json) > 0 || len(r.jsonPresent) > 0 || len(r.jsonMatch) > 0 || len(r.jsonKeys) > 0
}

// Filter returns the filter name.
func (e *Engine) Filter() string { return e.policy.Filter }

// Preset returns the active preset name.
func (e *Engine) Preset() string { return e.policy.Preset }

// Policy returns the policy the engine enforces. Callers must not modify it.
func (e *Engine) Policy() *filter.Policy { return e.policy }

// Ledger exposes the session quota state.
func (e *Engine) Ledger() *Ledger { return e.ledger }

// Governs reports whether the engine governs host. host may carry a port.
func (e *Engine) Governs(host string) bool {
	normalized := normalizeHost(host)
	return normalized != "" && filter.HostGoverned(normalized, e.policy.Hosts)
}
