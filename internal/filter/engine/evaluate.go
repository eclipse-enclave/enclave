// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"enclave/internal/domainpattern"
	"enclave/internal/filter"
)

var methodOverrideHeaders = []string{"X-Http-Method-Override", "X-Http-Method", "X-Method-Override"}

// Decision is the outcome of Evaluate. When Denial is nil the request was
// prepared for forwarding: its credential header holds the real value, its
// body is buffered if it was inspected, and Accept-Encoding is dropped when
// the response must be inspected. The caller must then call Finish with the
// upstream status, or Finish(0) when forwarding failed.
type Decision struct {
	Filter     string
	Op         string
	Capability string
	Denial     *Denial

	engine       *Engine
	route        *route
	input        bindInput
	reservations []*Reservation
	usedToken    string
	finished     bool
}

// Allowed reports whether the request may be forwarded.
func (d *Decision) Allowed() bool { return d.Denial == nil }

// Evaluate checks a request to a governed host and, when it is allowed,
// rewrites it for forwarding. scheme is "https" or "http" as received by the
// gateway. Evaluate never returns nil.
func (e *Engine) Evaluate(ctx context.Context, scheme string, req *http.Request) *Decision {
	d := &Decision{Filter: e.policy.Filter, engine: e}
	d.Denial = e.evaluate(ctx, scheme, req, d)
	if d.Denial != nil {
		d.release()
		d.finished = true
	}
	return d
}

func (e *Engine) evaluate(ctx context.Context, scheme string, req *http.Request, d *Decision) *Denial {
	if req == nil || req.URL == nil {
		return e.forbid(ReasonAmbiguous, "malformed request")
	}
	host := normalizeHost(req.Host)
	if host == "" {
		host = normalizeHost(req.URL.Host)
	}
	if host == "" || !filter.HostGoverned(host, e.policy.Hosts) {
		return e.forbid(ReasonNotGoverned, "host %s is not governed by this filter", quote(host))
	}
	if !strings.EqualFold(scheme, "https") {
		return e.forbid(ReasonPlainHTTP, "plain HTTP to %s is not allowed, use HTTPS", host)
	}
	if req.TLS != nil && req.TLS.ServerName != "" && normalizeHost(req.TLS.ServerName) != host {
		return e.forbid(ReasonAmbiguous, "TLS server name and Host header differ")
	}

	// I5: forms an upstream could read differently from the filter.
	for _, name := range methodOverrideHeaders {
		if _, ok := req.Header[name]; ok {
			return e.forbid(ReasonAmbiguous, "method override headers are not allowed")
		}
	}
	if _, ok := req.Header["Upgrade"]; ok {
		return e.forbid(ReasonAmbiguous, "protocol upgrades are not allowed")
	}
	if len(req.Trailer) > 0 {
		return e.forbid(ReasonAmbiguous, "request trailers are not allowed")
	}
	if len(req.Header.Values("Content-Type")) > 1 || len(req.Header.Values("Content-Encoding")) > 1 {
		return e.forbid(ReasonAmbiguous, "repeated Content-Type or Content-Encoding headers are not allowed")
	}
	if req.URL.Opaque != "" || req.URL.User != nil {
		return e.forbid(ReasonAmbiguous, "malformed request target")
	}
	contentType := req.Header.Get("Content-Type")
	if hasBody(req) {
		if contentType == "" {
			return e.forbid(ReasonAmbiguous, "a request body needs a Content-Type header")
		}
		if !validContentType(contentType) {
			return e.forbid(ReasonAmbiguous, "the Content-Type header does not parse")
		}
	}
	if denial := e.checkRequestTarget(req); denial != nil {
		return denial
	}
	escapedPath := req.URL.EscapedPath()
	path, err := filter.ParseRequestPath(escapedPath)
	if err != nil {
		return e.forbid(ReasonAmbiguous, "ambiguous path: %v", err)
	}
	query, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return e.forbid(ReasonAmbiguous, "ambiguous query string")
	}

	// I4 and I11: foreign credentials and placeholder leaks.
	if denial := e.checkCredentials(req, query, path); denial != nil {
		return denial
	}
	for _, name := range filter.SortedKeys(query) {
		if !filter.ValidQueryParamName(name) {
			return e.forbid(ReasonAmbiguous, "query parameter name %s is not allowed, names use only letters, digits, \".\", \"_\", \"~\", and \"-\"", quote(name))
		}
	}

	d.input = bindInput{query: query, header: req.Header, bodyContentType: contentType, jsonKeys: e.policy.JSONKeys}
	body := &requestBody{req: req}
	r, captures, denial := e.match(req, host, path, query, body)
	if denial != nil {
		return denial
	}
	if r == nil {
		return e.forbid(ReasonNoRoute, "no route for %s %s%s", req.Method, host, quoteless(escapedPath))
	}
	d.route, d.Op, d.Capability = r, r.Op, r.Capability
	d.input.captures = captures
	if r.Action == filter.ActionDeny {
		if r.Reason != "" {
			return e.forbid(ReasonDenyRoute, "%s is never allowed: %s", r.Op, r.Reason)
		}
		return e.forbid(ReasonDenyRoute, "%s is never allowed", r.Op)
	}
	if !r.AllowFormBody && isFormBody(contentType) {
		return e.forbid(ReasonFormBody, "%s: form-encoded request bodies are not allowed on governed hosts", r.Op)
	}
	if !r.enabled {
		return e.forbid(ReasonCapability, "%s needs capability %s, not granted by preset %s", r.Op, r.Capability, e.policy.Preset)
	}
	injectHeader := http.CanonicalHeaderKey(e.policy.Inject.Header)
	if !r.public && req.Header.Get(injectHeader) == "" {
		return e.forbid(ReasonMissingCredential, "%s needs the filter placeholder in the %s header", r.Op, injectHeader)
	}

	if r.readsBody || r.MultipartParts > 0 {
		data, denial := body.read(e, r.MaxBodyBytes)
		if denial != nil {
			return denial
		}
		d.input.body = data
	}
	if r.MultipartParts > 0 {
		parts, err := parseMultipart(d.input.body, contentType)
		if err != nil {
			return e.forbid(ReasonBody, "%s: %v", r.Op, err)
		}
		if len(parts) != r.MultipartParts {
			return e.forbid(ReasonBody, "%s: multipart body must have %d parts (got %d)", r.Op, r.MultipartParts, len(parts))
		}
	}
	vars := map[string][]string{}
	for _, name := range r.reqVars {
		values, err := e.bindVar(r, name, d.input)
		if err != nil {
			return e.forbid(ReasonBind, "%s: cannot read %s from the request: %v", r.Op, name, err)
		}
		vars[name] = values
	}
	for _, check := range r.checks {
		if denial := e.checkConstraint(ctx, r, check, vars); denial != nil {
			return denial
		}
	}
	if len(r.reqCounts) > 0 {
		res, exceeded := e.ledger.Reserve(needsFor(r.reqCounts, vars))
		if exceeded != nil {
			return e.exhausted(exceeded.Counter, exceeded.Limit, "")
		}
		d.reservations = append(d.reservations, res)
	}

	if r.public {
		req.Header.Del(injectHeader)
	} else {
		value, err := e.tokens.Token(ctx)
		if err != nil {
			return e.tokenDenial(err)
		}
		req.Header.Set(injectHeader, e.policy.Inject.Apply(value))
		d.usedToken = value
	}
	if len(r.resCounts) > 0 {
		req.Header.Del("Accept-Encoding")
	}
	return nil
}

// hasBody reports whether a request carries a body or may carry one, as with
// chunked transfer encoding.
func hasBody(req *http.Request) bool {
	return req.ContentLength != 0
}

// validContentType reports whether value parses as a type/subtype media type
// with well-formed parameters.
func validContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	typ, subtype, ok := strings.Cut(mediaType, "/")
	return ok && typ != "" && subtype != "" && !strings.Contains(subtype, "/")
}

// checkRequestTarget rejects a request target with characters outside the
// RFC 3986 path and query grammar. A raw "#" makes some servers drop the rest
// of the target as a fragment. It checks the target as received and as it is
// forwarded.
func (e *Engine) checkRequestTarget(req *http.Request) *Denial {
	if raw := req.RequestURI; strings.HasPrefix(raw, "/") {
		rawPath, rawQuery, _ := strings.Cut(raw, "?")
		if err := filter.CheckRawPath(rawPath); err != nil {
			return e.forbid(ReasonAmbiguous, "ambiguous path: %v", err)
		}
		if err := filter.CheckRawQuery(rawQuery); err != nil {
			return e.forbid(ReasonAmbiguous, "ambiguous query string: %v", err)
		}
	}
	if err := filter.CheckRawQuery(req.URL.RawQuery); err != nil {
		return e.forbid(ReasonAmbiguous, "ambiguous query string: %v", err)
	}
	return nil
}

// quoteless renders a request path for a message without quoting, bounded in
// length.
func quoteless(path string) string {
	if len(path) > maxQuotedValue {
		return path[:maxQuotedValue] + "..."
	}
	return path
}

func normalizeHost(value string) string {
	host, err := domainpattern.NormalizeHost(value)
	if err != nil {
		return ""
	}
	return host
}

// checkCredentials enforces I4 and I11 on a governed host: no denied headers
// or parameters, the injection header absent or exactly the placeholder form,
// no Authorization carrying anything else, and the placeholder nowhere else.
func (e *Engine) checkCredentials(req *http.Request, query url.Values, path filter.RequestPath) *Denial {
	placeholder := e.policy.Placeholder
	placeholderLower := strings.ToLower(placeholder)
	for _, name := range filter.SortedKeys(req.Header) {
		values := req.Header[name]
		lower := strings.ToLower(name)
		if e.denyHeaders[lower] {
			return e.forbid(ReasonForeignCredential, "header %s is not allowed on governed hosts", name)
		}
		if strings.Contains(lower, placeholderLower) {
			return e.forbid(ReasonPlaceholder, "the filter placeholder is only accepted in the %s header", e.policy.Inject.Header)
		}
		if lower == e.policy.Inject.Header {
			if len(values) != 1 || (values[0] != placeholder && values[0] != e.formatted) {
				return e.forbid(ReasonForeignCredential, "the %s header must carry the filter placeholder and nothing else", name)
			}
			continue
		}
		if lower == filter.AuthorizationHeader {
			return e.forbid(ReasonForeignCredential, "the %s header is not allowed, the filter injects %s", name, e.policy.Inject.Header)
		}
		for _, v := range values {
			if strings.Contains(v, placeholder) {
				return e.forbid(ReasonPlaceholder, "the filter placeholder is only accepted in the %s header", e.policy.Inject.Header)
			}
		}
	}
	if strings.Contains(req.URL.EscapedPath(), placeholder) || strings.Contains(req.URL.RawQuery, placeholder) {
		return e.forbid(ReasonPlaceholder, "the filter placeholder is not allowed in the URL")
	}
	for _, seg := range path.Segments {
		if strings.Contains(seg, placeholder) {
			return e.forbid(ReasonPlaceholder, "the filter placeholder is not allowed in the URL")
		}
	}
	for _, name := range filter.SortedKeys(query) {
		// Folded, so that a spelling such as Access_Token, which a server may
		// read as access_token, is denied too.
		if e.denyParams[filter.FoldParam(name)] {
			return e.forbid(ReasonForeignCredential, "query parameter %s is not allowed on governed hosts", quote(name))
		}
		if strings.Contains(name, placeholder) {
			return e.forbid(ReasonPlaceholder, "the filter placeholder is not allowed in the URL")
		}
		for _, v := range query[name] {
			if strings.Contains(v, placeholder) {
				return e.forbid(ReasonPlaceholder, "the filter placeholder is not allowed in the URL")
			}
		}
	}
	return nil
}

// requestBody buffers the request body once, on first need, and decodes the
// JSON document routes match on once.
type requestBody struct {
	req  *http.Request
	done bool
	data []byte

	decoded bool
	doc     any
	docErr  error
}

// json returns the JSON document of the buffered body, see jsonDocument.
func (b *requestBody) json(contentType string, keys string) (any, error) {
	if !b.decoded {
		b.decoded = true
		b.doc, b.docErr = jsonDocument(b.data, contentType, keys)
	}
	return b.doc, b.docErr
}

func (b *requestBody) read(e *Engine, limit int64) ([]byte, *Denial) {
	if b.done {
		if int64(len(b.data)) > limit {
			return nil, e.forbid(ReasonBody, "request body exceeds the inspection limit of %d bytes", limit)
		}
		return b.data, nil
	}
	b.done = true
	if !identityEncoding(b.req.Header.Values("Content-Encoding")) {
		return nil, e.forbid(ReasonBody, "compressed request bodies cannot be inspected")
	}
	var data []byte
	if b.req.Body != nil && b.req.Body != http.NoBody {
		var err error
		data, err = io.ReadAll(io.LimitReader(b.req.Body, limit+1))
		_ = b.req.Body.Close()
		if err != nil {
			return nil, e.forbid(ReasonBody, "cannot read the request body")
		}
		if int64(len(data)) > limit {
			return nil, e.forbid(ReasonBody, "request body exceeds the inspection limit of %d bytes", limit)
		}
	}
	b.data = data
	b.req.Body = io.NopCloser(bytes.NewReader(data))
	b.req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	b.req.ContentLength = int64(len(data))
	b.req.TransferEncoding = nil
	if len(data) == 0 {
		b.req.Body = http.NoBody
	}
	return data, nil
}

// isFormBody reports whether a request body may carry form parameters, which
// some servers read like query parameters, credentials included.
func isFormBody(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType, _, _ = strings.Cut(contentType, ";")
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "application/x-www-form-urlencoded", "multipart/form-data":
		return true
	}
	return false
}

func identityEncoding(values []string) bool {
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if p := strings.ToLower(strings.TrimSpace(part)); p != "" && p != "identity" {
				return false
			}
		}
	}
	return true
}

// match finds the first route matching method, host, path, query, and JSON
// conditions. A request parameter that is ambiguous for a route whose
// method, host, and path match denies the request. So does a body that a
// candidate route's JSON conditions cannot read: it must never make a route
// miss, or a request could pass a deny route and reach a broader allow route.
func (e *Engine) match(req *http.Request, host string, path filter.RequestPath, query url.Values, body *requestBody) (*route, map[string]string, *Denial) {
	for _, r := range e.routes {
		if !r.methods[req.Method] || !routeHostMatches(r.Hosts, host) {
			continue
		}
		captures, ok := r.template.Match(path)
		if !ok {
			continue
		}
		if name, param, ambiguous := ambiguousParam(r.params, query); ambiguous {
			return nil, nil, e.forbid(ReasonAmbiguous, "%s: query parameter %s is ambiguous with %s", r.Op, quote(name), param)
		}
		if !queryMatches(r.query, query) || !queryPatternsMatch(r.queryMatch, query) {
			continue
		}
		if r.readsJSON() {
			if _, denial := body.read(e, r.MaxBodyBytes); denial != nil {
				return nil, nil, denial
			}
			doc, err := body.json(req.Header.Get("Content-Type"), e.policy.JSONKeys)
			if err != nil {
				return nil, nil, e.forbid(ReasonBody, "%s: %v", r.Op, err)
			}
			matched, err := r.jsonConditionsMatch(doc)
			var ambiguous *filter.AmbiguousKeyError
			if errors.As(err, &ambiguous) {
				return nil, nil, e.forbid(ReasonAmbiguous, "%s: %v", r.Op, err)
			}
			if err != nil {
				return nil, nil, e.forbid(ReasonBody, "%s: the request body does not have the shape the route checks: %v", r.Op, err)
			}
			if !matched {
				continue
			}
		}
		return r, captures, nil
	}
	return nil, nil, nil
}

func routeHostMatches(patterns []string, host string) bool {
	for _, p := range patterns {
		if filter.MatchHost(host, p) {
			return true
		}
	}
	return false
}

// ambiguousParam finds a request parameter that folds like a parameter the
// route constrains or binds without being its name or protobuf alias. A
// server may read Format or for_mat as format, or ignore it and fall back to
// a default the route does not allow, so the route cannot be checked.
func ambiguousParam(params []routeParam, query url.Values) (name, param string, ambiguous bool) {
	if len(params) == 0 {
		return "", "", false
	}
	for _, name := range filter.SortedKeys(query) {
		fold := filter.FoldParam(name)
		for _, p := range params {
			if p.fold == fold && !slices.Contains(p.names, name) {
				return name, p.name, true
			}
		}
	}
	return "", "", false
}

// queryValues collects the values of all request parameters in names.
func queryValues(query url.Values, names []string) []string {
	var values []string
	for _, name := range names {
		values = append(values, query[name]...)
	}
	return values
}

func queryMatches(checks []queryCheck, query url.Values) bool {
	for _, check := range checks {
		values := queryValues(query, check.names)
		if len(values) == 0 {
			if !check.cond.Any && !check.cond.Absent {
				return false
			}
			continue
		}
		for _, v := range values {
			if !check.cond.AllowsValue(v) {
				return false
			}
		}
	}
	return true
}

func queryPatternsMatch(checks []queryMatch, query url.Values) bool {
	for _, check := range checks {
		if !slices.ContainsFunc(queryValues(query, check.names), check.re.MatchString) {
			return false
		}
	}
	return true
}

// jsonDocument returns the JSON document route conditions apply to: the
// body, or for a multipart body its first part, which servers such as
// Google's upload endpoints read as metadata. The body or part must declare
// JSON (jsonMediaType) and hold a JSON object. An empty body or first part
// is an absent document, so every path selects nothing. Errors never quote
// the body.
func jsonDocument(body []byte, contentType string, keys string) (any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && strings.HasPrefix(mediaType, "multipart/") {
		parts, err := parseMultipart(body, contentType)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(parts[0].data)) == 0 {
			return nil, nil
		}
		return decodeJSONObject(parts[0].data, parts[0].contentType, keys, "the first multipart part")
	}
	return decodeJSONObject(body, contentType, keys, "the request body")
}

// decodeJSONObject decodes data, described as what in errors, as a JSON
// object of a JSON media type.
func decodeJSONObject(data []byte, contentType string, keys string, what string) (any, error) {
	if !jsonMediaType(contentType) {
		return nil, fmt.Errorf("%s is not declared as JSON, send it as application/json", what)
	}
	doc, err := filter.DecodeJSONKeys(data, keys)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid JSON or repeats a key", what)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, fmt.Errorf("%s is not a JSON object", what)
	}
	return doc, nil
}

// jsonMediaType reports whether contentType declares JSON:
// application/json or an application/*+json type, without a charset other
// than UTF-8. A server may parse other types differently, as Google APIs
// parse application/json+protobuf as JSPB, an array encoding without keys.
func jsonMediaType(contentType string) bool {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	if mediaType != "application/json" && (!strings.HasPrefix(mediaType, "application/") || !strings.HasSuffix(mediaType, "+json")) {
		return false
	}
	charset, ok := params["charset"]
	return !ok || strings.EqualFold(charset, "utf-8") || strings.EqualFold(charset, "utf8")
}

// jsonConditionsMatch evaluates the route's json, jsonPresent, jsonMatch,
// and jsonAllowedKeys conditions on doc. An error means the document does not
// have the shape a path expects, or has a key spelled like one a path reads
// (*filter.AmbiguousKeyError).
func (r *route) jsonConditionsMatch(doc any) (bool, error) {
	for _, check := range r.json {
		nodes, err := check.path.Select(doc)
		if err != nil {
			return false, err
		}
		if len(nodes) == 0 {
			if !check.cond.Absent {
				return false, nil
			}
			continue
		}
		for _, node := range nodes {
			if !check.cond.AllowsJSON(node) {
				return false, nil
			}
		}
	}
	if len(r.jsonPresent) > 0 {
		present := false
		for _, path := range r.jsonPresent {
			nodes, err := path.SelectPresent(doc)
			if err != nil {
				return false, err
			}
			if len(nodes) > 0 {
				present = true
				break
			}
		}
		if !present {
			return false, nil
		}
	}
	for _, check := range r.jsonMatch {
		nodes, err := check.path.Select(doc)
		if err != nil {
			return false, err
		}
		if !slices.ContainsFunc(nodes, func(node any) bool {
			value, ok := jsonScalarString(node)
			return ok && check.re.MatchString(value)
		}) {
			return false, nil
		}
	}
	for _, check := range r.jsonKeys {
		nodes, err := check.path.Select(doc)
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			obj, ok := node.(map[string]any)
			if !ok {
				return false, fmt.Errorf("%s selects a value that is not an object", check.path)
			}
			for key := range obj {
				if !check.keys[key] {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

// jsonScalarString renders a JSON scalar the way binds read it: strings as
// they are, numbers and booleans in their JSON spelling.
func jsonScalarString(node any) (string, bool) {
	switch v := node.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}

func (e *Engine) bindVar(r *route, name string, in bindInput) ([]string, error) {
	if steps, ok := r.Bind[name]; ok {
		return runBind(steps, in)
	}
	if value, ok := in.captures[name]; ok {
		return []string{value}, nil
	}
	return nil, fmt.Errorf("%s is not bound", name)
}

func needsFor(counts []filter.Count, vars map[string][]string) []Need {
	needs := make([]Need, 0, len(counts))
	for _, c := range counts {
		need := Need{Counter: c.Counter}
		if c.Key != "" {
			need.Keys = vars[c.Key]
		}
		needs = append(needs, need)
	}
	return needs
}

// InspectsResponse reports whether the response must go through
// InspectResponse before it is relayed.
func (d *Decision) InspectsResponse() bool {
	return d.Denial == nil && d.route != nil && len(d.route.resCounts) > 0
}

// InspectResponse buffers a 2xx response of a route with response binds,
// counts it, and replaces its body with the buffered copy. A non-nil Denial
// means the response must be withheld and the Denial sent instead. A 3xx
// response is withheld, since its target may serve the content uncounted.
// Other responses pass untouched.
func (d *Decision) InspectResponse(resp *http.Response) *Denial {
	if !d.InspectsResponse() || d.finished || resp == nil || resp.StatusCode < 200 || resp.StatusCode > 399 {
		return nil
	}
	e, r := d.engine, d.route
	fail := func(denial *Denial) *Denial {
		d.release()
		d.finished = true
		return denial
	}
	if resp.StatusCode > 299 {
		return fail(e.forbid(ReasonResponse, "%s: a redirect response cannot be counted", r.Op))
	}
	if !identityEncoding(resp.Header.Values("Content-Encoding")) {
		return fail(e.forbid(ReasonResponse, "%s: compressed responses cannot be inspected", r.Op))
	}
	var data []byte
	if resp.Body != nil {
		var err error
		data, err = io.ReadAll(io.LimitReader(resp.Body, r.MaxResponseBytes+1))
		_ = resp.Body.Close()
		if err != nil {
			return fail(e.forbid(ReasonResponse, "%s: cannot read the response", r.Op))
		}
	}
	if int64(len(data)) > r.MaxResponseBytes {
		return fail(e.forbid(ReasonResponse, "%s: response exceeds the inspection limit of %d bytes", r.Op, r.MaxResponseBytes))
	}
	in := d.input
	in.response = data
	in.responseType = resp.Header.Get("Content-Type")
	vars := map[string][]string{}
	for _, name := range r.respVars {
		values, err := runBind(r.Bind[name], in)
		if err != nil {
			return fail(e.forbid(ReasonResponse, "%s: cannot read %s from the response: %v", r.Op, name, err))
		}
		vars[name] = values
	}
	res, exceeded := e.ledger.Reserve(needsFor(r.resCounts, vars))
	if exceeded != nil {
		return fail(e.exhausted(exceeded.Counter, exceeded.Limit, ", response withheld"))
	}
	d.reservations = append(d.reservations, res)
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	resp.TransferEncoding = nil
	resp.Header.Del("Content-Encoding")
	resp.Header.Set("Content-Length", strconv.Itoa(len(data)))
	return nil
}

// Finish commits the request's quota reservations when status is 2xx or
// 3xx and releases them otherwise. A redirect counts, since its target may
// serve the content without the filter. A 401 invalidates the injected token.
// Further calls do nothing.
func (d *Decision) Finish(status int) {
	if d.finished {
		return
	}
	d.finished = true
	if status >= 200 && status <= 399 {
		for _, res := range d.reservations {
			res.Commit()
		}
	} else {
		d.release()
	}
	if status == http.StatusUnauthorized && d.usedToken != "" {
		d.engine.tokens.Invalidate(d.usedToken)
	}
}

func (d *Decision) release() {
	for _, res := range d.reservations {
		res.Release()
	}
}
