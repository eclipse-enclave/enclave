// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package spec

import (
	"encoding/json"
	"fmt"

	"enclave/internal/model"
)

// specDocument is the on-disk schema of a filter's spec.yaml. sigs.k8s.io/yaml
// uses the json tags, which keys must match exactly (see decodeYAML). Fields whose YAML shape varies are raw JSON and parsed
// during validation, so errors can name the field.
type specDocument struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Kind          string                    `json:"kind"`
	Name          string                    `json:"name"`
	DisplayName   string                    `json:"displayName,omitempty"`
	Description   string                    `json:"description,omitempty"`
	DefaultPreset string                    `json:"defaultPreset,omitempty"`
	Hosts         []string                  `json:"hosts"`
	Confine       []string                  `json:"confine,omitempty"`
	Credential    *specCredential           `json:"credential"`
	JSONKeys      string                    `json:"jsonKeys,omitempty"`
	Resolvers     map[string]specResolver   `json:"resolvers,omitempty"`
	Counters      map[string]specCounter    `json:"counters,omitempty"`
	Capabilities  map[string]specCapability `json:"capabilities"`
	Routes        []specRoute               `json:"routes"`
	Deny          *specDeny                 `json:"deny,omitempty"`
}

type specCredential struct {
	OAuth2      *specOAuth2                    `json:"oauth2,omitempty"`
	Static      *specStatic                    `json:"static,omitempty"`
	Inject      *specInject                    `json:"inject"`
	Env         []string                       `json:"env"`
	Placeholder *model.SecretPlaceholderConfig `json:"placeholder,omitempty"`
	DenyHeaders []string                       `json:"denyHeaders,omitempty"`
	DenyParams  []string                       `json:"denyParams,omitempty"`
}

type specOAuth2 struct {
	AuthURL      string              `json:"authUrl"`
	TokenURL     string              `json:"tokenUrl"`
	RevokeURL    string              `json:"revokeUrl,omitempty"`
	AuthParams   map[string]string   `json:"authParams,omitempty"`
	ScopePrefix  string              `json:"scopePrefix,omitempty"`
	ScopeImplies map[string][]string `json:"scopeImplies,omitempty"`
	ClientHelp   string              `json:"clientHelp,omitempty"`
}

type specStatic struct {
	Env      []string                `json:"env,omitempty"`
	File     *model.SecretFileSource `json:"file,omitempty"`
	Priority string                  `json:"priority,omitempty"`
}

type specInject struct {
	Header string `json:"header"`
	Format string `json:"format,omitempty"`
}

type specResolver struct {
	URL       string   `json:"url"`
	Extract   string   `json:"extract"`
	Recursive bool     `json:"recursive,omitempty"`
	MaxDepth  int      `json:"maxDepth,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
}

type specCounter struct {
	Description string `json:"description,omitempty"`
	Unit        string `json:"unit,omitempty"`
}

type specCapability struct {
	Description string                    `json:"description,omitempty"`
	Public      bool                      `json:"public,omitempty"`
	Scopes      []string                  `json:"scopes,omitempty"`
	Constraints map[string]specConstraint `json:"constraints,omitempty"`
}

type specConstraint struct {
	Kind     string          `json:"kind"`
	Var      string          `json:"var"`
	Resolver string          `json:"resolver,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
}

type specRoute struct {
	Op               string                       `json:"op"`
	Action           string                       `json:"action,omitempty"`
	Reason           string                       `json:"reason,omitempty"`
	Capability       string                       `json:"capability,omitempty"`
	Method           stringList                   `json:"method"`
	Host             stringList                   `json:"host"`
	Path             string                       `json:"path"`
	Query            map[string]json.RawMessage   `json:"query,omitempty"`
	QueryMatch       map[string]string            `json:"queryMatch,omitempty"`
	JSON             map[string]json.RawMessage   `json:"json,omitempty"`
	JSONPresent      stringList                   `json:"jsonPresent,omitempty"`
	JSONMatch        map[string]string            `json:"jsonMatch,omitempty"`
	JSONAllowedKeys  map[string][]string          `json:"jsonAllowedKeys,omitempty"`
	WildcardOnly     stringList                   `json:"wildcardOnly,omitempty"`
	Bind             map[string][]json.RawMessage `json:"bind,omitempty"`
	Count            []specCount                  `json:"count,omitempty"`
	AllowFormBody    bool                         `json:"allowFormBody,omitempty"`
	MultipartParts   int                          `json:"multipartParts,omitempty"`
	MaxBodyBytes     int64                        `json:"maxBodyBytes,omitempty"`
	MaxResponseBytes int64                        `json:"maxResponseBytes,omitempty"`
}

type specCount struct {
	Counter string `json:"counter"`
	Key     string `json:"key,omitempty"`
}

type specDeny struct {
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

// presetDocument is the on-disk schema of presets/<name>.yaml.
type presetDocument struct {
	Description string                     `json:"description,omitempty"`
	Extends     string                     `json:"extends,omitempty"`
	Allow       map[string]json.RawMessage `json:"allow,omitempty"`
	Deny        []string                   `json:"deny,omitempty"`
	Quotas      map[string]int64           `json:"quotas,omitempty"`
}

// stringList accepts a YAML string or a list of strings.
type stringList []string

func (s *stringList) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("must be a string or a list of strings")
	}
	*s = many
	return nil
}
