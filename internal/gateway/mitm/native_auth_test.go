// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/gateway/exchange"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
	"enclave/internal/placeholderstore"
)

func nativeProxy(t *testing.T) (*proxy, string, map[string]string) {
	t.Helper()
	provider := model.ProviderConfig{Name: "anthropic", AuthSecrets: []model.AuthFileSecret{{File: ".credentials.json", Codec: "claude-credentials", Fields: []model.AuthSecretField{{Pointer: "/claudeAiOauth/accessToken", Kind: "access", Release: "oauth"}, {Pointer: "/claudeAiOauth/refreshToken", Kind: "refresh"}}}}, TokenEndpoint: &model.TokenEndpoint{Host: "platform.claude.com", Path: "/v1/oauth/token", Body: "json", Grants: []string{"authorization_code", "refresh_token"}, Fields: map[string]string{"access": "access_token", "refresh": "refresh_token"}, Deny: []model.AuthEndpoint{{Host: "api.anthropic.com", Path: "/api/oauth/claude_cli/create_api_key"}}}}
	secrets := map[string]model.SecretConfig{"oauth": {Release: &model.SecretReleaseConfig{HTTP: &model.HTTPSecretReleaseConfig{Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer %s"}}}}
	dir := t.TempDir()
	if err := placeholderstore.WriteNative(dir, ".credentials.json", []byte(`{"claudeAiOauth":{"accessToken":"synthetic-access-first","refreshToken":"synthetic-refresh"}}`)); err != nil {
		t.Fatal(err)
	}
	view, err := placeholderstore.Project(dir, t.TempDir(), "test", model.Profile{Providers: []model.ProviderConfig{provider}, Secrets: secrets}, nil)
	if err != nil {
		t.Fatal(err)
	}
	placeholders := map[string]string{}
	for _, b := range view.Bindings.Bindings {
		placeholders[b.Kind] = b.Placeholder
	}
	store, err := exchange.New(exchange.Config{Provider: provider, Secrets: secrets}, dir, filepath.Join(t.TempDir(), "canonical.lock"), "session", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := newProxy([]string{"api.anthropic.com", "platform.claude.com"}, nil, nil, false, nil, "session")
	p.nativeAuth = store
	return p, dir, placeholders
}

func TestNativeProxyReadThroughReleaseAndRefusals(t *testing.T) {
	p, dir, ph := nativeProxy(t)
	received := make(chan string, 5)
	transport := p.transport.Clone()
	p.transport = transport
	request := func(url, header, value string) int {
		req := httptest.NewRequest("POST", url, strings.NewReader(`{"grant_type":"refresh_token","refresh_token":"ENCLAVE_SECRET_foreign"}`))
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set(header, value)
		}
		w := httptest.NewRecorder()
		p.handler("https")(w, req)
		return w.Code
	}
	// The proxy's transport targets the local fixture while keeping its exact
	// release host. Only the fixture certificate is trusted on this test transport.
	tlsUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		w.WriteHeader(204)
	}))
	defer tlsUpstream.Close()
	roots := x509.NewCertPool()
	roots.AddCert(tlsUpstream.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: tlsUpstream.Certificate().DNSNames[0], MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(tlsUpstream.URL, "https://"))
	}
	if request("https://api.anthropic.com/v1/messages", "Authorization", "Bearer "+ph["access"]) != 204 {
		t.Fatal("native API release failed")
	}
	if <-received != "Bearer synthetic-access-first" {
		t.Fatal("wrong access released")
	}
	native, err := placeholderstore.ReadNative(dir, model.AuthFileSecret{File: ".credentials.json", Codec: "claude-credentials", Fields: []model.AuthSecretField{{Pointer: "/claudeAiOauth/accessToken", Kind: "access"}, {Pointer: "/claudeAiOauth/refreshToken", Kind: "refresh"}}})
	if err != nil {
		t.Fatal(err)
	}
	native.Document["claudeAiOauth"].(map[string]any)["accessToken"] = "synthetic-access-second"
	data, _ := json.Marshal(native.Document)
	if err := placeholderstore.WriteNative(dir, ".credentials.json", data); err != nil {
		t.Fatal(err)
	}
	if request("https://api.anthropic.com/v1/messages", "Authorization", ph["access"]) != 204 || <-received != "Bearer synthetic-access-second" {
		t.Fatal("stale native access released")
	}
	for _, tc := range []struct{ url, header, value string }{
		{"https://api.anthropic.com/v1/messages", "Authorization", ph["refresh"]},
		{"https://api.anthropic.com/v1/messages", "X-Debug", ph["access"]},
		{"https://api.anthropic.com/api/oauth/claude_cli/create_api_key", "Authorization", ph["access"]},
		{"https://platform.claude.com/v1/oauth/token", "", ""},
	} {
		if request(tc.url, tc.header, tc.value) != 403 {
			t.Fatal("unsafe native request forwarded")
		}
	}
	if err := os.Remove(filepath.Join(dir, placeholderstore.BindingsFile)); err != nil {
		t.Fatal(err)
	}
	if request("https://api.anthropic.com/v1/messages", "Authorization", ph["access"]) != 403 {
		t.Fatal("reset native binding still released")
	}
	if request("https://api.anthropic.com/v1/messages", "X-ENCLAVE_SECRET_unknown", "test") != 403 {
		t.Fatal("canonicalized unbound marker name forwarded")
	}
	select {
	case <-received:
		t.Fatal("denied request reached upstream")
	default:
	}
}

func TestNativeProxyMITMAndReadinessAfterHandlerInstall(t *testing.T) {
	p, _, _ := nativeProxy(t)
	if !p.nativeAuth.RequiresMITM("platform.claude.com") || !p.nativeAuth.RequiresMITM("api.anthropic.com") || p.nativeAuth.RequiresMITM("unrelated.example") {
		t.Fatal("native hosts bypass interception")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ready := false
	err := Run(ctx, Options{HTTPAddr: "127.0.0.1:0", HTTPSAddr: "127.0.0.1:0", TLSStore: tlsstore.New(t.TempDir()), NativeAuth: p.nativeAuth, OnReady: func() { ready = true; cancel() }})
	if err != nil || !ready {
		t.Fatalf("native proxy not installed before readiness: %v", err)
	}
}

func TestNativeAPIKeyAliasesUseExistingEnvRelease(t *testing.T) {
	_, dir, _ := nativeProxy(t)
	api := model.SecretReleaseEntry{SecretID: "api", Placeholder: "ENCLAVE_SECRET_synthetic_api", Value: "synthetic-env-api-key", Hosts: []string{"api.anthropic.com"}, Header: "X-Api-Key"}
	provider := model.ProviderConfig{Name: "anthropic", AuthSecrets: []model.AuthFileSecret{
		{File: ".credentials.json", Codec: "claude-credentials", Fields: []model.AuthSecretField{{Pointer: "/claudeAiOauth/accessToken", Kind: "access", Release: "oauth"}, {Pointer: "/claudeAiOauth/refreshToken", Kind: "refresh"}}},
		{File: "config.json", Codec: "claude-config", Fields: []model.AuthSecretField{{Pointer: "/primaryApiKey", Kind: "apiKey", Release: "api"}, {Pointer: "/anthropicApiKey", Kind: "apiKey", Release: "api"}}},
	}, TokenEndpoint: &model.TokenEndpoint{Host: "platform.claude.com", Path: "/v1/oauth/token", Body: "json", Grants: []string{"authorization_code", "refresh_token"}, Fields: map[string]string{"access": "access_token", "refresh": "refresh_token"}}}
	secrets := map[string]model.SecretConfig{"oauth": {Release: &model.SecretReleaseConfig{HTTP: &model.HTTPSecretReleaseConfig{Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer %s"}}}}
	if err := placeholderstore.WriteNative(dir, "config.json", []byte(`{"primaryApiKey":"synthetic-native-api-key"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := placeholderstore.Project(dir, t.TempDir(), "api-alias", model.Profile{Providers: []model.ProviderConfig{provider}, Secrets: secrets}, []model.SecretReleaseEntry{api}); err != nil {
		t.Fatal(err)
	}
	store, err := exchange.New(exchange.Config{Provider: provider, Secrets: secrets}, dir, filepath.Join(t.TempDir(), "api.lock"), "session", []model.SecretReleaseEntry{api})
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"X-Api-Key": []string{api.Placeholder}}
	dynamic, err := store.Releases(context.Background(), headers)
	if err != nil {
		t.Fatal(err)
	}
	rules := append([]model.SecretReleaseEntry{api}, dynamic...)
	if err := rewriteHeaders("https", "api.anthropic.com", headers, rules); err != nil || headers.Get("X-Api-Key") != api.Value {
		t.Fatal("API alias did not use existing env release")
	}
	wrong := http.Header{"X-Debug": []string{api.Placeholder}}
	if rewriteHeaders("https", "api.anthropic.com", wrong, rules) == nil {
		t.Fatal("API alias bypassed declared header")
	}
}
