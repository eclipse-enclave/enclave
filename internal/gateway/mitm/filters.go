// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"enclave/internal/filter"
	"enclave/internal/filter/engine"
)

// filterClientTimeout bounds one token refresh or resolver lookup.
const filterClientTimeout = 30 * time.Second

// NewFilterClient returns the client request filters use for token refreshes
// and resolver lookups. It verifies TLS against the system roots, bypasses
// any proxy, and never follows a redirect, so a credential cannot be carried
// to another host.
func NewFilterClient() *http.Client {
	return &http.Client{
		Transport: newUpstreamTransport(),
		Timeout:   filterClientTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// LoadFilters builds the engines of the filters in dir, which holds a
// "<name>.policy.json" and a "<name>.credential.json" for each filter and
// nothing else. Any error fails the whole load, so the gateway never runs with
// a partial policy. Errors never quote credential material.
func LoadFilters(dir string, client *http.Client) (*engine.Set, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read filter directory: %w", err)
	}
	type pair struct{ policy, credential string }
	pairs := map[string]*pair{}
	for _, entry := range entries {
		name, credential, ok := filter.ParseBundleFileName(entry.Name())
		if !ok || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("unexpected entry %q in filter directory", entry.Name())
		}
		p := pairs[name]
		if p == nil {
			p = &pair{}
			pairs[name] = p
		}
		path := filepath.Join(dir, entry.Name())
		if credential {
			p.credential = path
		} else {
			p.policy = path
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("filter directory %s holds no filters", dir)
	}
	names := make([]string, 0, len(pairs))
	for name := range pairs {
		names = append(names, name)
	}
	sort.Strings(names)
	engines := make([]*engine.Engine, 0, len(names))
	for _, name := range names {
		p := pairs[name]
		if p.policy == "" || p.credential == "" {
			return nil, fmt.Errorf("filter %s needs both %s and %s", name, filter.PolicyFileName(name), filter.CredentialFileName(name))
		}
		policy, err := filter.LoadPolicy(p.policy)
		if err != nil {
			return nil, err
		}
		if policy.Filter != name {
			return nil, fmt.Errorf("%s holds the policy of filter %s", filter.PolicyFileName(name), policy.Filter)
		}
		cred, err := filter.LoadCredential(p.credential)
		if err != nil {
			return nil, err
		}
		e, err := engine.New(engine.Options{Policy: policy, Credential: cred, Client: client})
		if err != nil {
			return nil, err
		}
		engines = append(engines, e)
	}
	return engine.NewSet(engines...)
}
