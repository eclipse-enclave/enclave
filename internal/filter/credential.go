// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"fmt"
	"net/url"
)

// Credential payload identity.
const (
	CredentialSchemaVersion = "1"
	CredentialKind          = "filter-credential" // #nosec G101 -- document kind, not a credential.
)

// Credential is the secret material the gateway needs for one filter. The
// host writes it next to the policy, never into the tool container. Exactly
// one of Static and OAuth2 is set.
//
// String and GoString redact the content so a stray %v cannot log secrets.
type Credential struct {
	SchemaVersion string            `json:"schemaVersion"`
	Kind          string            `json:"kind"`
	Filter        string            `json:"filter"`
	Static        *StaticCredential `json:"static,omitempty"`
	OAuth2        *OAuth2Credential `json:"oauth2,omitempty"`
}

// StaticCredential is a fixed secret value.
type StaticCredential struct {
	Value string `json:"value"`
}

// OAuth2Credential mints access tokens with the refresh token grant.
type OAuth2Credential struct {
	TokenURL     string   `json:"tokenUrl"`
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret,omitempty"`
	RefreshToken string   `json:"refreshToken"`
	Grant        string   `json:"grant,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// NewStaticCredential builds a static credential payload.
func NewStaticCredential(filterName, value string) *Credential {
	return &Credential{
		SchemaVersion: CredentialSchemaVersion,
		Kind:          CredentialKind,
		Filter:        filterName,
		Static:        &StaticCredential{Value: value},
	}
}

// NewOAuth2Credential builds an OAuth2 credential payload.
func NewOAuth2Credential(filterName string, c OAuth2Credential) *Credential {
	return &Credential{
		SchemaVersion: CredentialSchemaVersion,
		Kind:          CredentialKind,
		Filter:        filterName,
		OAuth2:        &c,
	}
}

// Type returns CredentialStatic or CredentialOAuth2.
func (c *Credential) Type() string {
	if c.OAuth2 != nil {
		return CredentialOAuth2
	}
	return CredentialStatic
}

// String redacts the credential.
func (c *Credential) String() string {
	if c == nil {
		return "<nil filter credential>"
	}
	return fmt.Sprintf("filter credential %s (%s, redacted)", c.Filter, c.Type())
}

// GoString redacts the credential.
func (c *Credential) GoString() string { return c.String() }

// String redacts the value.
func (StaticCredential) String() string { return "static credential (redacted)" }

// GoString redacts the value.
func (s StaticCredential) GoString() string { return s.String() }

// String redacts the secrets.
func (o OAuth2Credential) String() string {
	return fmt.Sprintf("oauth2 credential for %s (redacted)", o.TokenURL)
}

// GoString redacts the secrets.
func (o OAuth2Credential) GoString() string { return o.String() }

// ParseCredential decodes and validates a credential payload.
func ParseCredential(data []byte) (*Credential, error) {
	var c Credential
	if err := decodeStrict(data, &c); err != nil {
		// The decoder error can quote input, which is secret here.
		return nil, fmt.Errorf("parse filter credential: invalid document")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// LoadCredential reads and validates a credential payload file.
func LoadCredential(path string) (*Credential, error) {
	data, err := readLimited(path)
	if err != nil {
		return nil, fmt.Errorf("read filter credential: %w", err)
	}
	return ParseCredential(data)
}

// Validate checks the payload. Messages never quote secret fields.
func (c *Credential) Validate() error {
	if c.SchemaVersion != CredentialSchemaVersion {
		return fmt.Errorf("filter credential: schemaVersion must be %q", CredentialSchemaVersion)
	}
	if c.Kind != CredentialKind {
		return fmt.Errorf("filter credential: kind must be %q", CredentialKind)
	}
	if err := ValidateName(c.Filter); err != nil {
		return fmt.Errorf("filter credential: filter: %w", err)
	}
	if (c.Static == nil) == (c.OAuth2 == nil) {
		return fmt.Errorf("filter credential %s: exactly one of static and oauth2 is required", c.Filter)
	}
	if c.Static != nil {
		if err := validateSecretValue(c.Static.Value); err != nil {
			return fmt.Errorf("filter credential %s: static.value %w", c.Filter, err)
		}
		return nil
	}
	o := c.OAuth2
	if err := ValidateHTTPSURL(o.TokenURL); err != nil {
		return fmt.Errorf("filter credential %s: oauth2.tokenUrl: %w", c.Filter, err)
	}
	if err := validateSecretValue(o.ClientID); err != nil {
		return fmt.Errorf("filter credential %s: oauth2.clientId %w", c.Filter, err)
	}
	if err := validateSecretValue(o.RefreshToken); err != nil {
		return fmt.Errorf("filter credential %s: oauth2.refreshToken %w", c.Filter, err)
	}
	if o.ClientSecret != "" {
		if err := validateSecretValue(o.ClientSecret); err != nil {
			return fmt.Errorf("filter credential %s: oauth2.clientSecret %w", c.Filter, err)
		}
	}
	return nil
}

func validateSecretValue(value string) error {
	if value == "" {
		return fmt.Errorf("is required")
	}
	if hasControl(value) {
		return fmt.Errorf("contains control characters")
	}
	return nil
}

// ValidateHTTPSURL checks an absolute HTTPS URL without user info or fragment.
func ValidateHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q", raw)
	}
	if u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%q must be an https URL", raw)
	}
	if u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%q must not contain user info or a fragment", raw)
	}
	return nil
}

// CheckCredential reports whether cred belongs to the policy.
func (p *Policy) CheckCredential(cred *Credential) error {
	if cred == nil {
		return fmt.Errorf("filter %s: credential is missing", p.Filter)
	}
	if cred.Filter != p.Filter {
		return fmt.Errorf("filter %s: credential belongs to filter %s", p.Filter, cred.Filter)
	}
	if cred.Type() != p.CredentialType {
		return fmt.Errorf("filter %s: credential type %s does not match policy type %s", p.Filter, cred.Type(), p.CredentialType)
	}
	return nil
}
