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
	"fmt"
	"io"
	"net/url"
	"strings"

	"enclave/internal/domainpattern"
	"enclave/internal/model"
	"enclave/internal/util"
)

// Compiled policy identity.
const (
	PolicySchemaVersion = "1"
	PolicyKind          = "filter-policy"
)

// Route actions.
const (
	ActionAllow = "allow"
	ActionDeny  = "deny"
)

// JSONKeysProtobuf makes the engine read JSON object keys the way protobuf
// JSON parsers do: snake_case keys are renamed to lowerCamelCase, and a body
// whose keys collide after renaming is rejected. Query conditions and binds
// on a lowerCamelCase parameter also read its snake_case alias, see
// QueryParamNames.
const JSONKeysProtobuf = "protobuf"

// Credential types.
const (
	CredentialOAuth2 = "oauth2"
	CredentialStatic = "static"
)

// Inspection limits in bytes.
const (
	DefaultMaxBodyBytes     int64 = 10 << 20
	DefaultMaxResponseBytes int64 = 32 << 20
	MaxInspectBytes         int64 = 64 << 20
)

// Resolver walk depth.
const (
	DefaultResolverDepth = 32
	MaxResolverDepth     = 64
)

// MinPlaceholderLength is the shortest placeholder a policy accepts.
const MinPlaceholderLength = 22

// AuthorizationHeader is always treated as a credential header on governed
// hosts, whatever the injection header is.
const AuthorizationHeader = "authorization"

// ResolverIDToken is the placeholder for the looked up ID in a resolver URL.
const ResolverIDToken = "{id}"

const maxDocumentBytes = 16 << 20

// Policy is the compiled, non-secret form of a filter with its active preset.
// The host writes it, the gateway loads it.
type Policy struct {
	SchemaVersion  string                `json:"schemaVersion"`
	Kind           string                `json:"kind"`
	Filter         string                `json:"filter"`
	Preset         string                `json:"preset"`
	CredentialType string                `json:"credentialType"`
	Hosts          []string              `json:"hosts"`
	Confine        []string              `json:"confine,omitempty"`
	Placeholder    string                `json:"placeholder"`
	Inject         Inject                `json:"inject"`
	DenyHeaders    []string              `json:"denyHeaders,omitempty"`
	DenyParams     []string              `json:"denyParams,omitempty"`
	Capabilities   map[string]Capability `json:"capabilities"`
	Counters       map[string]Counter    `json:"counters,omitempty"`
	Resolvers      map[string]Resolver   `json:"resolvers,omitempty"`
	JSONKeys       string                `json:"jsonKeys,omitempty"`
	Routes         []Route               `json:"routes"`
	Deny           *DenyTemplate         `json:"deny,omitempty"`
}

// Inject says where the real credential goes. Header is lowercase. Format
// contains "%s" exactly once, or is empty for the raw value.
type Inject struct {
	Header string `json:"header"`
	Format string `json:"format,omitempty"`
}

// Apply formats value for the injection header.
func (i Inject) Apply(value string) string {
	if i.Format == "" {
		return value
	}
	return strings.Replace(i.Format, "%s", value, 1)
}

// Capability is a capability with its state under the active preset. Enabled
// is always true for public capabilities. Constraints carry values and are set
// only for enabled, non-public capabilities.
type Capability struct {
	Description string                `json:"description,omitempty"`
	Public      bool                  `json:"public,omitempty"`
	Enabled     bool                  `json:"enabled"`
	Constraints map[string]Constraint `json:"constraints,omitempty"`
}

// Constraint is a constraint definition with the value the preset gives it.
type Constraint struct {
	Kind     string          `json:"kind"`
	Var      string          `json:"var"`
	Resolver string          `json:"resolver,omitempty"`
	Value    ConstraintValue `json:"value"`
}

// Counter is a usage counter. Quota is nil when the preset sets none, in
// which case the counter is not tracked.
type Counter struct {
	Description string `json:"description,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Quota       *int64 `json:"quota,omitempty"`
}

// Resolver looks up the parents of a resource. MaxDepth is 1 when Recursive is
// false.
type Resolver struct {
	URL       string `json:"url"`
	Extract   string `json:"extract"`
	Recursive bool   `json:"recursive,omitempty"`
	MaxDepth  int    `json:"maxDepth"`
}

// Route maps requests to an operation of a capability, or denies them when
// Action is ActionDeny. AllowFormBody lets form-encoded bodies through, which
// are denied by default since servers may read credentials from them.
// MultipartParts, when set, is the exact part count of a multipart body.
//
// JSONPresent lists JSON paths of which at least one must reach a value,
// null included. JSONMatch and QueryMatch map a JSON path or a query
// parameter to an RE2 regular expression that some value must match,
// unanchored. JSONAllowedKeys maps a JSON path to the only keys the objects
// it selects may have. WildcardOnly names constraints the route does not bind
// on purpose, so it passes only when the preset sets them to "*".
type Route struct {
	Op               string                    `json:"op"`
	Action           string                    `json:"action,omitempty"`
	Reason           string                    `json:"reason,omitempty"`
	Capability       string                    `json:"capability,omitempty"`
	Methods          []string                  `json:"methods"`
	Hosts            []string                  `json:"hosts"`
	Path             string                    `json:"path"`
	Query            map[string]QueryCondition `json:"query,omitempty"`
	QueryMatch       map[string]string         `json:"queryMatch,omitempty"`
	JSON             map[string]JSONCondition  `json:"json,omitempty"`
	JSONPresent      []string                  `json:"jsonPresent,omitempty"`
	JSONMatch        map[string]string         `json:"jsonMatch,omitempty"`
	JSONAllowedKeys  map[string][]string       `json:"jsonAllowedKeys,omitempty"`
	WildcardOnly     []string                  `json:"wildcardOnly,omitempty"`
	Bind             map[string][]BindStep     `json:"bind,omitempty"`
	Count            []Count                   `json:"count,omitempty"`
	AllowFormBody    bool                      `json:"allowFormBody,omitempty"`
	MultipartParts   int                       `json:"multipartParts,omitempty"`
	MaxBodyBytes     int64                     `json:"maxBodyBytes"`
	MaxResponseBytes int64                     `json:"maxResponseBytes"`
}

// QueryCondition lists the allowed values of a query parameter. Absent allows
// the parameter to be missing. Any allows every value and absence.
type QueryCondition struct {
	Any    bool     `json:"any,omitempty"`
	Absent bool     `json:"absent,omitempty"`
	Values []string `json:"values,omitempty"`
}

// JSONCondition lists the allowed JSON scalar values at a path. Absent allows
// the path to select nothing (missing or null).
type JSONCondition struct {
	Absent bool              `json:"absent,omitempty"`
	Values []json.RawMessage `json:"values,omitempty"`
}

// Count adds to a counter. With Key, the counter is a set of the distinct
// values bound to that variable. Without, every request adds one.
type Count struct {
	Counter string `json:"counter"`
	Key     string `json:"key,omitempty"`
}

// DenyTemplate renders deny responses. Body may use {{message}} and
// {{status}} (JSON string literals) and {{code}} (the HTTP status).
type DenyTemplate struct {
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

var allowedMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true,
}

// ValidMethod reports whether a route may name method.
func ValidMethod(method string) bool { return allowedMethods[method] }

// ParsePolicy decodes and validates a compiled policy. Unknown fields are
// errors, so a policy written by a different Enclave version fails closed.
func ParsePolicy(data []byte) (*Policy, error) {
	var p Policy
	if err := decodeStrict(data, &p); err != nil {
		return nil, fmt.Errorf("parse filter policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// LoadPolicy reads and validates a compiled policy file.
func LoadPolicy(path string) (*Policy, error) {
	data, err := readLimited(path)
	if err != nil {
		return nil, fmt.Errorf("read filter policy: %w", err)
	}
	return ParsePolicy(data)
}

// Marshal encodes the policy as indented JSON.
func (p *Policy) Marshal() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing data after the document")
	}
	return nil
}

// readLimited reads a host-written policy or credential file.
func readLimited(path string) ([]byte, error) {
	return util.ReadRegularFile(path, maxDocumentBytes)
}

// Validate checks the whole policy. The gateway calls it on load and refuses
// to start on any error.
func (p *Policy) Validate() error {
	if p.SchemaVersion != PolicySchemaVersion {
		return fmt.Errorf("filter policy: schemaVersion must be %q (got %q)", PolicySchemaVersion, p.SchemaVersion)
	}
	if p.Kind != PolicyKind {
		return fmt.Errorf("filter policy: kind must be %q (got %q)", PolicyKind, p.Kind)
	}
	if err := ValidateName(p.Filter); err != nil {
		return fmt.Errorf("filter policy: filter: %w", err)
	}
	if err := ValidateName(p.Preset); err != nil {
		return fmt.Errorf("filter policy %s: preset: %w", p.Filter, err)
	}
	if err := p.validate(); err != nil {
		return fmt.Errorf("filter policy %s: %w", p.Filter, err)
	}
	return nil
}

func (p *Policy) validate() error {
	if p.JSONKeys != "" && p.JSONKeys != JSONKeysProtobuf {
		return fmt.Errorf("jsonKeys must be empty or %q", JSONKeysProtobuf)
	}
	if p.CredentialType != CredentialOAuth2 && p.CredentialType != CredentialStatic {
		return fmt.Errorf("credentialType must be %q or %q", CredentialOAuth2, CredentialStatic)
	}
	if err := ValidateHosts(p.Hosts); err != nil {
		return fmt.Errorf("hosts: %w", err)
	}
	if err := ValidateConfine(p.Confine, p.Hosts); err != nil {
		return fmt.Errorf("confine: %w", err)
	}
	if err := ValidatePlaceholder(p.Placeholder); err != nil {
		return fmt.Errorf("placeholder: %w", err)
	}
	if err := p.Inject.Validate(); err != nil {
		return fmt.Errorf("inject: %w", err)
	}
	if err := ValidateDenyHeaders(p.DenyHeaders, p.Inject.Header); err != nil {
		return fmt.Errorf("denyHeaders: %w", err)
	}
	if err := ValidateDenyParams(p.DenyParams); err != nil {
		return fmt.Errorf("denyParams: %w", err)
	}
	for _, name := range SortedKeys(p.Counters) {
		if err := ValidateName(name); err != nil {
			return fmt.Errorf("counters: %w", err)
		}
		if q := p.Counters[name].Quota; q != nil && *q < 0 {
			return fmt.Errorf("counters.%s.quota must not be negative", name)
		}
	}
	for _, name := range SortedKeys(p.Resolvers) {
		if err := ValidateIdent(name); err != nil {
			return fmt.Errorf("resolvers: %w", err)
		}
		if err := p.Resolvers[name].Validate(p.Hosts); err != nil {
			return fmt.Errorf("resolvers.%s.%w", name, err)
		}
	}
	if len(p.Capabilities) == 0 {
		return fmt.Errorf("capabilities must not be empty")
	}
	for _, name := range SortedKeys(p.Capabilities) {
		if err := ValidateName(name); err != nil {
			return fmt.Errorf("capabilities: %w", err)
		}
		if err := p.validateCapability(name, p.Capabilities[name]); err != nil {
			return fmt.Errorf("capabilities.%s: %w", name, err)
		}
	}
	if err := ValidateRoutes(p.Routes, RouteContext{
		Hosts:        p.Hosts,
		Capabilities: p.capabilityDefs(),
		Counters:     p.counterNames(),
	}); err != nil {
		return err
	}
	if p.Deny != nil {
		if err := p.Deny.Validate(); err != nil {
			return fmt.Errorf("deny: %w", err)
		}
	}
	return nil
}

func (p *Policy) validateCapability(name string, c Capability) error {
	if c.Public {
		if !c.Enabled {
			return fmt.Errorf("a public capability is always enabled")
		}
		if len(c.Constraints) > 0 {
			return fmt.Errorf("a public capability has no constraints")
		}
		return nil
	}
	if !c.Enabled {
		if len(c.Constraints) > 0 {
			return fmt.Errorf("a disabled capability carries no constraints")
		}
		return nil
	}
	for _, cname := range SortedKeys(c.Constraints) {
		con := c.Constraints[cname]
		if err := ValidateIdent(cname); err != nil {
			return fmt.Errorf("constraints: %w", err)
		}
		if err := ValidateConstraintDef(con.Kind, con.Var, con.Resolver, p.Resolvers); err != nil {
			return fmt.Errorf("constraints.%s: %w", cname, err)
		}
		if err := con.Value.Validate(con.Kind, con.Resolver != ""); err != nil {
			return fmt.Errorf("constraints.%s.value: %w", cname, err)
		}
	}
	return nil
}

func (p *Policy) capabilityDefs() map[string]CapabilityDef {
	defs := make(map[string]CapabilityDef, len(p.Capabilities))
	for name, c := range p.Capabilities {
		def := CapabilityDef{Public: c.Public, Disabled: !c.Public && !c.Enabled, ConstraintVars: map[string]string{}}
		for cname, con := range c.Constraints {
			def.ConstraintVars[cname] = con.Var
		}
		defs[name] = def
	}
	return defs
}

func (p *Policy) counterNames() map[string]bool {
	names := make(map[string]bool, len(p.Counters))
	for name := range p.Counters {
		names[name] = true
	}
	return names
}

// ValidateConstraintDef checks a constraint definition against the declared
// resolvers.
func ValidateConstraintDef(kind, varName, resolver string, resolvers map[string]Resolver) error {
	if !ValidConstraintKind(kind) {
		return fmt.Errorf("kind must be one of email, resource, value (got %q)", kind)
	}
	if err := ValidateVarName(varName); err != nil {
		return fmt.Errorf("var: %w", err)
	}
	if resolver == "" {
		return nil
	}
	if kind != KindResource {
		return fmt.Errorf("resolver is only allowed for kind resource")
	}
	if _, ok := resolvers[resolver]; !ok {
		return fmt.Errorf("resolver %q is not declared in resolvers", resolver)
	}
	return nil
}

// ValidateHosts checks governed host patterns: exact names or "*.domain",
// already normalized, without duplicates.
func ValidateHosts(hosts []string) error {
	if len(hosts) == 0 {
		return fmt.Errorf("must list at least one host")
	}
	seen := map[string]bool{}
	for _, host := range hosts {
		normalized, err := domainpattern.Normalize(host)
		if err != nil {
			return fmt.Errorf("invalid host %q: %w", host, err)
		}
		if normalized == "" || normalized != host {
			return fmt.Errorf("host %q is not normalized (want %q)", host, normalized)
		}
		if seen[host] {
			return fmt.Errorf("duplicate host %q", host)
		}
		seen[host] = true
	}
	return nil
}

// ValidateConfine checks confined domains: normalized plain domains with at
// least two labels, without duplicates, each above a governed host and
// neither equal to nor below a governed domain.
func ValidateConfine(confine []string, hosts []string) error {
	seen := map[string]bool{}
	for _, domain := range confine {
		normalized, err := domainpattern.Normalize(domain)
		if err != nil {
			return fmt.Errorf("invalid domain %q: %w", domain, err)
		}
		switch {
		case normalized == "" || normalized != domain:
			return fmt.Errorf("domain %q is not normalized (want %q)", domain, normalized)
		case strings.HasPrefix(domain, "*."):
			return fmt.Errorf("domain %q must not be a wildcard", domain)
		case !strings.Contains(domain, "."):
			return fmt.Errorf("domain %q must have at least two labels", domain)
		case seen[domain]:
			return fmt.Errorf("duplicate domain %q", domain)
		case HostGoverned(domain, hosts):
			return fmt.Errorf("domain %q is governed", domain)
		case !governsBelow(domain, hosts):
			return fmt.Errorf("domain %q has no governed host below it", domain)
		}
		seen[domain] = true
	}
	return nil
}

// governsBelow reports whether a governed host pattern governs a name
// strictly below domain.
func governsBelow(domain string, hosts []string) bool {
	for _, pattern := range hosts {
		if strings.HasSuffix(GovernedDomain(pattern), "."+domain) {
			return true
		}
	}
	return false
}

// MatchHost reports whether a normalized request host matches a route host
// pattern. An exact name matches only itself, and "*.domain" matches strict
// subdomains only.
func MatchHost(host, pattern string) bool {
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return len(host) > len(suffix)+1 && strings.HasSuffix(host, "."+suffix)
	}
	return host == pattern
}

// GovernedDomain is the domain a governed host pattern governs together with
// every name below it: the name itself, or the parent of "*.domain".
func GovernedDomain(pattern string) string {
	return strings.TrimPrefix(pattern, "*.")
}

// Governs reports whether a governed host pattern governs a normalized host.
// A filter governs every name equal to or below its patterns, the same names
// the network allowlist admits for them: "a.example.com" governs itself and
// every subdomain, "*.example.com" governs example.com and every subdomain.
func Governs(pattern string, host string) bool {
	domain := GovernedDomain(pattern)
	return host != "" && (host == domain || strings.HasSuffix(host, "."+domain))
}

// HostGoverned reports whether one of the governed patterns governs host.
func HostGoverned(host string, governed []string) bool {
	for _, pattern := range governed {
		if Governs(pattern, host) {
			return true
		}
	}
	return false
}

// HostCapabilities maps the hosts that enabled capabilities send requests to,
// route host patterns and resolver hosts, to those capabilities, sorted.
func (p *Policy) HostCapabilities() map[string][]string {
	sets := map[string]map[string]bool{}
	add := func(host, capability string) {
		if sets[host] == nil {
			sets[host] = map[string]bool{}
		}
		sets[host][capability] = true
	}
	for _, r := range p.Routes {
		if r.Action == ActionDeny || !p.Capabilities[r.Capability].Enabled {
			continue
		}
		for _, host := range r.Hosts {
			add(host, r.Capability)
		}
	}
	for name, c := range p.Capabilities {
		if !c.Enabled {
			continue
		}
		for _, con := range c.Constraints {
			if r, ok := p.Resolvers[con.Resolver]; ok {
				if host := resolverHost(r.URL); host != "" {
					add(host, name)
				}
			}
		}
	}
	hosts := make(map[string][]string, len(sets))
	for host, capabilities := range sets {
		hosts[host] = SortedKeys(capabilities)
	}
	return hosts
}

// resolverHost is the normalized host of a resolver URL template, or "" if the
// template does not parse.
func resolverHost(raw string) string {
	u, err := url.Parse(strings.Replace(raw, ResolverIDToken, "ID", 1))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// GovernedOverlap reports whether two governed host patterns govern a common
// name, which is the case when one governed domain is equal to or below the
// other.
func GovernedOverlap(a string, b string) bool {
	da, db := GovernedDomain(a), GovernedDomain(b)
	return da == db || strings.HasSuffix(da, "."+db) || strings.HasSuffix(db, "."+da)
}

// patternCovered reports whether every host a route or resolver host pattern
// matches is governed. Both pattern forms match nothing outside the domain
// they name, so that domain must be governed.
func patternCovered(pattern string, governed []string) bool {
	return HostGoverned(GovernedDomain(pattern), governed)
}

// ValidatePlaceholder checks the session placeholder.
func ValidatePlaceholder(placeholder string) error {
	if len(placeholder) < MinPlaceholderLength {
		return fmt.Errorf("must be at least %d characters", MinPlaceholderLength)
	}
	return model.ValidateSecretLiteral(placeholder, "placeholder")
}

// Validate checks the injection settings.
func (i Inject) Validate() error {
	if i.Header == "" {
		return fmt.Errorf("header is required")
	}
	if err := ValidateHeaderName(i.Header); err != nil {
		return fmt.Errorf("header: %w", err)
	}
	if i.Header != strings.ToLower(i.Header) {
		return fmt.Errorf("header %q must be lowercase", i.Header)
	}
	if i.Format == "" {
		return nil
	}
	if strings.Count(i.Format, "%s") != 1 || strings.Count(i.Format, "%") != 1 {
		return fmt.Errorf("format %q must contain %%s exactly once and no other %%", i.Format)
	}
	for _, r := range i.Format {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("format must be printable ASCII")
		}
	}
	return nil
}

// ValidateDenyHeaders checks the denied header names: lowercase, valid, and
// not the injection header.
func ValidateDenyHeaders(headers []string, injectHeader string) error {
	seen := map[string]bool{}
	for _, h := range headers {
		if err := ValidateHeaderName(h); err != nil {
			return err
		}
		if h != strings.ToLower(h) {
			return fmt.Errorf("header %q must be lowercase", h)
		}
		if h == injectHeader {
			return fmt.Errorf("header %q is the injection header", h)
		}
		if seen[h] {
			return fmt.Errorf("duplicate header %q", h)
		}
		seen[h] = true
	}
	return nil
}

// ValidateDenyParams checks the denied query parameter names.
func ValidateDenyParams(params []string) error {
	seen := map[string]bool{}
	for _, name := range params {
		if name == "" || hasControl(name) {
			return fmt.Errorf("invalid parameter name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate parameter %q", name)
		}
		seen[name] = true
	}
	return nil
}

// Validate checks a resolver against the governed hosts.
func (r Resolver) Validate(governed []string) error {
	if err := ValidateResolverURL(r.URL, governed); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if _, err := ParseJSONPath(r.Extract); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if r.MaxDepth < 1 || r.MaxDepth > MaxResolverDepth {
		return fmt.Errorf("maxDepth must be between 1 and %d", MaxResolverDepth)
	}
	if !r.Recursive && r.MaxDepth != 1 {
		return fmt.Errorf("maxDepth must be 1 when recursive is false")
	}
	return nil
}

// ValidateResolverURL checks a resolver URL template: HTTPS, a governed exact
// host, and "{id}" exactly once inside the path, where it is path-escaped.
func ValidateResolverURL(raw string, governed []string) error {
	if strings.Count(raw, ResolverIDToken) != 1 {
		return fmt.Errorf("must contain %s exactly once", ResolverIDToken)
	}
	probe := strings.Replace(raw, ResolverIDToken, "ID", 1)
	if strings.ContainsAny(probe, "{}") {
		return fmt.Errorf("must not contain braces other than %s", ResolverIDToken)
	}
	u, err := url.Parse(probe)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("must use https")
	}
	if u.User != nil || u.Fragment != "" || u.Port() != "" {
		return fmt.Errorf("must not contain user info, a port, or a fragment")
	}
	if !HostGoverned(u.Hostname(), governed) {
		return fmt.Errorf("host %q is not a governed host", u.Hostname())
	}
	pathEnd := strings.IndexAny(raw, "?#")
	if pathEnd < 0 {
		pathEnd = len(raw)
	}
	if !strings.Contains(raw[:pathEnd], ResolverIDToken) {
		return fmt.Errorf("%s must be in the path, not the query", ResolverIDToken)
	}
	if _, err := ParseRequestPath(u.EscapedPath()); err != nil {
		return fmt.Errorf("path: %w", err)
	}
	return nil
}

// Validate checks the deny template. A JSON content type must render valid
// JSON.
func (d DenyTemplate) Validate() error {
	if d.ContentType == "" {
		return fmt.Errorf("contentType is required")
	}
	if hasControl(d.ContentType) {
		return fmt.Errorf("contentType contains control characters")
	}
	if d.Body == "" {
		return fmt.Errorf("body is required")
	}
	if strings.Contains(strings.ToLower(d.ContentType), "json") {
		rendered := d.Render(403, StatusPermissionDenied, "sample \"message\"")
		if !json.Valid(rendered) {
			return fmt.Errorf("body does not render valid JSON for content type %q", d.ContentType)
		}
	}
	return nil
}

// Deny status names for {{status}}.
const (
	StatusPermissionDenied  = "PERMISSION_DENIED"
	StatusResourceExhausted = "RESOURCE_EXHAUSTED"
	StatusUnavailable       = "UNAVAILABLE"
)

// Render fills the template.
func (d DenyTemplate) Render(code int, status string, message string) []byte {
	messageLiteral, _ := json.Marshal(message)
	statusLiteral, _ := json.Marshal(status)
	return []byte(strings.NewReplacer(
		"{{message}}", string(messageLiteral),
		"{{status}}", string(statusLiteral),
		"{{code}}", fmt.Sprint(code),
	).Replace(d.Body))
}
