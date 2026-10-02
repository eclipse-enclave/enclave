// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"net/http"
	"path"
	"strings"
)

const githubOAuthHost = "github.com"
const githubAPIHost = "api.github.com"
const githubDevicePath = "/login/device/code"
const githubTokenPath = "/login/oauth/access_token" // #nosec G101 -- public OAuth endpoint.
const githubClientID = "178c6fc778ccc68e1d6a"

func githubHost(host string) bool {
	return protectedHost(host) == githubOAuthHost || protectedHost(host) == githubAPIHost
}

func (b *githubBroker) roundTrip(req *http.Request, transport, exchange http.RoundTripper) (*http.Response, bool, error) {
	if exactOrigin(req, githubOAuthHost) && (exactPath(req, githubDevicePath) || exactPath(req, githubTokenPath)) {
		resp, err := b.exchange(req, exchange)
		return resp, true, err
	}
	if githubHost(req.URL.Hostname()) || githubHost(req.Host) {
		// Other OAuth flows and ambiguous spellings must not become uncaptured
		// token endpoints. A regular github.com web page remains accessible.
		cleaned := strings.ToLower(path.Clean(req.URL.Path))
		if strings.HasPrefix(cleaned, "/login/oauth") || strings.HasPrefix(cleaned, githubDevicePath) {
			return nil, true, ErrDenied
		}
	}
	values := req.Header.Values("Authorization")
	if len(values) == 0 {
		return nil, false, nil
	}
	var handle, basicUser string
	if len(values) == 1 {
		for _, prefix := range []string{"token ", "Bearer ", "bearer "} {
			if strings.HasPrefix(values[0], prefix) {
				handle = strings.TrimPrefix(values[0], prefix)
			}
		}
		if username, password, ok := req.BasicAuth(); ok {
			if username == "" {
				return nil, true, ErrDenied
			}
			basicUser, handle = username, password
		}
	}
	if !strings.HasPrefix(handle, githubHandlePrefix) {
		if githubHost(req.URL.Hostname()) || githubHost(req.Host) {
			return nil, true, ErrDenied
		}
		return nil, false, nil
	}
	if !validHandle(handle, githubHandlePrefix) {
		return nil, true, ErrDenied
	}
	if basicUser != "" {
		if !exactOrigin(req, githubOAuthHost) || !githubGitEndpoint(req) {
			return nil, true, ErrDenied
		}
	} else if !exactOrigin(req, githubAPIHost) || !githubAPIMethod(req.Method) {
		return nil, true, ErrDenied
	}
	outgoing := req.Clone(req.Context())
	outgoing.Header.Del("Authorization")
	if containsHandle(outgoing) {
		return nil, true, ErrDenied
	}
	err := b.locked(func() error {
		g, err := b.readGrant()
		if err != nil {
			return ErrState
		}
		for user, current := range b.registry.Handles {
			if current != handle {
				continue
			}
			account, ok := g.Accounts[user]
			if !ok {
				return ErrDenied
			}
			if basicUser != "" {
				if basicUser != user && basicUser != "x-access-token" {
					return ErrDenied
				}
				outgoing.SetBasicAuth(basicUser, account.Token)
			} else {
				outgoing.Header.Set("Authorization", "Bearer "+account.Token)
			}
			return nil
		}
		return ErrDenied
	})
	if err != nil {
		return nil, true, err
	}
	resp, err := transport.RoundTrip(outgoing)
	return resp, true, err
}

func githubAPIMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func githubGitEndpoint(req *http.Request) bool {
	if req.URL.RawPath != "" || req.URL.Fragment != "" || req.URL.ForceQuery {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/"), "/")
	if len(parts) < 3 || !githubLogin(parts[0]) || !strings.HasSuffix(parts[1], ".git") {
		return false
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	if len(repo) == 0 || len(repo) > 100 || repo == "." || repo == ".." {
		return false
	}
	for _, c := range repo {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	if len(parts) == 4 && parts[2] == "info" && parts[3] == "refs" && req.Method == http.MethodGet {
		return req.URL.RawQuery == "service=git-upload-pack" || req.URL.RawQuery == "service=git-receive-pack"
	}
	return len(parts) == 3 && req.Method == http.MethodPost && req.URL.RawQuery == "" && (parts[2] == "git-upload-pack" || parts[2] == "git-receive-pack")
}
