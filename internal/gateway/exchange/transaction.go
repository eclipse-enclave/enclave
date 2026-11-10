// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"enclave/internal/model"
	"enclave/internal/placeholderstore"
)

func (s *Store) transact(parent context.Context, values map[string]string, media string) *http.Response {
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	release, err := s.lock(ctx)
	if err != nil {
		return refusal(http.StatusServiceUnavailable)
	}
	defer release()
	bindings, err := placeholderstore.LoadBindings(s.dir)
	if err != nil {
		return refusal(http.StatusServiceUnavailable)
	}
	native, err := s.native()
	if err != nil {
		return refusal(http.StatusServiceUnavailable)
	}
	login := values["grant_type"] != "refresh_token"
	if !login {
		valid := false
		for _, field := range s.file.Fields {
			if field.Kind != "refresh" {
				continue
			}
			b, ok := bindingFor(bindings, s.config.Provider.Name, s.file.File, field)
			if ok && b.Matches(values["refresh_token"]) && native.Values[field.Pointer] != "" && !strings.Contains(native.Values[field.Pointer], model.SecretPlaceholderMarker) {
				valid = true
				values["refresh_token"] = native.Values[field.Pointer]
			}
		}
		if !valid {
			return refusal(http.StatusForbidden)
		}
	}
	credentials := []string{}
	for _, entry := range s.env {
		credentials = append(credentials, entry.Value)
	}
	for _, file := range s.config.Provider.AuthSecrets {
		if file.CheckOnly {
			continue
		}
		snapshot, err := placeholderstore.ReadNative(s.dir, file)
		if err != nil {
			if file.File == s.file.File && native.Fingerprint == "" {
				continue
			}
			if !isMissing(err) {
				return refusal(http.StatusServiceUnavailable)
			}
		}
		for _, value := range snapshot.Values {
			credentials = append(credentials, value)
		}
	}
	fail := func() *http.Response { return refusal(http.StatusBadGateway) }
	body := mustJSON(values)
	if media == "application/x-www-form-urlencoded" {
		form := url.Values{}
		for key, value := range values {
			form.Set(key, value)
		}
		body = []byte(form.Encode())
	}
	endpoint := s.config.Provider.TokenEndpoint
	outgoing, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+endpoint.Host+endpoint.Path, bytes.NewReader(body))
	if err != nil {
		return fail()
	}
	outgoing.GetBody = nil
	outgoing.Header.Set("Content-Type", media)
	outgoing.Header.Set("Accept-Encoding", "identity")
	upstream, err := s.transport.RoundTrip(outgoing)
	if err != nil {
		return fail()
	}
	defer func() { _ = upstream.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(upstream.Body, maxBodyBytes+1))
	encoding := upstream.Header.Get("Content-Encoding")
	if err != nil || len(data) > maxBodyBytes || (encoding != "" && !strings.EqualFold(encoding, "identity")) {
		return fail()
	}
	object, err := objectJSON(data)
	if err != nil {
		return fail()
	}
	headers := safeHeaders(upstream.Header)
	hasTokens := false
	for _, kind := range []string{"access", "refresh", "id"} {
		if key := endpoint.Fields[kind]; key != "" {
			if _, ok := object[key]; ok {
				hasTokens = true
				credentials = append(credentials, textField(object, key))
			}
		}
	}
	errorCode := textField(object, "error")
	if hasTokens && (errorCode != "" || upstream.StatusCode < 200 || upstream.StatusCode >= 300) {
		return fail()
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 || errorCode != "" {
		if upstream.StatusCode >= 200 && upstream.StatusCode < 300 {
			return fail()
		}
		result := map[string]string{}
		for _, key := range []string{"error", "error_description", "error_uri"} {
			if v := textField(object, key); v != "" {
				result[key] = v
			}
		}
		encoded, _ := json.Marshal(result)
		if !contained(encoded, headers, credentials) {
			return refusal(http.StatusBadGateway)
		}
		return response(upstream.StatusCode, headers, encoded)
	}
	// Only declared credential fields are committed. A valid consumed grant is
	// retained even when expiry is missing or malformed.
	tokens := map[string]string{}
	for _, kind := range []string{"access", "refresh", "id"} {
		key := endpoint.Fields[kind]
		if key == "" {
			continue
		}
		if _, present := object[key]; !present {
			continue
		}
		token := textField(object, key)
		if token == "" || strings.Contains(token, model.SecretPlaceholderMarker) {
			return fail()
		}
		tokens[kind] = token
		credentials = append(credentials, token)
	}
	if tokens["access"] == "" {
		return fail()
	}
	current, err := s.native()
	if err != nil || current.Fingerprint != native.Fingerprint {
		return fail()
	}
	for _, field := range s.file.Fields {
		if value := tokens[field.Kind]; value != "" {
			parts, _ := model.AuthPointerSegments(field.Pointer)
			node := native.Document
			for _, key := range parts[:len(parts)-1] {
				child, ok := node[key].(map[string]any)
				if !ok {
					child = map[string]any{}
					node[key] = child
				}
				node = child
			}
			node[parts[len(parts)-1]] = value
			native.Values[field.Pointer] = value
		}
	}
	tokenType := textField(object, "token_type")
	unsafeMetadata := false
	nonSecret := func(value string) bool {
		for _, credential := range credentials {
			if placeholderstore.ContainsCredential(value, credential) {
				unsafeMetadata = true
				return false
			}
		}
		return true
	}
	scope := textField(object, endpoint.Fields["scope"])
	if tokenType != "" && !nonSecret(tokenType) {
		tokenType = ""
	}
	native.UpdateMetadata(s.file.Codec, s.config.Provider.Name, scope, object[endpoint.Fields["expiresIn"]], login, s.now(), nonSecret)
	encoded := mustJSON(native.Document)
	for _, field := range s.file.Fields {
		if field.Kind == "apiKey" || native.Values[field.Pointer] == "" {
			continue
		}
		if _, err := bindings.Bind(s.config.Provider.Name, s.file.File, field.Pointer, field.Kind, field.Placeholder, ""); err != nil {
			return fail()
		}
	}
	// Commit native first so a failed bindings write cannot lose a consumed grant.
	if placeholderstore.WriteNative(s.dir, s.file.File, encoded) != nil {
		return fail()
	}
	native, err = placeholderstore.ReadNative(s.dir, s.file)
	if err != nil {
		return refusal(http.StatusBadGateway)
	}
	if placeholderstore.SaveBindings(s.dir, bindings) != nil {
		return refusal(http.StatusBadGateway)
	}
	if unsafeMetadata {
		return refusal(http.StatusBadGateway)
	}
	return s.tokenReply(native, bindings, credentials, headers, tokenType, scope)
}

func textField(object map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(object[key], &value)
	return value
}
func mustJSON(value any) []byte { data, _ := json.Marshal(value); return data }

func safeHeaders(headers http.Header) http.Header {
	result := http.Header{}
	for key, values := range headers {
		if slices.Contains([]string{"cache-control", "pragma"}, strings.ToLower(key)) {
			result[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
		}
	}
	return result
}

func (s *Store) tokenReply(native placeholderstore.NativeFile, bindings placeholderstore.Bindings, credentials []string, headers http.Header, tokenType, scope string) *http.Response {
	result := map[string]any{}
	endpoint := s.config.Provider.TokenEndpoint
	for _, field := range s.file.Fields {
		key := endpoint.Fields[field.Kind]
		if key == "" || native.Values[field.Pointer] == "" {
			continue
		}
		b, ok := bindingFor(bindings, s.config.Provider.Name, s.file.File, field)
		if !ok {
			return refusal(http.StatusBadGateway)
		}
		placeholder, err := b.Render(native.Values[field.Pointer])
		if err != nil {
			return refusal(http.StatusBadGateway)
		}
		result[key] = placeholder
	}
	if tokenType != "" {
		result["token_type"] = tokenType
	}
	if scope != "" {
		result[endpoint.Fields["scope"]] = scope
	}
	nativeScope, expiry := native.ReplyMetadata(s.file.Codec, s.config.Provider.Name)
	if nativeScope != "" {
		result[endpoint.Fields["scope"]] = nativeScope
	}
	if remaining := int64(expiry.Sub(s.now()) / time.Second); remaining > 0 {
		result[endpoint.Fields["expiresIn"]] = remaining
	}
	encoded := mustJSON(result)
	if !contained(encoded, headers, credentials) {
		return refusal(http.StatusBadGateway)
	}
	return response(http.StatusOK, headers, encoded)
}
