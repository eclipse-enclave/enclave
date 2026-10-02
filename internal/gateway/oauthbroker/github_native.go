// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	yamlv2 "go.yaml.in/yaml/v2"
	"sigs.k8s.io/yaml"
)

// Native fields are from cli/cli v2.87.3 internal/config/config.go. Keyring-only
// accounts and other hosts have no import fallback: the gateway never asks the
// workload to read a real credential for it.
type githubNativeHost struct {
	User        string                      `json:"user"`
	OAuthToken  string                      `json:"oauth_token"` // #nosec G117 -- imported only from gateway-private native storage.
	GitProtocol string                      `json:"git_protocol,omitempty"`
	Users       map[string]githubNativeUser `json:"users,omitempty"`
}

type githubNativeUser struct {
	OAuthToken string `json:"oauth_token"` // #nosec G117 -- native projection replaces this with a session handle.
}

func githubLogin(value string) bool {
	if len(value) < 1 || len(value) > 39 || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func githubToken(value string) bool { return opaqueToken(value) && !ContainsHandle(value) }

func decodeGitHubNative(data []byte, dst any) error {
	decoder := yamlv2.NewDecoder(bytes.NewReader(data))
	var document any
	err := decoder.Decode(&document)
	if err != nil && err != io.EOF {
		return ErrState
	}
	if err == nil && document == nil {
		return ErrState
	}
	if err := decoder.Decode(&document); err != io.EOF {
		return ErrState
	}
	encoded, err := yaml.YAMLToJSONStrict(data)
	if err != nil || strictJSON(encoded, dst) != nil {
		return ErrState
	}
	return nil
}

func readGitHubNative(dir, name string) ([]byte, error) {
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxDocument {
		return nil, ErrState
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed native filename in a gateway-only feature mount.
	if err != nil || len(data) > maxDocument {
		return nil, ErrState
	}
	return data, nil
}

func importGitHub(config GitHubConfig) (*githubGrant, error) {
	g := &githubGrant{Schema: 1, Identity: config.Identity, Accounts: map[string]githubAccount{}, Config: map[string]any{}, Devices: map[string]githubDevice{}}
	data, err := readGitHubNative(config.StoreDir, "config.yml")
	if err != nil {
		return nil, err
	}
	if len(data) != 0 && decodeGitHubNative(data, &g.Config) != nil {
		return nil, ErrState
	}
	if g.Config == nil {
		g.Config = map[string]any{}
	}
	// gh's default config represents unset optional strings as empty YAML
	// scalars. Keep the canonical grant typed without rejecting that format.
	for _, key := range []string{"editor", "pager", "browser", "http_unix_socket"} {
		if value, exists := g.Config[key]; exists && value == nil {
			g.Config[key] = ""
		}
	}
	if err := validateGitHubPreferences(g.Config); err != nil {
		return nil, err
	}
	data, err = readGitHubNative(config.StoreDir, "hosts.yml")
	if err != nil {
		return nil, err
	}
	var hosts map[string]githubNativeHost
	if len(data) != 0 && decodeGitHubNative(data, &hosts) != nil {
		return nil, ErrState
	}
	if len(hosts) == 0 {
		return g, nil
	}
	if len(hosts) != 1 {
		return nil, ErrState
	}
	var rawHosts map[string]json.RawMessage
	if decodeGitHubNative(data, &rawHosts) != nil || exactKeys(rawHosts["github.com"], "user", "oauth_token", "git_protocol", "users") != nil {
		return nil, ErrState
	}
	var rawHost struct {
		Users map[string]json.RawMessage `json:"users"`
	}
	if json.Unmarshal(rawHosts["github.com"], &rawHost) != nil {
		return nil, ErrState
	}
	for _, user := range rawHost.Users {
		if exactKeys(user, "oauth_token") != nil {
			return nil, ErrState
		}
	}
	host, ok := hosts["github.com"]
	if !ok || !githubLogin(host.User) || len(host.Users) > githubMaxAccounts || (host.GitProtocol != "" && host.GitProtocol != "https" && host.GitProtocol != "ssh") {
		return nil, ErrState
	}
	for user, native := range host.Users {
		if !githubLogin(user) || !githubToken(native.OAuthToken) {
			return nil, ErrState
		}
		g.Accounts[user] = githubAccount{Token: native.OAuthToken}
	}
	if host.OAuthToken != "" {
		if !githubToken(host.OAuthToken) {
			return nil, ErrState
		}
		if account, exists := g.Accounts[host.User]; exists && account.Token != host.OAuthToken {
			return nil, ErrState
		}
		g.Accounts[host.User] = githubAccount{Token: host.OAuthToken}
	}
	if g.Accounts[host.User].Token == "" || len(g.Accounts) > githubMaxAccounts {
		return nil, ErrState
	}
	g.Active = host.User
	if host.GitProtocol != "" {
		g.Config["git_protocol"] = host.GitProtocol
	}
	encoded, err := yaml.Marshal(g.Config)
	if err != nil {
		return nil, ErrState
	}
	for _, account := range g.Accounts {
		if strings.Contains(string(encoded), account.Token) {
			return nil, ErrState
		}
		for user := range g.Accounts {
			if strings.Contains(user, account.Token) {
				return nil, ErrState
			}
		}
	}
	return g, nil
}

func validateGitHubPreferences(config map[string]any) error {
	if len(config) > 16 {
		return ErrState
	}
	for key, value := range config {
		switch key {
		case "git_protocol":
			if value != "https" && value != "ssh" {
				return ErrState
			}
		case "version":
			if value != "1" && value != float64(1) {
				return ErrState
			}
		case "editor", "pager", "browser", "http_unix_socket":
			text, ok := value.(string)
			if !ok || len(text) > 2048 || strings.ContainsAny(text, "\x00\r\n") || ContainsHandle(text) {
				return ErrState
			}
			// Unix-socket routing bypasses the gateway's HTTPS origin boundary.
			if key == "http_unix_socket" && text != "" {
				return ErrState
			}
		case "prompt", "prefer_editor_prompt", "color_labels", "accessible_colors", "accessible_prompter", "spinner":
			if value != "enabled" && value != "disabled" {
				return ErrState
			}
		case "aliases":
			aliases, ok := value.(map[string]any)
			if !ok || len(aliases) > 64 {
				return ErrState
			}
			for name, command := range aliases {
				text, ok := command.(string)
				if !ok || !safeText(name, 128) || !safeText(text, 4096) || ContainsHandle(text) {
					return ErrState
				}
			}
		default:
			return ErrState
		}
	}
	return nil
}

func (b *githubBroker) initializeView() error {
	return b.locked(func() error {
		if b.registry.ViewInitialized {
			return nil
		}
		g, err := b.readGrant()
		if err != nil {
			return ErrState
		}
		root, err := os.OpenRoot(b.config.ViewDir)
		if err != nil {
			return ErrState
		}
		defer func() { _ = root.Close() }()
		for _, name := range []string{"hosts.yml", "config.yml"} {
			if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return ErrState
			}
		}
		hosts := map[string]githubNativeHost{}
		if g.Active != "" {
			host := githubNativeHost{User: g.Active, OAuthToken: b.registry.Handles[g.Active], GitProtocol: "https", Users: map[string]githubNativeUser{}}
			if protocol, ok := g.Config["git_protocol"].(string); ok {
				host.GitProtocol = protocol
			}
			for user := range g.Accounts {
				host.Users[user] = githubNativeUser{OAuthToken: b.registry.Handles[user]}
			}
			hosts["github.com"] = host
		}
		for name, value := range map[string]any{"hosts.yml": hosts, "config.yml": g.Config} {
			data, err := yaml.Marshal(value)
			if err != nil {
				return ErrState
			}
			if err := writeInitialViewFile(root, b.config.ViewDir, name, data); err != nil {
				return err
			}
		}
		dir, err := root.Open(".")
		if err != nil {
			return ErrState
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return ErrState
		}
		b.registry.ViewInitialized = true
		return b.saveRegistry()
	})
}
