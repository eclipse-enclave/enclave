// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const githubDeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

func githubDeviceCode(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(value) == 40 && len(decoded) == 20
}

func githubUserCode(value string) bool {
	if len(value) != 9 || value[4] != '-' {
		return false
	}
	for i, c := range value {
		if i != 4 && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func githubScopes(value, separator string) bool {
	if value == "" {
		return true
	}
	if len(value) > 4096 {
		return false
	}
	for _, scope := range strings.Split(value, separator) {
		// Closed vocabulary from GitHub's OAuth scope documentation. In
		// particular, offline_access would opt into unsupported rotating tokens.
		switch scope {
		case "repo", "repo:status", "repo_deployment", "public_repo", "repo:invite", "security_events",
			"admin:repo_hook", "write:repo_hook", "read:repo_hook", "admin:org", "write:org", "read:org",
			"admin:public_key", "write:public_key", "read:public_key", "admin:org_hook", "gist", "notifications",
			"user", "read:user", "user:email", "user:follow", "project", "read:project", "delete_repo",
			"write:packages", "read:packages", "delete:packages", "admin:gpg_key", "write:gpg_key", "read:gpg_key",
			"codespace", "workflow", "read:audit_log":
		default:
			return false
		}
	}
	return true
}

func githubForm(req *http.Request) (url.Values, error) {
	if req.Method != http.MethodPost || req.Body == nil || req.ContentLength > maxDocument || len(req.Trailer) != 0 || containsHandle(req) || req.Header.Get("Content-Encoding") != "" || len(req.Header.Values("Content-Type")) != 1 {
		return nil, ErrDenied
	}
	media, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		return nil, ErrDenied
	}
	for key, value := range params {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return nil, ErrDenied
		}
	}
	defer func() { _ = req.Body.Close() }()
	data, err := boundedRead(req.Body)
	if err != nil || (req.ContentLength >= 0 && req.ContentLength != int64(len(data))) {
		return nil, ErrDenied
	}
	form, err := url.ParseQuery(string(data))
	if err != nil || form.Get("client_id") != githubClientID {
		return nil, ErrDenied
	}
	for _, values := range form {
		if len(values) != 1 {
			return nil, ErrDenied
		}
	}
	if req.URL.Path == githubDevicePath {
		if len(form) != 2 || form.Get("scope") == "" || !githubScopes(form.Get("scope"), " ") {
			return nil, ErrDenied
		}
	} else if len(form) != 3 || form.Get("grant_type") != githubDeviceGrant || !githubDeviceCode(form.Get("device_code")) {
		return nil, ErrDenied
	}
	return form, nil
}

func githubDispatch(req *http.Request, form url.Values, transport http.RoundTripper) (*http.Response, []byte, error) {
	outgoing := req.Clone(req.Context())
	payload := form.Encode()
	outgoing.Body = io.NopCloser(strings.NewReader(payload))
	outgoing.ContentLength = int64(len(payload))
	outgoing.GetBody = nil
	outgoing.Header = http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Accept": {"application/json"}, "Accept-Encoding": {"identity"}}
	response, err := transport.RoundTrip(outgoing)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, nil, ErrUncertain
	}
	data, err := responseBody(response)
	return response, data, err
}

func (b *githubBroker) exchange(req *http.Request, transport http.RoundTripper) (*http.Response, error) {
	form, err := githubForm(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(req.Context(), exchangeTimeout)
	defer cancel()
	req = req.Clone(ctx)
	if req.URL.Path == githubDevicePath {
		response, body, err := githubDispatch(req, form, transport)
		if err != nil {
			return nil, err
		}
		return githubDeviceResponse(response, body)
	}
	var projected *http.Response
	err = b.locked(func() error {
		if req.Context().Err() != nil {
			return ErrDenied
		}
		g, err := b.readGrant()
		if err != nil {
			return ErrState
		}
		now := b.now()
		if retireGitHubDevices(g, now) {
			if err := b.saveGrant(g); err != nil {
				return err
			}
		}
		digest := sha256.Sum256([]byte(form.Get("device_code")))
		key := hex.EncodeToString(digest[:])
		if device, exists := g.Devices[key]; exists {
			if device.User == "" {
				return ErrUncertain
			}
			if device.Session != b.config.Session || device.Incarnation != b.config.Incarnation {
				return ErrDenied
			}
			projected, err = b.tokenResponse(g, device.User)
			return err
		}
		// Bounded replay history stores only device-code hashes. Pending entries
		// survive crashes and block redispatch until their code has expired; a
		// new device login can proceed.
		if len(g.Devices) >= githubMaxDevices {
			return ErrState
		}
		g.Devices[key] = githubDevice{Session: b.config.Session, Incarnation: b.config.Incarnation, Created: now.Unix()}
		if err := b.saveGrant(g); err != nil {
			return err
		}
		response, body, err := githubDispatch(req, form, transport)
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if strictJSON(body, &fields) != nil {
			return ErrResponse
		}
		if _, isError := fields["error"]; isError {
			projected, err = githubErrorResponse(response, body)
			if err != nil {
				return err
			}
			delete(g.Devices, key)
			return b.saveGrant(g)
		}
		var token struct {
			Access string `json:"access_token"` // #nosec G117 -- captured provider token is never forwarded to the workload.
			Type   string `json:"token_type"`
			Scope  string `json:"scope"`
		}
		if response.StatusCode != http.StatusOK || len(fields) != 3 || strictJSON(body, &token) != nil || exactKeys(body, "access_token", "token_type", "scope") != nil || !githubToken(token.Access) || token.Type != "bearer" || !githubScopes(token.Scope, ",") || strings.Contains(token.Scope, token.Access) {
			return ErrResponse
		}
		user, err := githubCurrentUser(req.Context(), token.Access, transport)
		if err != nil {
			return err
		}
		if _, exists := g.Accounts[user]; !exists && len(g.Accounts) >= githubMaxAccounts {
			return ErrState
		}
		if err := b.ensureHandle(user); err != nil {
			return err
		}
		if err := b.saveRegistry(); err != nil {
			return err
		}
		g.Accounts[user] = githubAccount{Token: token.Access, Scope: token.Scope}
		g.Active = user
		g.Devices[key] = githubDevice{Session: b.config.Session, Incarnation: b.config.Incarnation, User: user, Created: now.Unix()}
		if err := b.saveGrant(g); err != nil {
			return err
		}
		projected, err = b.tokenResponse(g, user)
		return err
	})
	if err != nil {
		if projected != nil && projected.Body != nil {
			_ = projected.Body.Close()
		}
		return nil, err
	}
	return projected, nil
}

func (b *githubBroker) tokenResponse(g *githubGrant, user string) (*http.Response, error) {
	handle := b.registry.Handles[user]
	if handle == "" || g.Accounts[user].Token == "" {
		return nil, ErrState
	}
	return projectedResponse(http.StatusOK, map[string]string{"access_token": handle, "token_type": "bearer", "scope": g.Accounts[user].Scope})
}

func githubCurrentUser(ctx context.Context, token string, transport http.RoundTripper) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", ErrState
	}
	req.Header = http.Header{"Authorization": {"Bearer " + token}, "Accept": {"application/json"}, "Accept-Encoding": {"identity"}, "User-Agent": {"enclave-github-broker"}}
	response, err := transport.RoundTrip(req)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return "", ErrUncertain
	}
	data, err := responseBody(response)
	if err != nil || response.StatusCode != http.StatusOK {
		return "", ErrResponse
	}
	var fields map[string]json.RawMessage
	var user string
	// The profile is not forwarded: only its bounded login is persisted. Other
	// GitHub profile fields are deliberately neither trusted nor projected.
	if strictJSON(data, &fields) != nil || json.Unmarshal(fields["login"], &user) != nil || !githubLogin(user) || strings.Contains(user, token) {
		return "", ErrResponse
	}
	return user, nil
}

func githubDeviceResponse(response *http.Response, data []byte) (*http.Response, error) {
	var fields map[string]json.RawMessage
	if strictJSON(data, &fields) != nil {
		return nil, ErrResponse
	}
	if _, isError := fields["error"]; isError {
		return githubErrorResponse(response, data)
	}
	var device struct {
		Device   string `json:"device_code"`
		User     string `json:"user_code"`
		URI      string `json:"verification_uri"`
		Expires  int    `json:"expires_in"`
		Interval int    `json:"interval"`
	}
	if response.StatusCode != http.StatusOK || strictJSON(data, &device) != nil || exactKeys(data, "device_code", "user_code", "verification_uri", "expires_in", "interval") != nil || !githubDeviceCode(device.Device) || !githubUserCode(device.User) || device.URI != "https://github.com/login/device" || device.Expires < 1 || device.Expires > int(githubMaxDeviceLifetime/time.Second) || device.Interval < 1 || device.Interval > 60 {
		return nil, ErrResponse
	}
	return projectedResponse(http.StatusOK, device)
}

func githubErrorResponse(response *http.Response, data []byte) (*http.Response, error) {
	var value struct {
		Error       string `json:"error"`
		Description string `json:"error_description,omitempty"`
		URI         string `json:"error_uri,omitempty"`
		Interval    int    `json:"interval,omitempty"`
	}
	if strictJSON(data, &value) != nil || exactKeys(data, "error", "error_description", "error_uri", "interval") != nil || value.Interval < 0 || value.Interval > 3600 {
		return nil, ErrResponse
	}
	switch response.StatusCode {
	case 200, 400, 401, 403, 429:
	default:
		return nil, ErrResponse
	}
	switch value.Error {
	case "authorization_pending", "slow_down", "expired_token", "access_denied", "bad_verification_code", "incorrect_client_credentials", "incorrect_device_code", "device_flow_disabled", "unsupported_grant_type":
	default:
		return nil, ErrResponse
	}
	value.Description, value.URI = "", ""
	return projectedResponse(response.StatusCode, value)
}
