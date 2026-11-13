// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package spec

import (
	"fmt"

	"enclave/internal/filter"
)

// Compile builds the policy the gateway enforces from the filter, a resolved
// preset, and the session placeholder. The result is validated.
func Compile(f *Filter, p *Preset, placeholder string) (*filter.Policy, error) {
	if f == nil || p == nil {
		return nil, fmt.Errorf("compile filter: filter and preset are required")
	}
	pol := &filter.Policy{
		SchemaVersion:  filter.PolicySchemaVersion,
		Kind:           filter.PolicyKind,
		Filter:         f.Name,
		Preset:         p.Name,
		CredentialType: f.Credential.Type,
		Hosts:          append([]string(nil), f.Hosts...),
		Confine:        append([]string(nil), f.Confine...),
		Placeholder:    placeholder,
		Inject:         f.Credential.Inject,
		DenyHeaders:    append([]string(nil), f.Credential.DenyHeaders...),
		DenyParams:     append([]string(nil), f.Credential.DenyParams...),
		Capabilities:   map[string]filter.Capability{},
		Counters:       map[string]filter.Counter{},
		Resolvers:      map[string]filter.Resolver{},
		JSONKeys:       f.JSONKeys,
		Routes:         append([]filter.Route(nil), f.Routes...),
	}
	if f.Deny != nil {
		deny := *f.Deny
		pol.Deny = &deny
	}
	for name, r := range f.Resolvers {
		pol.Resolvers[name] = r.Resolver
	}
	for name, c := range f.Counters {
		if quota, ok := p.Quotas[name]; ok {
			q := quota
			c.Quota = &q
		}
		pol.Counters[name] = c
	}
	for name, def := range f.Capabilities {
		c := filter.Capability{Description: def.Description, Public: def.Public}
		values, enabled := p.Capabilities[name]
		switch {
		case def.Public:
			c.Enabled = true
		case enabled:
			c.Enabled = true
			for cname, con := range def.Constraints {
				value, ok := values[cname]
				if !ok {
					return nil, fmt.Errorf("compile filter %s: preset %s: capability %s has no value for constraint %s", f.Name, p.Name, name, cname)
				}
				if c.Constraints == nil {
					c.Constraints = map[string]filter.Constraint{}
				}
				c.Constraints[cname] = filter.Constraint{Kind: con.Kind, Var: con.Var, Resolver: con.Resolver, Value: value}
			}
		}
		pol.Capabilities[name] = c
	}
	for capName := range p.Capabilities {
		if _, ok := f.Capabilities[capName]; !ok {
			return nil, fmt.Errorf("compile filter %s: preset %s enables unknown capability %s", f.Name, p.Name, capName)
		}
	}
	if err := pol.Validate(); err != nil {
		return nil, err
	}
	return pol, nil
}

// NewOAuth2Credential builds the gateway credential payload for a grant of
// this filter.
func (f *Filter) NewOAuth2Credential(clientID, clientSecret, refreshToken, grant string, scopes []string) (*filter.Credential, error) {
	if f.Credential.OAuth2 == nil {
		return nil, fmt.Errorf("filter %s does not use an oauth2 credential", f.Name)
	}
	cred := filter.NewOAuth2Credential(f.Name, filter.OAuth2Credential{
		TokenURL:     f.Credential.OAuth2.TokenURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RefreshToken: refreshToken,
		Grant:        grant,
		Scopes:       append([]string(nil), scopes...),
	})
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	return cred, nil
}

// NewStaticCredential builds the gateway credential payload for a static
// secret of this filter.
func (f *Filter) NewStaticCredential(value string) (*filter.Credential, error) {
	if f.Credential.Static == nil {
		return nil, fmt.Errorf("filter %s does not use a static credential", f.Name)
	}
	cred := filter.NewStaticCredential(f.Name, value)
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	return cred, nil
}
