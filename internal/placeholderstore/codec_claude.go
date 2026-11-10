// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"enclave/internal/model"
)

type nativePurpose int

const (
	strictNative nativePurpose = iota
	projectedView
	canonicalNative
	credentialCheck
)

type nativeValue struct {
	field   model.AuthSecretField
	value   string
	replace func(string)
}

func decodeAuthFile(file model.AuthFileSecret, data []byte, purpose nativePurpose) (map[string]any, []nativeValue, error) {
	doc, pointers, err := decodeNative(file.Codec, data, purpose)
	if err != nil {
		return nil, nil, err
	}
	declared := map[string]bool{}
	for _, field := range file.Fields {
		declared[field.Pointer] = true
	}
	for _, pointer := range pointers {
		if !declared[pointer] {
			return nil, nil, fmt.Errorf("credential field %s is not declared", pointer)
		}
	}
	var values []nativeValue
	for _, field := range file.Fields {
		parts, err := model.AuthPointerSegments(field.Pointer)
		if err != nil {
			return nil, nil, err
		}
		node := doc
		for i, key := range parts {
			value, exists := node[key]
			if !exists || value == nil || value == "" {
				break
			}
			if i == len(parts)-1 {
				text, ok := value.(string)
				if !ok {
					return nil, nil, fmt.Errorf("credential field %s must be a string", field.Pointer)
				}
				values = append(values, nativeValue{field: field, value: text, replace: func(replacement string) { node[key] = replacement }})
				break
			}
			var ok bool
			node, ok = value.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("credential pointer traverses a non-object")
			}
		}
	}
	return doc, values, nil
}

func decodeNative(codec string, data []byte, purpose nativePurpose) (map[string]any, []string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSONValue(decoder, 0); err != nil {
		return nil, nil, fmt.Errorf("invalid native credential JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, nil, fmt.Errorf("native credential file contains multiple values")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil || doc == nil {
		return nil, nil, fmt.Errorf("native credential file must be an object")
	}
	switch codec {
	case "claude-credentials":
		if err := filterFields(doc, map[string]string{"claudeAiOauth": "object"}, purpose); err != nil {
			return nil, nil, err
		}
		if oauth, exists := doc["claudeAiOauth"]; exists {
			object, ok := oauth.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("claudeAiOauth must be an object")
			}
			if err := filterFields(object, map[string]string{"accessToken": "string", "refreshToken": "string", "expiresAt": "number", "scopes": "strings", "subscriptionType": "string", "rateLimitTier": "string"}, purpose); err != nil {
				return nil, nil, err
			}
		}
		return doc, []string{"/claudeAiOauth/accessToken", "/claudeAiOauth/refreshToken"}, nil
	case "codex-auth":
		if err := filterFields(doc, map[string]string{"auth_mode": "string", "OPENAI_API_KEY": "string", "tokens": "object", "last_refresh": "string"}, purpose); err != nil {
			return nil, nil, err
		}
		if tokens, ok := doc["tokens"].(map[string]any); ok {
			if err := filterFields(tokens, map[string]string{"access_token": "string", "refresh_token": "string", "id_token": "string", "account_id": "string"}, purpose); err != nil {
				return nil, nil, err
			}
		}
		return doc, []string{"/OPENAI_API_KEY", "/tokens/access_token", "/tokens/refresh_token", "/tokens/id_token"}, nil
	case "claude-config":
		if err := filterFields(doc, map[string]string{"primaryApiKey": "string", "anthropicApiKey": "string", "allowTelemetry": "bool", "hasCompletedOnboarding": "bool", "lastOnboardingVersion": "string", "forceLoginMethod": "string", "oauthAccount": "object"}, purpose); err != nil {
			return nil, nil, err
		}
		if account, ok := doc["oauthAccount"].(map[string]any); ok {
			if err := filterFields(account, map[string]string{"accountUuid": "string", "emailAddress": "string", "organizationUuid": "string", "organizationRole": "string", "workspaceRole": "string", "displayName": "string"}, purpose); err != nil {
				return nil, nil, err
			}
		}
		return doc, []string{"/primaryApiKey", "/anthropicApiKey"}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported native credential codec %q", codec)
	}
}

// Only known metadata leaves may reach a view. Kept native files must already
// satisfy the allowlist; projection can discard extra keys.
func filterFields(object map[string]any, allowed map[string]string, purpose nativePurpose) error {
	if purpose == credentialCheck {
		return nil
	}
	for key, value := range object {
		kind, ok := allowed[key]
		if !ok {
			if purpose == strictNative {
				return fmt.Errorf("unsupported native credential fields")
			}
			if purpose == projectedView {
				delete(object, key)
			}
			continue
		}
		if value == nil {
			continue
		}
		valid := false
		switch kind {
		case "string":
			_, valid = value.(string)
		case "number":
			_, valid = value.(json.Number)
		case "bool":
			_, valid = value.(bool)
		case "object":
			_, valid = value.(map[string]any)
		case "strings":
			if values, ok := value.([]any); ok {
				valid = true
				for _, item := range values {
					if _, ok := item.(string); !ok {
						valid = false
						break
					}
				}
			}
		}
		if !valid {
			return fmt.Errorf("invalid native credential field type")
		}
	}
	return nil
}

func uniqueJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return fmt.Errorf("JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		seen := map[string]bool{}
		for decoder.More() {
			if delimiter == '{' {
				key, err := decoder.Token()
				if err != nil || seen[key.(string)] {
					return fmt.Errorf("duplicate JSON field")
				}
				seen[key.(string)] = true
			}
			if err := uniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	}
	return err
}

func (native *NativeFile) UpdateMetadata(codec, scope string, expires json.RawMessage, login bool, now time.Time, nonSecret func(string) bool) {
	oauth, _ := native.Document["claudeAiOauth"].(map[string]any)
	if scope != "" && nonSecret(scope) && codec == "claude-credentials" {
		oauth["scopes"] = strings.Fields(scope)
	}
	if codec == "codex-auth" {
		if login {
			native.Document["auth_mode"], native.Document["OPENAI_API_KEY"] = "chatgpt", nil
			delete(native.Values, "/OPENAI_API_KEY")
		}
		native.Document["last_refresh"] = now.UTC().Format(time.RFC3339)
		for _, pointer := range []string{"/tokens/access_token", "/tokens/id_token"} {
			claims, _ := JWTClaims(native.Values[pointer])
			if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
				if account, ok := auth["chatgpt_account_id"].(string); ok && nonSecret(account) {
					native.Document["tokens"].(map[string]any)["account_id"] = account
				}
			}
		}
	} else {
		delete(oauth, "expiresAt")
		var seconds int64
		if json.Unmarshal(expires, &seconds) == nil && seconds > 0 && seconds <= math.MaxInt64/int64(time.Second) {
			expiry := now.Add(time.Duration(seconds) * time.Second).UTC().Truncate(time.Millisecond)
			oauth["expiresAt"] = expiry.UnixMilli()
		}
	}
}

func (native NativeFile) ReplyMetadata(codec string) (scope string, expiry time.Time) {
	switch codec {
	case "claude-credentials":
		oauth, _ := native.Document["claudeAiOauth"].(map[string]any)
		values, _ := oauth["scopes"].([]any)
		var scopes []string
		for _, value := range values {
			scopes = append(scopes, value.(string))
		}
		scope = strings.Join(scopes, " ")
		number, _ := oauth["expiresAt"].(json.Number)
		if millis, err := number.Int64(); err == nil {
			expiry = time.UnixMilli(millis)
		}
	case "codex-auth":
		claims, _ := JWTClaims(native.Values["/tokens/access_token"])
		number, _ := claims["exp"].(json.Number)
		if seconds, err := number.Int64(); err == nil {
			expiry = time.Unix(seconds, 0)
		}
	}
	return
}

func HasClientMetadata(codec string) bool { return codec != "codex-auth" }
