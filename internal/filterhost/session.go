// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"enclave/internal/auth"
	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/filter"
	"enclave/internal/filter/spec"
	"enclave/internal/model"
)

// Selection is one entry of the filters option. An empty Preset selects the
// filter's defaultPreset.
type Selection struct {
	Name   string
	Preset string
}

func (s Selection) String() string {
	if s.Preset == "" {
		return s.Name
	}
	return s.Name + ":" + s.Preset
}

// ParseSelection turns the merged filters option into selections. A leading
// + is dropped, a -name removes the filter whatever its preset and wherever
// it stands, as -name does in a features selection, and none selects nothing.
// The same filter added twice is an error.
func ParseSelection(entries []string) ([]Selection, error) {
	var out []Selection
	seen := map[string]bool{}
	removed := map[string]bool{}
	sawNone := false
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		switch {
		case entry == "":
			continue
		case strings.EqualFold(entry, "none"):
			sawNone = true
			continue
		}
		if err := config.ValidateFilterEntry(entry); err != nil {
			return nil, fmt.Errorf("filters entry %q: %w", entry, err)
		}
		if rest, ok := strings.CutPrefix(entry, "-"); ok {
			name, _, _ := strings.Cut(strings.TrimSpace(rest), ":")
			removed[name] = true
			continue
		}
		entry = strings.TrimSpace(strings.TrimPrefix(entry, "+"))
		name, preset, _ := strings.Cut(entry, ":")
		if seen[name] {
			return nil, fmt.Errorf("filter %s is selected twice", name)
		}
		seen[name] = true
		out = append(out, Selection{Name: name, Preset: preset})
	}
	if sawNone && len(out) > 0 {
		return nil, fmt.Errorf("filters value none cannot be combined with other filters")
	}
	return slices.DeleteFunc(out, func(sel Selection) bool { return removed[sel.Name] }), nil
}

// SessionFilter is a filter resolved for one session: the preset, the
// credential, the session placeholder, and the compiled policy. The
// credential is unexported and String redacts it.
type SessionFilter struct {
	Filter *spec.Filter
	Preset *spec.Preset
	// Grant is the selected grant id, "" for a static credential.
	Grant       string
	Placeholder string
	Policy      *filter.Policy
	credential  *filter.Credential
}

// String names the filter without credential material.
func (s SessionFilter) String() string { return "filter " + s.Label() }

// GoString redacts like String.
func (s SessionFilter) GoString() string { return s.String() }

// Label is the name:preset form recorded as provenance.
func (s SessionFilter) Label() string { return s.Filter.Name + ":" + s.Preset.Name }

// StartupLine is the line a session start prints for the filter.
func (s SessionFilter) StartupLine() string {
	if s.Grant != "" {
		return fmt.Sprintf("filter %s: preset %s, grant %s", s.Filter.Name, s.Preset.Name, s.Grant)
	}
	return fmt.Sprintf("filter %s: preset %s, static credential", s.Filter.Name, s.Preset.Name)
}

// Hosts are the governed hosts.
func (s SessionFilter) Hosts() []string { return append([]string(nil), s.Filter.Hosts...) }

// Confine are the confined domains.
func (s SessionFilter) Confine() []string { return append([]string(nil), s.Filter.Confine...) }

// Env maps each container env var of the filter to the placeholder.
func (s SessionFilter) Env() map[string]string {
	env := make(map[string]string, len(s.Filter.Credential.Env))
	for _, name := range s.Filter.Credential.Env {
		env[name] = s.Placeholder
	}
	return env
}

// Bundle is what the gateway receives: the policy and the credential payload.
// It must never reach the tool container.
func (s SessionFilter) Bundle() (backend.FilterBundle, error) {
	policy, err := s.Policy.Marshal()
	if err != nil {
		return backend.FilterBundle{}, fmt.Errorf("encode filter %s policy: %w", s.Filter.Name, err)
	}
	credential, err := json.Marshal(s.credential) // #nosec G117 -- the payload is for the gateway alone.
	if err != nil {
		return backend.FilterBundle{}, fmt.Errorf("encode filter %s credential", s.Filter.Name)
	}
	return backend.FilterBundle{
		Name:       s.Filter.Name,
		Policy:     policy,
		Credential: credential,
		Hosts:      append([]string(nil), s.Policy.Hosts...),
		Confine:    append([]string(nil), s.Policy.Confine...),
	}, nil
}

// SummaryJSON is the read-only, non-secret policy summary for the container.
func (s SessionFilter) SummaryJSON() ([]byte, error) {
	data, err := json.MarshalIndent(s.Policy.Summary(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ResolveRequest is what resolving a session's filters needs from the host.
type ResolveRequest struct {
	Paths   model.Paths
	Home    string
	Entries []string
	// Secrets are the sources static credentials resolve from, the same as
	// for credentials.sources. An empty Secrets.Home defaults to the
	// request's Home.
	Secrets auth.SecretSources
	// OnGrantProblem receives each grant file that does not load. Nil drops
	// them.
	OnGrantProblem func(problem string)
}

// Resolve loads each selected filter, resolves its preset and credential,
// generates the session placeholder, and compiles the policy. It returns nil
// when nothing is selected.
func Resolve(req ResolveRequest) ([]SessionFilter, error) {
	selections, err := ParseSelection(req.Entries)
	if err != nil {
		return nil, err
	}
	placeholders := auth.NewPlaceholderResolver()
	var out []SessionFilter
	for _, sel := range selections {
		sf, err := resolveOne(req, sel, placeholders)
		if err != nil {
			return nil, err
		}
		out = append(out, sf)
	}
	return out, nil
}

// ResolvePreview loads each selected filter and resolves its preset without
// touching credentials, for previews such as `enclave network print`.
func ResolvePreview(paths model.Paths, home string, entries []string) ([]SessionFilter, error) {
	selections, err := ParseSelection(entries)
	if err != nil {
		return nil, err
	}
	var out []SessionFilter
	for _, sel := range selections {
		f, p, err := loadSelection(paths, home, sel)
		if err != nil {
			return nil, err
		}
		out = append(out, SessionFilter{Filter: f, Preset: p})
	}
	return out, nil
}

func loadSelection(paths model.Paths, home string, sel Selection) (*spec.Filter, *spec.Preset, error) {
	f, err := Load(paths, sel.Name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, unknownFilterError(paths, sel.Name)
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := ResolvePreset(f, home, sel.Preset)
	if err != nil {
		return nil, nil, err
	}
	return f, p, nil
}

func unknownFilterError(paths model.Paths, name string) error {
	names, err := config.ListFilterNames(paths)
	if err != nil || len(names) == 0 {
		return fmt.Errorf("unknown filter %s (install one with `%s filters add <source>`)", name, model.AppName)
	}
	return fmt.Errorf("unknown filter %s (installed: %s)", name, strings.Join(names, ", "))
}

func resolveOne(req ResolveRequest, sel Selection, placeholders *auth.PlaceholderResolver) (SessionFilter, error) {
	f, p, err := loadSelection(req.Paths, req.Home, sel)
	if err != nil {
		return SessionFilter{}, err
	}
	sf := SessionFilter{Filter: f, Preset: p}
	switch {
	case f.Credential.OAuth2 != nil:
		required := f.RequiredScopes(p)
		grant, ok, problems, err := SelectGrant(f, NewStore(req.Home, f.Name), required)
		if err != nil {
			return SessionFilter{}, fmt.Errorf("filter %s: read grants: %w", f.Name, err)
		}
		reportGrantProblems(problems, req.OnGrantProblem)
		if !ok {
			return SessionFilter{}, fmt.Errorf("filter %s: no grant covers preset %s (run: %s)", f.Name, p.Name, LoginCommand(f.Name, p.Name))
		}
		cred, err := f.NewOAuth2Credential(grant.ClientID, grant.ClientSecret, grant.RefreshToken, grant.ID, grant.Scopes)
		if err != nil {
			return SessionFilter{}, err
		}
		sf.Grant = grant.ID
		sf.credential = cred
	case f.Credential.Static != nil:
		value, err := resolveStatic(req, f)
		if err != nil {
			return SessionFilter{}, err
		}
		cred, err := f.NewStaticCredential(value)
		if err != nil {
			return SessionFilter{}, err
		}
		sf.credential = cred
	default:
		return SessionFilter{}, fmt.Errorf("filter %s declares no credential", f.Name)
	}
	placeholder, err := placeholders.ResolvePlaceholder("filter:"+f.Name, f.Credential.Placeholder)
	if err != nil {
		return SessionFilter{}, fmt.Errorf("filter %s: placeholder: %w", f.Name, err)
	}
	sf.Placeholder = placeholder
	pol, err := spec.Compile(f, p, placeholder)
	if err != nil {
		return SessionFilter{}, err
	}
	if err := pol.CheckCredential(sf.credential); err != nil {
		return SessionFilter{}, err
	}
	sf.Policy = pol
	return sf, nil
}

// resolveStatic reads a static credential the way credentials.sources are
// read: the env aliases from the host environment, Enclave's secrets files,
// and the env store, and the file source, in the filter's priority order.
func resolveStatic(req ResolveRequest, f *spec.Filter) (string, error) {
	s := f.Credential.Static
	sources := req.Secrets
	if sources.Home == "" {
		sources.Home = req.Home
	}
	value, _, found, err := sources.Resolve(auth.SecretRef{ID: "filter " + f.Name, EnvVars: s.Env, File: s.File, Priority: s.Priority})
	if err != nil {
		return "", err
	}
	if value = strings.TrimSpace(value); found && value != "" {
		return value, nil
	}
	var where []string
	if len(s.Env) > 0 {
		where = append(where, "set "+strings.Join(s.Env, " or ")+" in the host environment or Enclave's secrets")
	}
	if s.File != nil {
		where = append(where, "create "+s.File.Path)
	}
	return "", fmt.Errorf("filter %s: no static credential found (%s)", f.Name, strings.Join(where, ", or "))
}

// Labels returns the name:preset labels of filters.
func Labels(filters []SessionFilter) []string {
	labels := make([]string, 0, len(filters))
	for _, sf := range filters {
		labels = append(labels, sf.Label())
	}
	return labels
}

// Owner names a party that claims hosts or env vars, such as "tool claude".
type Owner = string

// SessionClaims are the hosts and container env vars a session's tool,
// features, and options already claim.
type SessionClaims struct {
	// ReleaseHosts are the serviceAuth release hosts. A bare host covers its
	// subdomains too.
	ReleaseHosts map[Owner][]string
	// Env are container env var names.
	Env map[string]Owner
}

// CheckCollisions fails when two filters govern one host, a governed host is
// a credential release host, or a filter env var is already claimed.
func CheckCollisions(filters []SessionFilter, claims SessionClaims) error {
	envOwner := map[string]Owner{}
	for name, owner := range claims.Env {
		envOwner[name] = owner
	}
	for i, sf := range filters {
		for _, other := range filters[:i] {
			if a, b, ok := overlappingHosts(other.Filter.Hosts, sf.Filter.Hosts, false); ok {
				return fmt.Errorf("filters %s and %s both govern %s", other.Filter.Name, sf.Filter.Name, describeOverlap(a, b))
			}
		}
		for _, owner := range sortedOwners(claims.ReleaseHosts) {
			if a, b, ok := overlappingHosts(sf.Filter.Hosts, claims.ReleaseHosts[owner], true); ok {
				return fmt.Errorf("filter %s governs %s, where %s releases a credential", sf.Filter.Name, describeOverlap(a, b), owner)
			}
		}
		for _, name := range sf.Filter.Credential.Env {
			if owner, taken := envOwner[name]; taken {
				return fmt.Errorf("filter %s sets env %s, which %s also sets", sf.Filter.Name, name, owner)
			}
			envOwner[name] = "filter " + sf.Filter.Name
		}
	}
	return nil
}

// ReleaseHostCollision reports the first governed host of filters that a
// credential release rule for hosts also covers. exact rules match only the
// named hosts.
func ReleaseHostCollision(filters []SessionFilter, hosts []string, exact bool) (string, string, bool) {
	for _, sf := range filters {
		for _, governed := range sf.Filter.Hosts {
			for _, host := range hosts {
				if setsOverlap(filterHostSet(governed), releaseHostSet(host, exact)) {
					return sf.Filter.Name, governed, true
				}
			}
		}
	}
	return "", "", false
}

func sortedOwners(m map[Owner][]string) []Owner {
	owners := make([]Owner, 0, len(m))
	for owner := range m {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	return owners
}

func describeOverlap(a string, b string) string {
	if a == b {
		return a
	}
	return a + " and " + b
}

// hostSet is a base host, whether the set includes it, and whether it
// includes every strict subdomain of it.
type hostSet struct {
	base string
	self bool
	sub  bool
}

// filterHostSet reads a governed host. Both an exact name and *.name govern
// the name and every subdomain of it (filter.Governs).
func filterHostSet(host string) hostSet {
	return hostSet{base: filter.GovernedDomain(strings.ToLower(host)), self: true, sub: true}
}

// releaseHostSet reads a release host, which also covers its subdomains
// unless the rule matches exactly.
func releaseHostSet(host string, exact bool) hostSet {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if base, ok := strings.CutPrefix(host, "*."); ok {
		return hostSet{base: base, sub: true}
	}
	return hostSet{base: host, self: true, sub: !exact}
}

func setsOverlap(a hostSet, b hostSet) bool {
	switch {
	case a.base == b.base:
		return (a.self && b.self) || (a.sub && b.sub)
	case strings.HasSuffix(a.base, "."+b.base):
		return b.sub
	case strings.HasSuffix(b.base, "."+a.base):
		return a.sub
	default:
		return false
	}
}

// overlappingHosts finds a governed host in a that overlaps a host in b. b
// holds release hosts when release is set, governed hosts otherwise.
func overlappingHosts(a []string, b []string, release bool) (string, string, bool) {
	for _, x := range a {
		for _, y := range b {
			other := filterHostSet(y)
			if release {
				other = releaseHostSet(y, false)
			}
			if setsOverlap(filterHostSet(x), other) {
				return x, y, true
			}
		}
	}
	return "", "", false
}
