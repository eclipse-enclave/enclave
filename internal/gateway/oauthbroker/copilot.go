// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

// OpenCode signs in to GitHub Copilot with a GitHub.com device login of its
// own OAuth client and sends the resulting GitHub token to the Copilot API.
const (
	copilotClientID     = "Ov23li8tweQw6odWQebz"
	copilotDomain       = "githubcopilot.com"
	copilotRegistryFile = "copilot-session.json"
	// copilotHandlePrefix extends the GitHub handle prefix, so the GitHub CLI
	// adapter refuses a Copilot handle on GitHub's own hosts.
	copilotHandlePrefix = githubHandlePrefix + "copilot_"
)

var copilotGrantFile = protectedauth.GrantFile(protectedauth.AdapterCopilot)

type copilotGrant struct {
	Schema      int       `json:"schema"`
	Identity    string    `json:"identity"`
	Status      string    `json:"status"`
	CommittedAt time.Time `json:"committedAt"`
	Token       string    `json:"token,omitempty"` // #nosec G117 -- gateway-private grant state; projections carry handles.
	Scope       string    `json:"scope,omitempty"`
}

type copilotRegistry struct {
	Schema      int    `json:"schema"`
	Identity    string `json:"identity"`
	Session     string `json:"session"`
	Incarnation string `json:"incarnation"`
	Token       string `json:"token"`
}

type copilotAdapter struct{ b *Broker }

func (b *Broker) openCopilot() error {
	path := filepath.Join(b.config.RuntimeDir, copilotRegistryFile)
	var r copilotRegistry
	if err := readPrivateJSON(path, &r); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return ErrState
		}
		var value [32]byte
		if _, err := rand.Read(value[:]); err != nil {
			return ErrState
		}
		r = copilotRegistry{Schema: 1, Identity: b.config.Identity, Session: b.config.Session, Incarnation: b.config.Incarnation, Token: copilotHandlePrefix + hex.EncodeToString(value[:])}
	}
	if r.Schema != 1 || r.Identity != b.config.Identity || r.Session != b.config.Session || r.Incarnation != b.config.Incarnation || !validHandle(r.Token, copilotHandlePrefix) {
		return ErrState
	}
	if err := b.write(path, r); err != nil {
		return ErrState
	}
	b.copilot = &r
	if g, err := b.readCopilotGrant(); err != nil {
		return err
	} else if g != nil {
		return b.writeCopilotGrant(g)
	}
	return nil
}

func (b *Broker) readCopilotGrant() (*copilotGrant, error) {
	var g copilotGrant
	if err := readPrivateJSON(filepath.Join(b.config.StoreDir, copilotGrantFile), &g); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, ErrState
	}
	if g.Schema != 1 || g.Identity != b.config.Identity || (g.Status != "ready" && g.Status != "uncertain") ||
		(g.Status == "ready" && (!githubToken(g.Token) || g.CommittedAt.Unix() <= 0 || !githubScopes(g.Scope, ","))) {
		return nil, ErrState
	}
	if err := util.SyncDir(b.config.StoreDir); err != nil {
		return nil, ErrState
	}
	return &g, nil
}

func (b *Broker) writeCopilotGrant(g *copilotGrant) error {
	if err := b.write(filepath.Join(b.config.StoreDir, copilotGrantFile), g); err != nil {
		b.failed = true
		return ErrState
	}
	return nil
}

func copilotHost(host string) bool {
	host = protectedHost(host)
	return host == copilotDomain || strings.HasSuffix(host, "."+copilotDomain)
}

func (a copilotAdapter) roundTrip(req *http.Request, transport, exchange http.RoundTripper) (*http.Response, bool, error) {
	b := a.b
	if exactOrigin(req, githubOAuthHost) && (exactPath(req, githubDevicePath) || exactPath(req, githubTokenPath)) {
		form, ok, err := copilotForm(req)
		if !ok {
			// Another client's login, such as the GitHub CLI's.
			return nil, false, nil
		}
		if err != nil {
			return nil, true, err
		}
		response, err := b.copilotExchange(req, form, exchange)
		return response, true, err
	}
	authorization := req.Header.Values("Authorization")
	if len(authorization) != 1 || subtle.ConstantTimeCompare([]byte(authorization[0]), []byte("Bearer "+b.copilot.Token)) != 1 {
		return nil, false, nil
	}
	host := req.URL.Hostname()
	if req.URL.Scheme != "https" || (req.URL.Port() != "" && req.URL.Port() != "443") || !copilotHost(host) || (req.Host != "" && protectedHost(req.Host) != protectedHost(req.URL.Host) && protectedHost(req.Host) != protectedHost(host)) ||
		(req.TLS != nil && req.TLS.ServerName != "" && protectedHost(req.TLS.ServerName) != protectedHost(host)) {
		return nil, true, ErrDenied
	}
	outgoing := req.Clone(req.Context())
	outgoing.Header.Del("Authorization")
	if containsHandle(outgoing) {
		return nil, true, ErrDenied
	}
	var token string
	err := b.locked(func() error {
		g, err := b.readCopilotGrant()
		if err != nil {
			return err
		}
		if g == nil {
			return ErrDenied
		}
		if g.Status != "ready" {
			return ErrUncertain
		}
		token = g.Token
		return nil
	})
	if err != nil {
		return nil, true, err
	}
	outgoing.Header.Set("Authorization", "Bearer "+token)
	response, err := mediate(outgoing, transport, token)
	return response, true, err
}

// copilotForm parses a device login request. ok reports whether it belongs to
// the Copilot client; err rejects one that does but is malformed.
func copilotForm(req *http.Request) (url.Values, bool, error) {
	if req.Method != http.MethodPost || req.Body == nil || req.ContentLength > maxDocument || len(req.Trailer) != 0 || req.Header.Get("Content-Encoding") != "" || len(req.Header.Values("Content-Type")) != 1 {
		return nil, false, nil
	}
	data, err := boundedRead(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, true, ErrDenied
	}
	// Later adapters read the same body.
	req.Body = io.NopCloser(bytes.NewReader(data))
	media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		return nil, false, nil
	}
	form := url.Values{}
	switch media {
	case "application/json":
		var fields map[string]string
		if uniqueJSON(data) != nil || json.Unmarshal(data, &fields) != nil {
			return nil, false, nil
		}
		for key, value := range fields {
			form.Set(key, value)
		}
	case "application/x-www-form-urlencoded":
		if form, err = url.ParseQuery(string(data)); err != nil {
			return nil, false, nil
		}
	default:
		return nil, false, nil
	}
	if form.Get("client_id") != copilotClientID {
		return nil, false, nil
	}
	for _, values := range form {
		if len(values) != 1 {
			return nil, true, ErrDenied
		}
	}
	if containsHandle(req) {
		return nil, true, ErrDenied
	}
	if req.URL.Path == githubDevicePath {
		if len(form) != 2 || !githubScopes(form.Get("scope"), " ") {
			return nil, true, ErrDenied
		}
	} else if len(form) != 3 || form.Get("grant_type") != githubDeviceGrant || !githubDeviceCode(form.Get("device_code")) {
		return nil, true, ErrDenied
	}
	return form, true, nil
}

func (b *Broker) copilotExchange(req *http.Request, form url.Values, transport http.RoundTripper) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), exchangeTimeout)
	defer cancel()
	req = req.Clone(ctx)
	if req.URL.Path == githubDevicePath {
		// The device code is bootstrap material the client needs; no credential.
		response, body, err := githubDispatch(req, form, transport)
		if err != nil {
			return nil, err
		}
		return githubDeviceResponse(response, body)
	}
	var projected *http.Response
	err := b.locked(func() error {
		g, err := b.readCopilotGrant()
		if err != nil {
			return err
		}
		if g != nil && g.Status == "ready" {
			return ErrDenied // Fresh logins only; reset the identity to switch.
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
			return err
		}
		var token struct {
			Access string `json:"access_token"` // #nosec G117 -- captured provider token is never forwarded to the workload.
			Type   string `json:"token_type"`
			Scope  string `json:"scope"`
		}
		if response.StatusCode != http.StatusOK || len(fields) != 3 || strictJSON(body, &token) != nil || exactKeys(body, "access_token", "token_type", "scope") != nil ||
			!githubToken(token.Access) || token.Type != "bearer" || !githubScopes(token.Scope, ",") || strings.Contains(token.Scope, token.Access) {
			// The device code is consumed either way; a new login can follow.
			_ = b.writeCopilotGrant(&copilotGrant{Schema: 1, Identity: b.config.Identity, Status: "uncertain"})
			return ErrResponse
		}
		if err := b.writeCopilotGrant(&copilotGrant{Schema: 1, Identity: b.config.Identity, Status: "ready", CommittedAt: b.now().UTC(), Token: token.Access, Scope: token.Scope}); err != nil {
			return err
		}
		projected, err = projectedResponse(http.StatusOK, map[string]string{"access_token": b.copilot.Token, "token_type": "bearer", "scope": token.Scope})
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

// copilotReady reports the session handle when a Copilot grant is ready.
func (b *Broker) copilotReady() (string, error) {
	if b.copilot == nil {
		return "", nil
	}
	g, err := b.readCopilotGrant()
	if err != nil || g == nil || g.Status != "ready" {
		return "", err
	}
	return b.copilot.Token, nil
}
