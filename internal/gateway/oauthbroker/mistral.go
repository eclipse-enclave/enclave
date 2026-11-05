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
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

// Mistral Vibe's browser sign-in returns an API key from its final exchange.
// The gateway keeps the key and gives Vibe a handle in its place.
const (
	mistralConsoleHost  = "console.mistral.ai"
	mistralRegistryFile = "mistral-session.json"
	mistralHandleMarker = "ENCLAVEmis"
)

var mistralGrantFile = protectedauth.GrantFile(protectedauth.AdapterMistral)

// mistralReleaseHosts receive the key as a Bearer token: the API, account
// lookup and managed configuration.
var mistralReleaseHosts = []string{"api.mistral.ai", mistralConsoleHost, "chat.mistral.ai"}

var mistralExchangePath = regexp.MustCompile(`^/api/vibe/sign-in/[A-Za-z0-9_-]{1,128}/exchange$`)

type mistralGrant struct {
	Schema      int       `json:"schema"`
	Identity    string    `json:"identity"`
	Status      string    `json:"status"`
	CommittedAt time.Time `json:"committedAt"`
	Key         string    `json:"key,omitempty"` // #nosec G117 -- gateway-private grant state; projections carry handles.
}

type mistralRegistry struct {
	Schema      int    `json:"schema"`
	Identity    string `json:"identity"`
	Session     string `json:"session"`
	Incarnation string `json:"incarnation"`
	Key         string `json:"key"`
}

type mistralAdapter struct{ b *Broker }

func (b *Broker) openMistral() error {
	path := filepath.Join(b.config.RuntimeDir, mistralRegistryFile)
	var r mistralRegistry
	if err := readPrivateJSON(path, &r); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return ErrState
		}
		value, err := randomHandle(mistralHandleMarker)
		if err != nil {
			return ErrState
		}
		r = mistralRegistry{Schema: 1, Identity: b.config.Identity, Session: b.config.Session, Incarnation: b.config.Incarnation, Key: value}
	}
	if r.Schema != 1 || r.Identity != b.config.Identity || r.Session != b.config.Session || r.Incarnation != b.config.Incarnation || !validHandle(r.Key, mistralHandleMarker) {
		return ErrState
	}
	if err := b.write(path, r); err != nil {
		return ErrState
	}
	b.mistral = &r
	if g, err := b.readMistralGrant(); err != nil {
		return err
	} else if g != nil {
		return b.writeMistralGrant(g)
	}
	return nil
}

func (b *Broker) readMistralGrant() (*mistralGrant, error) {
	var g mistralGrant
	if err := readPrivateJSON(filepath.Join(b.config.StoreDir, mistralGrantFile), &g); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, ErrState
	}
	if g.Schema != 1 || g.Identity != b.config.Identity || (g.Status != "ready" && g.Status != "uncertain") || (g.Status == "ready" && (!mistralKey(g.Key) || g.CommittedAt.Unix() <= 0)) {
		return nil, ErrState
	}
	if err := util.SyncDir(b.config.StoreDir); err != nil {
		return nil, ErrState
	}
	return &g, nil
}

func (b *Broker) writeMistralGrant(g *mistralGrant) error {
	if err := b.write(filepath.Join(b.config.StoreDir, mistralGrantFile), g); err != nil {
		b.failed = true
		return ErrState
	}
	return nil
}

func mistralKey(value string) bool {
	return opaqueToken(value) && len(value) <= 512 && !ContainsHandle(value)
}

func (a mistralAdapter) roundTrip(req *http.Request, transport, exchange http.RoundTripper) (*http.Response, bool, error) {
	b := a.b
	if exactOrigin(req, mistralConsoleHost) && req.URL.RawQuery == "" && req.URL.RawPath == "" && mistralExchangePath.MatchString(req.URL.Path) {
		response, err := b.mistralExchange(req, exchange)
		return response, true, err
	}
	authorization := req.Header.Values("Authorization")
	if len(authorization) != 1 || subtle.ConstantTimeCompare([]byte(authorization[0]), []byte("Bearer "+b.mistral.Key)) != 1 {
		return nil, false, nil
	}
	if !slices.ContainsFunc(mistralReleaseHosts, func(host string) bool { return exactOrigin(req, host) }) {
		return nil, true, ErrDenied
	}
	outgoing := req.Clone(req.Context())
	var key string
	err := b.locked(func() error {
		g, err := b.readMistralGrant()
		if err != nil {
			return err
		}
		if g == nil {
			return ErrDenied
		}
		if g.Status != "ready" {
			return ErrUncertain
		}
		key = g.Key
		return nil
	})
	if err != nil {
		return nil, true, err
	}
	outgoing.Header.Set("Authorization", "Bearer "+key)
	response, err := release(req, outgoing, "Authorization", transport, key)
	return response, true, err
}

func (b *Broker) mistralExchange(req *http.Request, transport http.RoundTripper) (*http.Response, error) {
	if req.Method != http.MethodPost || !jsonContent(req.Header) || req.Header.Get("Content-Encoding") != "" || req.Body == nil || req.ContentLength > maxDocument || len(req.Trailer) != 0 || containsHandle(req) {
		return nil, ErrDenied
	}
	defer func() { _ = req.Body.Close() }()
	data, err := boundedRead(req.Body)
	if err != nil {
		return nil, ErrDenied
	}
	var input struct {
		Token    string `json:"exchange_token"`
		Verifier string `json:"code_verifier"`
	}
	if strictJSON(data, &input) != nil || exactKeys(data, "exchange_token", "code_verifier") != nil || !safeText(input.Token, 4096) || !safeText(input.Verifier, 256) {
		return nil, ErrDenied
	}
	var response *http.Response
	err = b.locked(func() error {
		if err := req.Context().Err(); err != nil {
			return ErrDenied
		}
		g, err := b.readMistralGrant()
		if err != nil {
			return err
		}
		if g != nil && g.Status == "ready" {
			return ErrDenied // Fresh sign-ins only; reset the identity to switch.
		}
		// Persist intent BEFORE dispatch: the exchange token is single-use.
		if err := b.writeMistralGrant(&mistralGrant{Schema: 1, Identity: b.config.Identity, Status: "uncertain"}); err != nil {
			return err
		}
		payload, err := json.Marshal(input)
		if err != nil {
			return ErrState
		}
		// The exchange token is consumed once dispatched; a client that gives
		// up must not cancel an exchange the provider may complete.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), exchangeTimeout)
		defer cancel()
		outgoing := req.Clone(ctx)
		outgoing.Body = io.NopCloser(bytes.NewReader(payload))
		outgoing.GetBody = nil
		outgoing.ContentLength = int64(len(payload))
		outgoing.Header = http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}, "Accept-Encoding": {"identity"}}
		if agent := req.Header.Get("User-Agent"); agent != "" && safeText(agent, 256) {
			outgoing.Header.Set("User-Agent", agent)
		}
		upstream, err := transport.RoundTrip(outgoing)
		if err != nil {
			if upstream != nil && upstream.Body != nil {
				_ = upstream.Body.Close()
			}
			return ErrUncertain
		}
		body, err := responseBody(upstream)
		if err != nil {
			return ErrResponse
		}
		if upstream.StatusCode != http.StatusOK {
			response, err = mistralError(upstream.StatusCode, body)
			return err
		}
		var result struct {
			Key string `json:"api_key"` // #nosec G117 -- captured provider key is never forwarded to the workload.
		}
		if strictJSON(body, &result) != nil || exactKeys(body, "api_key") != nil || !mistralKey(result.Key) {
			return ErrResponse
		}
		if err := b.writeMistralGrant(&mistralGrant{Schema: 1, Identity: b.config.Identity, Status: "ready", CommittedAt: b.now().UTC(), Key: result.Key}); err != nil {
			return err
		}
		response, err = projectedResponse(http.StatusOK, map[string]string{"api_key": b.mistral.Key})
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// mistralError keeps only Vibe's numeric error code, which selects its
// message; descriptions could reflect bootstrap material.
func mistralError(status int, body []byte) (*http.Response, error) {
	if status < 400 || status > 499 {
		return nil, ErrResponse
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil, ErrResponse
	}
	projected := map[string]any{}
	var code int
	if raw, ok := fields["error_code"]; ok && json.Unmarshal(raw, &code) == nil && code > 0 && code < 100000 {
		projected["error_code"] = code
	}
	return projectedResponse(status, projected)
}

// mistralView renders Vibe's .env with the handle in place of the key.
func (b *Broker) mistralView() ([]byte, error) {
	if b.mistral == nil {
		return nil, nil
	}
	g, err := b.readMistralGrant()
	if err != nil || g == nil || g.Status != "ready" {
		return nil, err
	}
	return []byte("MISTRAL_API_KEY='" + strings.ReplaceAll(b.mistral.Key, "'", "") + "'\n"), nil
}
