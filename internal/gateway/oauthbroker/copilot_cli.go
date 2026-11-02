// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const copilotCLIClientID = "Ov23ctDVkRmgkPke0Mmm"
const copilotUserPath = "/copilot_internal/user"

func copilotEmptyBody(req *http.Request) bool {
	if req.Body == nil || req.Body == http.NoBody {
		return true
	}
	// HTTP/2 represents even an empty server body with a non-nil reader.
	// Require EOF, not merely a declared zero length, before dispatch.
	var first [1]byte
	n, err := req.Body.Read(first[:])
	if n != 0 || err != io.EOF || len(req.Trailer) != 0 {
		return false
	}
	_ = req.Body.Close()
	req.Body = http.NoBody
	return true
}

func (b *Broker) copilotLoginClientID() string {
	if b.config.Tool == "copilot" {
		return copilotCLIClientID
	}
	return copilotClientID
}

func copilotScopes(value, separator string, cli bool) bool {
	if !cli || value == "" {
		return githubScopes(value, separator)
	}
	if len(value) > 4096 {
		return false
	}
	for _, scope := range strings.Split(value, separator) {
		if scope != "write:plugin_gateway_connections" && (scope == "" || !githubScopes(scope, separator)) {
			return false
		}
	}
	return true
}

func copilotCurrentUser(ctx context.Context, token string, transport http.RoundTripper) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+copilotUserPath, nil)
	if err != nil {
		return "", ErrState
	}
	req.Header = http.Header{"Authorization": {"Bearer " + token}, "Accept": {"application/json"}, "User-Agent": {"enclave-copilot-broker"}}
	response, err := mediate(req, transport, token)
	if err != nil {
		return "", err
	}
	_, login, err := copilotUserResponse(response)
	return login, err
}

func copilotUserResponse(response *http.Response) (map[string]json.RawMessage, string, error) {
	data, err := responseBody(response)
	if err != nil || response.StatusCode != http.StatusOK {
		return nil, "", ErrResponse
	}
	var fields map[string]json.RawMessage
	var login string
	if strictJSON(data, &fields) != nil || json.Unmarshal(fields["login"], &login) != nil || !githubLogin(login) {
		return nil, "", ErrResponse
	}
	filtered := make(map[string]json.RawMessage)
	for name, value := range fields {
		switch name {
		case "access_type_sku", "analytics_tracking_id", "assigned_date", "can_signup_for_limited", "can_upgrade_plan",
			"chat_enabled", "cli_remote_control_enabled", "cloud_session_storage_enabled", "codex_agent_enabled",
			"copilot_plan", "copilotignore_enabled", "endpoints", "enterprise_list", "is_mcp_enabled", "is_staff",
			"limited_user_quotas", "limited_user_reset_date", "login", "monthly_quotas", "organization_list",
			"organization_login_list", "quota_reset_date", "quota_reset_date_utc", "quota_snapshots",
			"restricted_telemetry", "te", "token_based_billing":
			filtered[name] = value
		}
	}
	return filtered, login, nil
}

func projectCopilotUserResponse(response *http.Response) (*http.Response, error) {
	fields, _, err := copilotUserResponse(response)
	if err != nil {
		return nil, err
	}
	return projectedResponse(http.StatusOK, fields)
}

func (b *Broker) copilotNativeFiles(files map[string][]byte) error {
	grant, err := b.readCopilotGrant()
	if err != nil || grant == nil || grant.Status != "ready" {
		return err
	}
	account := map[string]string{"host": "https://github.com", "login": grant.Login}
	token := map[string]string{"token": b.copilot.Token}
	key := "https://github.com:" + grant.Login
	return addJSON(files, "config.json", map[string]any{
		"authTokens":       map[string]any{key + ":github": token, key: token},
		"loggedInUsers":    []map[string]string{{"host": "https://github.com", "login": grant.Login, "kind": "githubDotCom"}},
		"lastLoggedInUser": account,
	})
}
