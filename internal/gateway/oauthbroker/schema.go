// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxDocument = 64 << 10

// This is an experimental closed schema, not a claim about the live provider.
// Unknown nested fields are rejected even when the candidate client ignores them.
type tokenResponse struct {
	Access       string        `json:"access_token"`            // #nosec G117 -- stored only in gateway-private state; HTTP projections overwrite it with a handle.
	Refresh      string        `json:"refresh_token,omitempty"` // #nosec G117 -- stored only in gateway-private state; HTTP projections overwrite it with a handle.
	Type         string        `json:"token_type"`
	Expires      int64         `json:"expires_in"`
	Scope        string        `json:"scope"`
	Account      *account      `json:"account,omitempty"`
	Organization *organization `json:"organization,omitempty"`
}

type account struct {
	UUID  string `json:"uuid"`
	Email string `json:"email_address"`
}

type organization struct {
	UUID string `json:"uuid"`
}

type tokenRequest struct {
	GrantType   string `json:"grant_type"`
	ClientID    string `json:"client_id"`
	Code        string `json:"code,omitempty"`
	RedirectURI string `json:"redirect_uri,omitempty"`
	Verifier    string `json:"code_verifier,omitempty"`
	State       string `json:"state,omitempty"`
	Refresh     string `json:"refresh_token,omitempty"` // #nosec G117 -- real token serialized only for the exact trusted HTTPS token endpoint.
	Scope       string `json:"scope,omitempty"`
}

func strictJSON(data []byte, dst any) error {
	if len(data) == 0 || len(data) > maxDocument || !utf8.Valid(data) {
		return ErrResponse
	}
	// encoding/json accepts duplicate fields (including escaped spellings) and
	// case-insensitive struct keys. Validate structure first and decode by exact
	// field names below to avoid ambiguous token/metadata interpretation.
	d := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueValue(d, 0); err != nil {
		return ErrResponse
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrResponse
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return ErrResponse
	}
	return nil
}

func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrResponse
	}
	token, err := d.Token()
	if err != nil {
		return ErrResponse
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return ErrResponse
	}
	keys := map[string]bool{}
	for d.More() {
		if delim == '{' {
			token, err := d.Token()
			if err != nil {
				return ErrResponse
			}
			key, ok := token.(string)
			if !ok || keys[strings.ToLower(key)] {
				return ErrResponse
			}
			keys[strings.ToLower(key)] = true
		}
		if err := uniqueValue(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func exactKeys(data []byte, allowed ...string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return ErrResponse
	}
	for field, value := range fields {
		found := false
		for _, key := range allowed {
			if field == key {
				found = true
				break
			}
		}
		if !found || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrResponse
		}
	}
	return nil
}

func decodeToken(data []byte) (tokenResponse, error) {
	var token tokenResponse
	if err := strictJSON(data, &token); err != nil {
		return token, err
	}
	if err := exactKeys(data, "access_token", "refresh_token", "token_type", "expires_in", "scope", "account", "organization"); err != nil {
		return tokenResponse{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return tokenResponse{}, ErrResponse
	}
	for _, field := range []string{"account", "organization"} {
		if raw, ok := fields[field]; ok {
			allowed := []string{"uuid"}
			if field == "account" {
				allowed = append(allowed, "email_address")
			}
			if err := exactKeys(raw, allowed...); err != nil {
				return tokenResponse{}, err
			}
		}
	}
	return token, nil
}

func validateToken(t tokenResponse, initial bool) error {
	if !opaqueToken(t.Access) || t.Type != "Bearer" || t.Expires < 1 || t.Expires > 86400*366 || !validScope(t.Scope) {
		return ErrResponse
	}
	if (initial || t.Refresh != "") && !opaqueToken(t.Refresh) {
		return ErrResponse
	}
	if initial && (t.Account == nil || t.Organization == nil) {
		return ErrResponse
	}
	if t.Account != nil && (!uuid(t.Account.UUID) || !validEmail(t.Account.Email)) {
		return ErrResponse
	}
	if t.Organization != nil && !uuid(t.Organization.UUID) {
		return ErrResponse
	}
	return nil
}

func validEmail(value string) bool {
	if !safeText(value, 320) {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && address.Name == ""
}

func validScope(value string) bool {
	if !safeText(value, 4096) {
		return false
	}
	for _, scope := range strings.Split(value, " ") {
		switch scope {
		case "user:profile", "user:inference", "user:sessions:claude_code", "user:mcp_servers", "org:create_api_key":
		default:
			return false
		}
	}
	return true
}

func opaqueToken(value string) bool {
	if len(value) < 16 || len(value) > 8192 {
		return false
	}
	for _, c := range value {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func safeText(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func uuid(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func jsonContent(header http.Header) bool {
	if len(header.Values("Content-Type")) != 1 {
		return false
	}
	kind, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return false
	}
	for key, value := range params {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func boundedRead(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxDocument+1))
	if err != nil || len(data) > maxDocument {
		return nil, ErrResponse
	}
	return data, nil
}

func responseBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, ErrResponse
	}
	defer func() { _ = resp.Body.Close() }()
	if !jsonContent(resp.Header) || resp.ContentLength > maxDocument || len(resp.Header.Values("Content-Encoding")) > 1 {
		return nil, ErrResponse
	}
	encoded, err := boundedRead(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.Uncompressed || (resp.ContentLength >= 0 && int64(len(encoded)) != resp.ContentLength) {
		return nil, ErrResponse
	}
	switch resp.Header.Get("Content-Encoding") {
	case "", "identity":
		return encoded, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(encoded))
		if err != nil {
			return nil, ErrResponse
		}
		defer func() { _ = reader.Close() }()
		return boundedRead(reader)
	default:
		return nil, ErrResponse
	}
}

func projectedResponse(status int, value any) (*http.Response, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, ErrResponse
	}
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type":   {"application/json"},
			"Content-Length": {strconv.Itoa(len(data))},
			"Cache-Control":  {"no-store"},
			"Pragma":         {"no-cache"},
		},
		ContentLength: int64(len(data)),
		Body:          io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func sanitizedError(resp *http.Response, data []byte) (*http.Response, error) {
	var value struct {
		Code        string `json:"error"`
		Description string `json:"error_description,omitempty"`
		URI         string `json:"error_uri,omitempty"`
	}
	if resp.StatusCode != 400 && resp.StatusCode != 401 && resp.StatusCode != 403 && resp.StatusCode != 429 {
		return nil, ErrResponse
	}
	if strictJSON(data, &value) != nil || exactKeys(data, "error", "error_description", "error_uri") != nil {
		return nil, ErrResponse
	}
	switch value.Code {
	case "invalid_request", "invalid_client", "invalid_grant", "unauthorized_client", "unsupported_grant_type", "invalid_scope", "access_denied", "authorization_pending", "slow_down", "expired_token", "temporarily_unavailable", "server_error":
		// Descriptions, URIs, headers, and trailers may reflect credentials or
		// bootstrap material. Only the enumerated code crosses the boundary.
		return projectedResponse(resp.StatusCode, map[string]string{"error": value.Code})
	default:
		return nil, ErrResponse
	}
}
