// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"bytes"
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

// Config is a declaration, not a snapshot of native credentials. Slice 2 only
// implements Claude; other codecs arrive with their first exchange consumer.
type Config struct {
	Provider model.ProviderConfig          `json:"provider"`
	Secrets  map[string]model.SecretConfig `json:"secrets"`
}

func endpointHost(req *http.Request) string {
	host := req.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// Intercept owns both successful exchanges and refusals. No endpoint request
// falls back to ordinary forwarding after a parser or store error.
func (s *Store) Intercept(req *http.Request, scheme string) (*http.Response, bool) {
	host := endpointHost(req)
	endpoint := s.config.Provider.TokenEndpoint
	canonicalPath := path.Clean("/" + req.URL.Path)
	for _, denied := range endpoint.Deny {
		if host == denied.Host && canonicalPath == denied.Path {
			return refusal(http.StatusForbidden), true
		}
	}
	if host != endpoint.Host || canonicalPath != endpoint.Path {
		return nil, false
	}
	_, port, portErr := net.SplitHostPort(req.Host)
	if (portErr == nil && port != "443") || scheme != "https" || req.Method != http.MethodPost || req.URL.Path != endpoint.Path || req.URL.RawQuery != "" || req.URL.RawPath != "" {
		return refusal(http.StatusForbidden), true
	}
	values, format, err := parseRequest(req, endpoint.Body)
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
	return s.transact(req.Context(), values, format), true
}

func parseRequest(req *http.Request, allowed string) (map[string]string, string, error) {
	media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", err
	}
	format := ""
	switch media {
	case "application/json":
		format = "json"
	case "application/x-www-form-urlencoded":
		format = "form"
	}
	if format == "" || (allowed != "either" && allowed != format) {
		return nil, "", fmt.Errorf("unsupported exchange body")
	}
	if enc := req.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		return nil, "", fmt.Errorf("encoded exchange")
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes+1))
	if err != nil || len(data) > maxBodyBytes {
		return nil, "", fmt.Errorf("invalid exchange body")
	}
	values := map[string]string{}
	if format == "json" {
		object, err := objectJSON(data)
		if err != nil {
			return nil, "", err
		}
		for key, raw := range object {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || bytes.Equal(raw, []byte("null")) {
				return nil, "", fmt.Errorf("exchange fields must be strings")
			}
			values[key] = value
		}
	} else {
		form, err := url.ParseQuery(string(data))
		if err != nil {
			return nil, "", err
		}
		for key, list := range form {
			if len(list) != 1 {
				return nil, "", fmt.Errorf("duplicate exchange field")
			}
			values[key] = list[0]
		}
	}
	return values, format, nil
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

func encodeRequest(values map[string]string, format string) ([]byte, string) {
	if format == "form" {
		form := url.Values{}
		for k, v := range values {
			form.Set(k, v)
		}
		return []byte(form.Encode()), "application/x-www-form-urlencoded"
	}
	data, _ := json.Marshal(values)
	return data, "application/json"
}

func refusal(status int) *http.Response {
	return response(status, http.Header{}, []byte(`{"error":"protected_auth_failed"}`))
}
func response(status int, headers http.Header, data []byte) *http.Response {
	headers.Set("Content-Type", "application/json")
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}
}
