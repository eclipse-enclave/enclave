// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"net/http"
	"reflect"
	"testing"

	"enclave/internal/model"
)

func TestRewriteHeadersRejectsCanonicalizedPlaceholderHeaderNames(t *testing.T) {
	for _, placeholder := range []string{"ENCLAVE_SECRET_abcdef", "vendor-MiXeD_token-end"} {
		t.Run(placeholder, func(t *testing.T) {
			headers := http.Header{}
			headers.Add("x-"+placeholder, "ordinary")
			headers.Add("Authorization", "Bearer "+placeholder)
			before := headers.Clone()
			rules := []model.SecretReleaseEntry{{Placeholder: placeholder, Value: "real-secret", Hosts: []string{"api.example.com"}, Header: "authorization", Format: "Bearer %s"}}
			if err := rewriteHeaders("https", "api.example.com", headers, rules); err == nil {
				t.Fatal("canonicalized placeholder header name accepted")
			}
			if !reflect.DeepEqual(headers, before) {
				t.Fatal("denied request headers changed")
			}
		})
	}
}

func TestRewriteHeadersRespectsDeclaredHeaders(t *testing.T) {
	rules := []model.SecretReleaseEntry{
		{Placeholder: "sk-ant-oat01-ENCLAVE_SECRET_oauth", Value: "real-oauth", Hosts: []string{"api.anthropic.com"}, Header: "authorization", Format: "Bearer %s"},
		{Placeholder: "ENCLAVE_SECRET_key", Value: "real-key", Hosts: []string{"api.anthropic.com"}, Header: "x-api-key"},
	}
	for _, tc := range []struct {
		name    string
		headers http.Header
		deny    bool
	}{
		{"both credentials", http.Header{"Authorization": {"Bearer sk-ant-oat01-ENCLAVE_SECRET_oauth"}, "X-Api-Key": {"ENCLAVE_SECRET_key"}}, false},
		{"wrong header", http.Header{"X-Debug": {"sk-ant-oat01-ENCLAVE_SECRET_oauth"}}, true},
		{"other credential header", http.Header{"Authorization": {"ENCLAVE_SECRET_key"}}, true},
		{"mixed valid and invalid", http.Header{"Authorization": {"Bearer sk-ant-oat01-ENCLAVE_SECRET_oauth"}, "X-Debug": {"ENCLAVE_SECRET_key"}}, true},
		{"multiple values", http.Header{"Authorization": {"Bearer sk-ant-oat01-ENCLAVE_SECRET_oauth"}, "X-Debug": {"ordinary", "ENCLAVE_SECRET_key"}}, true},
		{"header name", http.Header{"X-ENCLAVE_SECRET_key": {"ordinary"}}, true},
		{"case insensitive", http.Header{"authorization": {"Bearer sk-ant-oat01-ENCLAVE_SECRET_oauth"}}, false},
		{"ordinary headers", http.Header{"X-Debug": {"ordinary"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.headers.Clone()
			err := rewriteHeaders("https", "api.anthropic.com", tc.headers, rules)
			if (err != nil) != tc.deny {
				t.Fatalf("rewriteHeaders() error = %v, deny = %v", err, tc.deny)
			}
			if tc.deny {
				if !reflect.DeepEqual(tc.headers, before) {
					t.Fatalf("denied request headers changed: %v", tc.headers)
				}
				return
			}
			for header, values := range tc.headers {
				for i, value := range values {
					want := before[header][i]
					if header == "Authorization" || header == "authorization" {
						want = "Bearer real-oauth"
					}
					if header == "X-Api-Key" {
						want = "real-key"
					}
					if value != want {
						t.Fatalf("%s = %q, want %q", header, value, want)
					}
				}
			}
		})
	}
}
