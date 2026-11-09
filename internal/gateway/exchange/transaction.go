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
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"enclave/internal/model"
	"enclave/internal/placeholderstore"
)

func (s *Store) transact(parent context.Context, values map[string]string, format string) *http.Response {
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	release, err := s.lock(ctx)
	if err != nil {
		return refusal(http.StatusServiceUnavailable)
	}
	defer release()
	bindings, state, err := s.load()
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
		if state.Uncertain {
			return refusal(http.StatusServiceUnavailable)
		}
	}
	credentials := []string{}
	for _, entry := range s.env {
		credentials = append(credentials, entry.Value)
	}
	for _, file := range s.config.Provider.AuthSecrets {
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
	if state.Fingerprint != native.Fingerprint {
		state.Replay = nil
		state.ExpiresAt = nil
	}
	if !login && replayValid(native, state, s.now()) {
		return s.tokenReply(native, bindings, state, credentials)
	}
	if state.Generation == math.MaxUint64 {
		return refusal(http.StatusServiceUnavailable)
	}
	state.Inflight = &placeholderstore.ExchangeIntent{Generation: state.Generation, StartedAt: s.now().UTC(), Login: login, Session: s.session}
	bindings.Providers[s.config.Provider.Name] = state
	if placeholderstore.SaveBindings(s.dir, bindings) != nil {
		return refusal(http.StatusServiceUnavailable)
	}
	// Every path after this durable intent either commits native-first or leaves
	// recovery evidence. No HTTP client redirect loop or replayable request body.
	finish := func(uncertain bool) {
		state.Inflight = nil
		state.Uncertain = state.Uncertain || uncertain
		bindings.Providers[s.config.Provider.Name] = state
	}
	fail := func() *http.Response {
		finish(!login)
		_ = placeholderstore.SaveBindings(s.dir, bindings)
		return refusal(http.StatusBadGateway)
	}
	body, contentType := encodeRequest(values, format)
	endpoint := s.config.Provider.TokenEndpoint
	outgoing, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+endpoint.Host+endpoint.Path, bytes.NewReader(body))
	if err != nil {
		return fail()
	}
	outgoing.GetBody = nil
	outgoing.Header.Set("Content-Type", contentType)
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
		pending := upstream.StatusCode >= 200 && upstream.StatusCode < 300 && slices.Contains([]string{"authorization_pending", "slow_down"}, errorCode)
		if upstream.StatusCode >= 200 && upstream.StatusCode < 300 && !pending {
			return fail()
		}
		nonConsuming := upstream.StatusCode == 400 && slices.Contains([]string{"invalid_request", "invalid_client", "unauthorized_client", "unsupported_grant_type", "invalid_scope"}, errorCode)
		finish(!login && !pending && !nonConsuming)
		if placeholderstore.SaveBindings(s.dir, bindings) != nil {
			return refusal(http.StatusBadGateway)
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
	// retained even when expiry is missing or malformed; that response cannot replay.
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
	oauth, ok := native.Document["claudeAiOauth"].(map[string]any)
	if !ok {
		oauth = map[string]any{}
		native.Document["claudeAiOauth"] = oauth
	}
	for _, field := range s.file.Fields {
		if value := tokens[field.Kind]; value != "" {
			parts, _ := model.AuthPointerSegments(field.Pointer)
			if len(parts) != 2 || parts[0] != "claudeAiOauth" {
				return fail()
			}
			oauth[parts[1]] = value
		}
	}
	replay := placeholderstore.ReplayFields{}
	unsafeMetadata := false
	nonSecret := func(value string) bool {
		for _, credential := range credentials {
			if contains(value, credential) {
				unsafeMetadata = true
				return false
			}
		}
		return true
	}
	if scope := textField(object, endpoint.Fields["scope"]); scope != "" && nonSecret(scope) {
		replay.Scope = &scope
		oauth["scopes"] = strings.Fields(scope)
	}
	if tokenType := textField(object, "token_type"); tokenType != "" && nonSecret(tokenType) {
		replay.TokenType = &tokenType
	}
	state.ExpiresAt = nil
	state.Replay = nil
	if raw, ok := object[endpoint.Fields["expiresIn"]]; ok {
		var seconds int64
		if json.Unmarshal(raw, &seconds) == nil && seconds > 0 && seconds <= math.MaxInt64/int64(time.Second) {
			expiry := s.now().Add(time.Duration(seconds) * time.Second).UTC().Truncate(time.Millisecond)
			state.ExpiresAt = &expiry
			state.Replay = &replay
			oauth["expiresAt"] = expiry.UnixMilli()
		}
	}
	if state.ExpiresAt == nil {
		delete(oauth, "expiresAt")
	}
	encoded, err := json.Marshal(native.Document)
	if err != nil {
		return fail()
	}
	projected, _, _, err := placeholderstore.ProjectFile(s.config.Provider.Name, s.file, encoded, &bindings, s.config.Secrets, s.env)
	if err != nil {
		return fail()
	}
	// Projection has not touched disk or bindings yet. Native first, bindings
	// second: a failure in between leaves the intent for the next locked reader.
	if placeholderstore.WriteNative(s.dir, s.file.File, encoded) != nil {
		return fail()
	}
	native, err = placeholderstore.ReadNative(s.dir, s.file)
	if err != nil {
		return refusal(http.StatusBadGateway)
	}
	state.Generation++
	state.Fingerprint = native.Fingerprint
	state.Inflight = nil
	state.Uncertain = false
	bindings.Providers[s.config.Provider.Name] = state
	if placeholderstore.SaveBindings(s.dir, bindings) != nil {
		return refusal(http.StatusBadGateway)
	}
	if unsafeMetadata || !contained(mustJSON(projected), headers, credentials) {
		return refusal(http.StatusBadGateway)
	}
	replyState := state
	replyState.Replay = &replay
	result := s.tokenReply(native, bindings, replyState, credentials)
	if result.StatusCode == http.StatusOK {
		for key, values := range headers {
			result.Header[key] = values
		}
	}
	return result
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

func replayValid(native placeholderstore.NativeFile, state placeholderstore.ExchangeState, now time.Time) bool {
	if state.Replay == nil || state.ExpiresAt == nil || state.Fingerprint != native.Fingerprint || int64(state.ExpiresAt.Sub(now)/time.Second) <= 0 {
		return false
	}
	oauth, ok := native.Document["claudeAiOauth"].(map[string]any)
	if !ok {
		return false
	}
	number, ok := oauth["expiresAt"].(json.Number)
	if !ok {
		return false
	}
	millis, err := number.Int64()
	return err == nil && millis == state.ExpiresAt.UnixMilli()
}

func (s *Store) tokenReply(native placeholderstore.NativeFile, bindings placeholderstore.Bindings, state placeholderstore.ExchangeState, credentials []string) *http.Response {
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
		result[key] = b.Placeholder
	}
	if state.Replay != nil {
		if state.Replay.Scope != nil {
			result[endpoint.Fields["scope"]] = *state.Replay.Scope
		}
		if state.Replay.TokenType != nil {
			result["token_type"] = *state.Replay.TokenType
		}
	}
	if state.ExpiresAt != nil {
		remaining := int64(state.ExpiresAt.Sub(s.now()) / time.Second)
		if remaining > 0 {
			result[endpoint.Fields["expiresIn"]] = remaining
		}
	}
	encoded := mustJSON(result)
	if !contained(encoded, http.Header{}, credentials) {
		return refusal(http.StatusBadGateway)
	}
	return response(http.StatusOK, http.Header{}, encoded)
}
