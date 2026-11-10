// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"enclave/internal/model"
)

const maxBodyBytes = 64 << 10

// Config declares mediated native providers, never a credential snapshot.
type Config struct {
	Providers []model.ProviderConfig        `json:"providers"`
	Secrets   map[string]model.SecretConfig `json:"secrets"`
}

func endpointHost(req *http.Request) string {
	host := cmp.Or(strings.TrimSpace(req.Host), req.URL.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// Intercept owns both successful exchanges and refusals. No endpoint request
// falls back to ordinary forwarding after a parser or store error.
func (s *Store) Intercept(req *http.Request, scheme string) (*http.Response, bool) {
	for _, s := range s.routes {
		host := endpointHost(req)
		endpoint := s.provider.TokenEndpoint
		canonicalPath := path.Clean("/" + req.URL.Path)
		for _, denied := range endpoint.Deny {
			if host == denied.Host && canonicalPath == denied.Path {
				return refusal(http.StatusForbidden), true
			}
		}
		if host != endpoint.Host || !strings.HasPrefix(strings.ToLower(canonicalPath), strings.ToLower(endpoint.Path)) {
			continue
		}
		_, port, portErr := net.SplitHostPort(cmp.Or(strings.TrimSpace(req.Host), req.URL.Host))
		if (portErr == nil && port != "443") || scheme != "https" || req.Method != http.MethodPost || req.URL.Path != endpoint.Path || req.URL.RawQuery != "" || req.URL.RawPath != "" {
			return refusal(http.StatusForbidden), true
		}
		values, err := parseRequest(req, endpoint.Body)
		if err != nil || !slices.Contains(endpoint.Grants, values["grant_type"]) {
			return refusal(http.StatusForbidden), true
		}
		if values["grant_type"] == "authorization_code" && (values["code"] == "" || values["refresh_token"] != "") {
			return refusal(http.StatusForbidden), true
		}
		for key, value := range values {
			if strings.Contains(key, model.SecretPlaceholderMarker) || (strings.Contains(value, model.SecretPlaceholderMarker) && key != endpoint.Fields["refresh"]) {
				return refusal(http.StatusForbidden), true
			}
		}
		media, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
		return s.transact(req.Context(), values, media), true
	}
	return nil, false
}

func parseRequest(req *http.Request, body string) (map[string]string, error) {
	media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || (media != "application/json" && media != "application/x-www-form-urlencoded") || (body == "json" && media != "application/json") || (body == "form" && media != "application/x-www-form-urlencoded") {
		return nil, fmt.Errorf("exchange body media type is not declared")
	}
	if enc := req.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		return nil, fmt.Errorf("encoded exchange")
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes+1))
	if err != nil || len(data) > maxBodyBytes {
		return nil, fmt.Errorf("invalid exchange body")
	}
	if media == "application/x-www-form-urlencoded" {
		form, err := url.ParseQuery(string(data))
		if err != nil {
			return nil, err
		}
		values := map[string]string{}
		for key, entries := range form {
			if len(entries) != 1 {
				return nil, fmt.Errorf("duplicate exchange field")
			}
			values[key] = entries[0]
		}
		return values, nil
	}
	object, err := objectJSON(data)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for key, raw := range object {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || bytes.Equal(raw, []byte("null")) {
			return nil, fmt.Errorf("exchange fields must be strings")
		}
		values[key] = value
	}
	return values, nil
}

// Only top-level scalar fields are interpreted. Unknown response values are
// discarded, while duplicate keys at the protocol boundary are always refused.
func objectJSON(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("exchange body must be an object")
	}
	object := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		key := token.(string)
		if _, found := object[key]; found {
			return nil, fmt.Errorf("duplicate exchange field")
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, err
		}
		object[key] = raw
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing exchange data")
	}
	return object, nil
}

func refusal(status int) *http.Response {
	return response(status, http.Header{}, []byte(`{"error":"protected_auth_failed"}`))
}
func response(status int, headers http.Header, data []byte) *http.Response {
	headers.Set("Content-Type", "application/json")
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}
}

func copilotAPIRequest(req *http.Request) bool {
	_, port, _ := net.SplitHostPort(cmp.Or(strings.TrimSpace(req.Host), req.URL.Host))
	if req.Method != http.MethodGet || !slices.Contains([]string{"/copilot_internal/user", "/copilot_internal/managed_settings", "/copilot_internal/team_customizations"}, req.URL.Path) || req.URL.RawPath != "" || req.ContentLength != 0 || len(req.Trailer) != 0 || (port != "" && port != "443") {
		return false
	}
	if req.Body != nil {
		var first [1]byte
		n, err := req.Body.Read(first[:])
		req.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(first[:n]), req.Body), req.Body}
		return n == 0 && err == io.EOF && len(req.Trailer) == 0
	}
	return true
}
