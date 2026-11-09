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
)

func decodeNative(codec string, data []byte) (map[string]any, []string, error) {
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
		if err := typedFields(doc, map[string]string{"claudeAiOauth": "object"}); err != nil {
			return nil, nil, err
		}
		if oauth, exists := doc["claudeAiOauth"]; exists {
			object, ok := oauth.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("claudeAiOauth must be an object")
			}
			if err := typedFields(object, map[string]string{"accessToken": "string", "refreshToken": "string", "expiresAt": "number", "scopes": "strings", "subscriptionType": "string", "rateLimitTier": "string"}); err != nil {
				return nil, nil, err
			}
		}
		return doc, []string{"/claudeAiOauth/accessToken", "/claudeAiOauth/refreshToken"}, nil
	case "claude-config":
		if err := typedFields(doc, map[string]string{"primaryApiKey": "string", "anthropicApiKey": "string", "allowTelemetry": "bool", "hasCompletedOnboarding": "bool", "lastOnboardingVersion": "string", "forceLoginMethod": "string", "oauthAccount": "object"}); err != nil {
			return nil, nil, err
		}
		if account, ok := doc["oauthAccount"].(map[string]any); ok {
			if err := typedFields(account, map[string]string{"accountUuid": "string", "emailAddress": "string", "organizationUuid": "string", "organizationRole": "string", "workspaceRole": "string", "displayName": "string"}); err != nil {
				return nil, nil, err
			}
		}
		return doc, []string{"/primaryApiKey", "/anthropicApiKey"}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported native credential codec %q", codec)
	}
}

// Native metadata has a closed, typed schema too: a nested object must not
// become an undeclared credential channel just because its parent is allowed.
func typedFields(object map[string]any, allowed map[string]string) error {
	for key, value := range object {
		kind, ok := allowed[key]
		if !ok {
			return fmt.Errorf("unsupported native credential fields")
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
