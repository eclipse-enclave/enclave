// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testClientSecret = "client-secret-value-123"
	testRefreshToken = "1//refresh-token-value-456"
	testStaticValue  = "static-secret-value-789"
)

func validOAuth2Credential() *Credential {
	return NewOAuth2Credential("gws", OAuth2Credential{
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     "client-id.apps.googleusercontent.com",
		ClientSecret: testClientSecret,
		RefreshToken: testRefreshToken,
		Grant:        "readonly",
		Scopes:       []string{"https://www.googleapis.com/auth/gmail.readonly"},
	})
}

func TestCredentialRoundTrip(t *testing.T) {
	for _, cred := range []*Credential{validOAuth2Credential(), NewStaticCredential("gws", testStaticValue)} {
		data, err := json.Marshal(cred)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "credential.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadCredential(path)
		if err != nil {
			t.Fatalf("LoadCredential: %v", err)
		}
		if loaded.Type() != cred.Type() || loaded.Filter != "gws" {
			t.Fatalf("loaded = %v", loaded)
		}
	}
}

func TestCredentialValidateErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Credential)
		want   string
	}{
		{"schema", func(c *Credential) { c.SchemaVersion = "" }, "schemaVersion"},
		{"kind", func(c *Credential) { c.Kind = "x" }, "kind"},
		{"filter", func(c *Credential) { c.Filter = "" }, "filter"},
		{"neither", func(c *Credential) { c.OAuth2 = nil }, "exactly one"},
		{"both", func(c *Credential) { c.Static = &StaticCredential{Value: "x"} }, "exactly one"},
		{"token url scheme", func(c *Credential) { c.OAuth2.TokenURL = "http://oauth2.googleapis.com/token" }, "tokenUrl"},
		{"token url user", func(c *Credential) { c.OAuth2.TokenURL = "https://u:p@oauth2.googleapis.com/token" }, "tokenUrl"},
		{"client id", func(c *Credential) { c.OAuth2.ClientID = "" }, "clientId is required"},
		{"refresh token", func(c *Credential) { c.OAuth2.RefreshToken = "" }, "refreshToken is required"},
		{"refresh token newline", func(c *Credential) { c.OAuth2.RefreshToken = "a\nb" }, "refreshToken contains control"},
		{"client secret newline", func(c *Credential) { c.OAuth2.ClientSecret = "a\rb" }, "clientSecret contains control"},
		{"static empty", func(c *Credential) { c.OAuth2 = nil; c.Static = &StaticCredential{} }, "static.value is required"},
		{"static newline", func(c *Credential) { c.OAuth2 = nil; c.Static = &StaticCredential{Value: "a\nX-Evil: 1"} }, "static.value contains control"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validOAuth2Credential()
			tt.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want %q", err, tt.want)
			}
		})
	}
}

// I1 and I12: neither formatting nor parse errors reveal secret fields.
func TestCredentialNeverPrintsSecrets(t *testing.T) {
	oauth := validOAuth2Credential()
	static := NewStaticCredential("gws", testStaticValue)
	printed := strings.Join([]string{
		fmt.Sprint(oauth), fmt.Sprintf("%v %+v %#v %s", oauth, oauth, oauth, oauth),
		fmt.Sprintf("%v %+v %#v", *oauth.OAuth2, *oauth.OAuth2, *oauth.OAuth2),
		fmt.Sprintf("%v %+v %#v", static, static, static), fmt.Sprintf("%v %#v", *static.Static, *static.Static),
	}, "\n")
	for _, secret := range []string{testClientSecret, testRefreshToken, testStaticValue} {
		if strings.Contains(printed, secret) {
			t.Fatalf("formatting leaked %q:\n%s", secret, printed)
		}
	}

	data, _ := json.Marshal(oauth)
	broken := strings.Replace(string(data), `"refreshToken"`, `"refreshToken":"`+testRefreshToken+`","refreshTokenX"`, 1)
	_, err := ParseCredential([]byte(broken))
	if err == nil || strings.Contains(err.Error(), testRefreshToken) || strings.Contains(err.Error(), testClientSecret) {
		t.Fatalf("ParseCredential error = %v", err)
	}
	bad := validOAuth2Credential()
	bad.OAuth2.RefreshToken = testRefreshToken + "\n"
	if err := bad.Validate(); err == nil || strings.Contains(err.Error(), testRefreshToken) {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestPolicyCheckCredential(t *testing.T) {
	p := validPolicy()
	if err := p.CheckCredential(validOAuth2Credential()); err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	if err := p.CheckCredential(nil); err == nil {
		t.Fatal("nil credential accepted")
	}
	if err := p.CheckCredential(NewStaticCredential("gws", "x")); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("type mismatch = %v", err)
	}
	other := validOAuth2Credential()
	other.Filter = "other"
	if err := p.CheckCredential(other); err == nil || !strings.Contains(err.Error(), "belongs to filter other") {
		t.Fatalf("filter mismatch = %v", err)
	}
}
