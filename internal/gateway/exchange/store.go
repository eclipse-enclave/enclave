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
	"strings"
	"sync/atomic"
	"time"

	"enclave/internal/domainpattern"
	"enclave/internal/model"
	"enclave/internal/placeholderstore"
	"enclave/internal/util"
)

type Store struct {
	config                 Config
	dir, lockPath, session string
	file                   model.AuthFileSecret
	transport              http.RoundTripper
	now                    func() time.Time
	timeout                time.Duration
	env                    []model.SecretReleaseEntry
	seenFingerprint        atomic.Value // stores only a fingerprint string
}

func New(config Config, dir, lockPath, session string, env []model.SecretReleaseEntry) (*Store, error) {
	if dir == "" || lockPath == "" || session == "" || config.Provider.TokenEndpoint == nil {
		return nil, fmt.Errorf("protected auth requires a native store, lock and exchange declaration")
	}
	var file model.AuthFileSecret
	for _, candidate := range config.Provider.AuthSecrets {
		if candidate.Codec == "claude-credentials" {
			if file.File != "" {
				return nil, fmt.Errorf("ambiguous native exchange file")
			}
			file = candidate
		}
	}
	if file.File == "" {
		return nil, fmt.Errorf("protected exchange currently requires Claude credentials")
	}
	endpoint := config.Provider.TokenEndpoint
	host, hostErr := domainpattern.NormalizeHost(endpoint.Host)
	if config.Provider.Name == "" || hostErr != nil || host != endpoint.Host || endpoint.Path == "" || !strings.HasPrefix(endpoint.Path, "/") || strings.ContainsAny(endpoint.Path, "?#") || endpoint.Fields["access"] == "" || endpoint.Fields["refresh"] == "" || endpoint.Fields["id"] != "" {
		return nil, fmt.Errorf("unsupported Claude exchange declaration")
	}
	kinds := map[string]bool{}
	for _, field := range file.Fields {
		expected := "/claudeAiOauth/" + map[string]string{"access": "accessToken", "refresh": "refreshToken"}[field.Kind]
		if (field.Kind != "access" && field.Kind != "refresh") || field.Pointer != expected || kinds[field.Kind] {
			return nil, fmt.Errorf("unsupported Claude exchange fields")
		}
		kinds[field.Kind] = true
	}
	if !kinds["access"] || !kinds["refresh"] {
		return nil, fmt.Errorf("missing Claude exchange field")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	return &Store{config: config, dir: dir, lockPath: lockPath, session: session, file: file, transport: transport, now: time.Now, timeout: 30 * time.Second, env: env}, nil
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
// second grant database and never allows refresh or id fields into HTTP headers.
func (s *Store) Releases(ctx context.Context, headers http.Header) ([]model.SecretReleaseEntry, error) {
	marked := false
	for key, values := range headers {
		if strings.Contains(strings.ToLower(key), strings.ToLower(model.SecretPlaceholderMarker)) {
			marked = true
		}
		for _, v := range values {
			if strings.Contains(v, model.SecretPlaceholderMarker) {
				marked = true
			}
		}
	}
	if !marked {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
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
	for _, file := range s.config.Provider.AuthSecrets {
		native, err := placeholderstore.ReadNative(s.dir, file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, field := range file.Fields {
			if field.Kind != "access" && field.Kind != "apiKey" {
				continue
			}
			b, ok := bindingFor(bindings, s.config.Provider.Name, file.File, field)
			if !ok {
				continue
			}
			value := native.Values[field.Pointer]
			if value == "" || strings.Contains(value, model.SecretPlaceholderMarker) {
				continue
			}
			var rule model.SecretReleaseEntry
			if field.Kind == "apiKey" {
				for _, env := range s.env {
					if env.SecretID == field.Release {
						rule = env
						break
					}
				}
				if rule.Value == "" || rule.Placeholder != b.Placeholder {
					continue
				}
			} else {
				secret := s.config.Secrets[field.Release]
				if secret.Release == nil || secret.Release.HTTP == nil {
					return nil, fmt.Errorf("missing native HTTP release")
				}
				h := secret.Release.HTTP
				rule = model.SecretReleaseEntry{SecretID: field.Release, Value: value, Hosts: h.Hosts, Header: h.Header, Format: h.Format}
			}
			rule.Placeholder = b.Placeholder
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

// RequiresMITM includes exchange and deny endpoints even before an identity has
// credentials, so a first login cannot bypass mediation through a TLS tunnel.
func (s *Store) RequiresMITM(host string) bool {
	endpoint := s.config.Provider.TokenEndpoint
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
