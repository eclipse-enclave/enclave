// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package policy

import (
	"maps"
	"strings"

	"enclave/internal/model"
	"enclave/internal/network"
)

// SessionNetworkInputs are a session's contributions to its network policy on
// top of the built-in allowlist and the policy files. Session startup and the
// `enclave network` preview both resolve through them, so the preview shows
// the allowlist a run with the same options enforces, except release hosts
// selected at session start, which previews cannot know.
type SessionNetworkInputs struct {
	Profile         model.Profile
	Features        []model.Extension
	AllowDomains    []string
	AllowAllNetwork bool
	// ReleaseHostOverrides maps a secret id to hosts selected at session start
	// through serviceAuth.hostsFromCredential.
	ReleaseHostOverrides map[string][]string
}

// NewSessionNetworkInputs returns the network inputs of a session started with
// the given tool, features, and run options.
func NewSessionNetworkInputs(profile model.Profile, features []model.Extension, run model.RunOptions) SessionNetworkInputs {
	return SessionNetworkInputs{
		Profile:         profile,
		Features:        features,
		AllowDomains:    run.AllowDomains,
		AllowAllNetwork: run.AllowAllNetwork,
	}
}

// SpecDomains unions network.allowedDomains/deniedDomains across the tool spec
// and the enabled features, plus every declared HTTP release host
// (serviceDomains/serviceAuth): a host the proxy injects credentials for must
// be resolvable, or the release rule is dead weight.
func (in SessionNetworkInputs) SpecDomains() (allowed []string, denied []string) {
	allowed = append([]string(nil), in.Profile.AllowedDomains...)
	denied = append([]string(nil), in.Profile.DeniedDomains...)
	allowed = append(allowed, in.releaseHosts(in.Profile.Secrets)...)
	for _, feature := range in.Features {
		allowed = append(allowed, feature.AllowedDomains...)
		denied = append(denied, feature.DeniedDomains...)
		allowed = append(allowed, in.releaseHosts(feature.Secrets)...)
	}
	return allowed, denied
}

// Merge merges cfg with the session's spec domains, appends the AllowDomains
// not already allowed, and applies AllowAllNetwork.
func (in SessionNetworkInputs) Merge(cfg network.MergeConfig) network.EffectivePolicy {
	cfg.SpecAllowedDomains, cfg.SpecDeniedDomains = in.SpecDomains()
	effective := network.Merge(cfg)
	allowed := make(map[string]bool, len(effective.Domains))
	for _, domain := range effective.Domains {
		allowed[domain] = true
	}
	for _, domain := range in.AllowDomains {
		key := strings.ToLower(domain)
		if !allowed[key] {
			allowed[key] = true
			effective.Domains = append(effective.Domains, key)
		}
	}
	if in.AllowAllNetwork {
		effective.Mode = model.NetworkModeUnrestricted
		effective.ModeSource = "allow-all-network"
	}
	return effective
}

// releaseHosts is model.ReleaseHosts plus any host selected through
// serviceAuth.hostsFromCredential, so that host becomes resolvable exactly like
// a declared one. The override narrows where the token is released, not what
// the session can reach: dropping the declared hosts here would make e.g.
// gitlab.com unresolvable for tools whose allowlist has no other source for it.
// The secrets are left untouched.
func (in SessionNetworkInputs) releaseHosts(secrets map[string]model.SecretConfig) []string {
	if len(in.ReleaseHostOverrides) == 0 {
		return model.ReleaseHosts(secrets)
	}
	effective := maps.Clone(secrets)
	for id, hosts := range in.ReleaseHostOverrides {
		sc, ok := effective[id]
		if !ok || sc.Release == nil || sc.Release.HTTP == nil {
			continue
		}
		release := *sc.Release
		http := *release.HTTP
		http.Hosts = append(append([]string{}, http.Hosts...), hosts...)
		release.HTTP = &http
		sc.Release = &release
		effective[id] = sc
	}
	return model.ReleaseHosts(effective)
}
