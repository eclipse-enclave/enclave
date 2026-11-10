// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"time"

	"enclave/internal/domainpattern"
	"enclave/internal/model"
	"enclave/internal/placeholderstore"
	"enclave/internal/util"
)

type Store struct {
	config                 Config
	provider               model.ProviderConfig
	routes                 []*Store
	fields                 []model.AuthSecretField
	dir, lockPath, session string
	file                   model.AuthFileSecret
	transport              http.RoundTripper
	now                    func() time.Time
	timeout                time.Duration
	env                    []model.SecretReleaseEntry
}

func New(config Config, dir, lockPath, session string, env []model.SecretReleaseEntry) (*Store, error) {
	store := &Store{}
	for _, provider := range config.Providers {
		if provider.TokenEndpoint == nil {
			continue
		}
		route, err := newProvider(config, provider, dir, lockPath, session, env)
		if err != nil {
			return nil, err
		}
		store.routes = append(store.routes, route)
	}
	if len(store.routes) == 0 {
		return nil, fmt.Errorf("protected auth requires native exchange declarations")
	}
	store.routes[0].routes = store.routes
	return store.routes[0], nil
}

func newProvider(config Config, provider model.ProviderConfig, dir, lockPath, session string, env []model.SecretReleaseEntry) (*Store, error) {
	if dir == "" || lockPath == "" || session == "" || provider.TokenEndpoint == nil {
		return nil, fmt.Errorf("protected auth requires a native store, lock and exchange declaration")
	}
	var file model.AuthFileSecret
	for _, owner := range config.Providers {
		for _, candidate := range owner.AuthSecrets {
			if !candidate.CheckOnly && (candidate.Codec == "claude-credentials" || candidate.Codec == "codex-auth" || candidate.Codec == "provider-map" || candidate.Codec == "copilot-config") {
				if file.File != "" {
					return nil, fmt.Errorf("ambiguous native exchange file")
				}
				file = candidate
			}
		}
	}
	if file.File == "" {
		return nil, fmt.Errorf("protected exchange requires a supported native codec")
	}
	if provider.TokenEndpoint.Fields["refresh"] == "" && file.Codec != "copilot-config" {
		return nil, fmt.Errorf("native exchange requires a refresh response field")
	}
	kinds := map[string]bool{}
	var fields []model.AuthSecretField
	for _, field := range file.Fields {
		expected := map[string]string{"access": "/claudeAiOauth/accessToken", "refresh": "/claudeAiOauth/refreshToken"}[field.Kind]
		if file.Codec == "codex-auth" {
			expected = map[string]string{"access": "/tokens/access_token", "refresh": "/tokens/refresh_token", "id": "/tokens/id_token", "apiKey": "/OPENAI_API_KEY"}[field.Kind] // #nosec G101 -- native JSON pointers, not credential values.
		}
		if file.Codec == "provider-map" {
			if field.Kind == "apiKey" {
				parts, _ := model.AuthPointerSegments(field.Pointer)
				if len(parts) == 2 && parts[1] == "key" {
					continue
				}
			}
			if field.Kind == "access" || field.Kind == "refresh" {
				parts, _ := model.AuthPointerSegments(field.Pointer)
				if len(parts) == 2 && parts[0] != provider.Name {
					continue
				}
				expected = "/" + model.EscapeAuthPointer(provider.Name) + "/" + field.Kind
			}
		}
		if file.Codec == "copilot-config" && field.Kind == "access" {
			expected = "/authTokens/*/token"
		}
		if expected == "" || field.Pointer != expected {
			return nil, fmt.Errorf("unsupported native exchange fields")
		}
		kinds[field.Kind] = true
		fields = append(fields, field)
	}
	if !kinds["access"] || (!kinds["refresh"] && file.Codec != "copilot-config") {
		return nil, fmt.Errorf("missing native exchange field")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	return &Store{config: config, provider: provider, fields: fields, dir: dir, lockPath: lockPath, session: session, file: file, transport: transport, now: time.Now, timeout: 30 * time.Second, env: env}, nil
}

func (s *Store) lock(ctx context.Context) (func(), error) {
	for {
		release, ok, err := util.TryAcquireFileLock(s.lockPath)
		if err != nil || ok {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *Store) native() (placeholderstore.NativeFile, error) {
	native, err := placeholderstore.ReadNative(s.dir, s.file)
	if errors.Is(err, fs.ErrNotExist) {
		return placeholderstore.NativeFile{Document: map[string]any{}, Values: map[string]string{}}, nil
	}
	return native, err
}

func isMissing(err error) bool { return errors.Is(err, fs.ErrNotExist) }

func bindingFor(bindings placeholderstore.Bindings, provider, file string, field model.AuthSecretField) (placeholderstore.Binding, bool) {
	for _, b := range bindings.Bindings {
		if b.Provider == provider && b.File == file && b.Pointer == field.Pointer && b.Kind == field.Kind {
			return b, true
		}
	}
	return placeholderstore.Binding{}, false
}

// Releases reads native values on each marked request. It never persists a
// second grant database. Only declared access fields authorize HTTP releases.
func (s *Store) Releases(req *http.Request) ([]model.SecretReleaseEntry, error) {
	var rules []model.SecretReleaseEntry
	for _, route := range s.routes {
		entries, err := route.releases(req)
		if err != nil {
			return nil, err
		}
		rules = append(rules, entries...)
	}
	return rules, nil
}

func (s *Store) releases(req *http.Request) ([]model.SecretReleaseEntry, error) {
	headers := req.Header
	if !MarkedHeaders(headers) {
		return nil, nil
	}
	if s.provider.TokenEndpoint.Host == "github.com" && endpointHost(req) == "api.github.com" && !copilotUserRequest(req) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(req.Context(), s.timeout)
	defer cancel()
	release, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	bindings, err := placeholderstore.LoadBindings(s.dir)
	if err != nil {
		return nil, err
	}
	var rules []model.SecretReleaseEntry
	native, err := placeholderstore.ReadNative(s.dir, s.file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, field := range s.fields {
		if field.Kind != "access" {
			continue
		}
		for _, b := range bindings.Bindings {
			if b.Provider != s.provider.Name || b.File != s.file.File || b.Kind != field.Kind || !model.AuthPointerMatches(field.Pointer, b.Pointer) {
				continue
			}
			value := native.Values[b.Pointer]
			if value == "" || strings.Contains(value, model.SecretPlaceholderMarker) {
				continue
			}
			secret := s.config.Secrets[field.Release]
			if secret.Release == nil || secret.Release.HTTP == nil {
				return nil, fmt.Errorf("missing native HTTP release")
			}
			h := secret.Release.HTTP
			rule := model.SecretReleaseEntry{SecretID: field.Release, Value: value, Hosts: h.Hosts, Header: h.Header, Format: h.Format}
			rule.Placeholder = b.Placeholder
			if b.Shape == "jwt" {
				for _, values := range headers {
					for _, header := range values {
						for _, candidate := range strings.Fields(header) {
							if b.Matches(candidate) {
								rule.Placeholder = candidate
								rules = append(rules, rule)
							}
						}
					}
				}
			} else {
				rules = append(rules, rule)
			}
		}
	}
	return rules, nil
}

// RequiresMITM includes exchange and deny endpoints even before an identity has
// credentials, so a first login cannot bypass mediation through a TLS tunnel.
func (s *Store) RequiresMITM(host string) bool {
	return slices.ContainsFunc(s.routes, func(route *Store) bool { return route.requiresMITM(host) })
}

func (s *Store) requiresMITM(host string) bool {
	endpoint := s.provider.TokenEndpoint
	if host == endpoint.Host {
		return true
	}
	for _, denied := range endpoint.Deny {
		if host == denied.Host {
			return true
		}
	}
	for _, secret := range s.config.Secrets {
		if secret.Release != nil && secret.Release.HTTP != nil {
			for _, pattern := range secret.Release.HTTP.Hosts {
				if domainpattern.MatchNormalizedHost(host, pattern) {
					return true
				}
			}
		}
	}
	return false
}

func MarkedHeaders(headers http.Header) bool {
	for key, values := range headers {
		if strings.Contains(strings.ToLower(key), strings.ToLower(model.SecretPlaceholderMarker)) {
			return true
		}
		for _, value := range values {
			if strings.Contains(value, model.SecretPlaceholderMarker) {
				return true
			}
		}
	}
	return false
}
