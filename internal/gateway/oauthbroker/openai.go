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
	"encoding/base64"
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

// ChatGPT sign-in, shared by Codex, OpenCode and pi. All three use this
// client and endpoint; the request is form-encoded or JSON depending on the
// client and grant.
const (
	openaiTokenHost    = "auth.openai.com" // #nosec G101 -- public OAuth host, not a credential.
	openaiTokenPath    = "/oauth/token"    // #nosec G101 -- public OAuth endpoint, not a credential.
	openaiClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openaiReleaseHost  = "chatgpt.com"
	openaiRegistryFile = "openai-session.json"
	// openaiHandleMarker starts the signature segment of JWT-shaped handles
	// and the refresh handle. Clients parse a handle's header and payload,
	// which are the real token's non-secret claims; the signature carries the
	// handle's randomness.
	openaiHandleMarker = "ENCLAVEoai"
)

var openaiGrantFile = protectedauth.GrantFile(protectedauth.AdapterOpenAI)

type openaiToken struct {
	Access  string `json:"access"`       // #nosec G117 -- gateway-private grant state; projections carry handles.
	Refresh string `json:"refresh"`      // #nosec G117 -- gateway-private grant state; projections carry handles.
	ID      string `json:"id,omitempty"` // #nosec G117 -- gateway-private grant state; projections carry handles.
	Type    string `json:"type,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Life    int64  `json:"life"`
}

type openaiGrant struct {
	Schema      int         `json:"schema"`
	Identity    string      `json:"identity"`
	Generation  uint64      `json:"generation"`
	Status      string      `json:"status"`
	CommittedAt time.Time   `json:"committedAt"`
	ExpiresAt   int64       `json:"expiresAt"`
	AccountID   string      `json:"accountId"`
	Token       openaiToken `json:"token"`
}

// openaiRegistry holds a session's handle signatures and refresh handle.
type openaiRegistry struct {
	Schema      int    `json:"schema"`
	Identity    string `json:"identity"`
	Session     string `json:"session"`
	Incarnation string `json:"incarnation"`
	Access      string `json:"access"`
	ID          string `json:"id"`
	Refresh     string `json:"refresh"`
}

type openaiAdapter struct{ b *Broker }

func openaiHandle(kind string) (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return openaiHandleMarker + kind + hex.EncodeToString(value[:]), nil
}

func validOpenAIHandle(value, kind string) bool {
	if !strings.HasPrefix(value, openaiHandleMarker+kind) {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, openaiHandleMarker+kind))
	return err == nil && len(decoded) == 32
}

func (b *Broker) openOpenAI() error {
	path := filepath.Join(b.config.RuntimeDir, openaiRegistryFile)
	var r openaiRegistry
	if err := readPrivateJSON(path, &r); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return ErrState
		}
		r = openaiRegistry{Schema: 1, Identity: b.config.Identity, Session: b.config.Session, Incarnation: b.config.Incarnation}
		for _, handle := range []struct {
			kind   string
			target *string
		}{{"A", &r.Access}, {"I", &r.ID}, {"R", &r.Refresh}} {
			value, err := openaiHandle(handle.kind)
			if err != nil {
				return ErrState
			}
			*handle.target = value
		}
	}
	if r.Schema != 1 || r.Identity != b.config.Identity || r.Session != b.config.Session || r.Incarnation != b.config.Incarnation ||
		!validOpenAIHandle(r.Access, "A") || !validOpenAIHandle(r.ID, "I") || !validOpenAIHandle(r.Refresh, "R") {
		return ErrState
	}
	// Registration precedes any grant commit, as for the Claude adapter.
	if err := b.write(path, r); err != nil {
		return ErrState
	}
	b.openai = &r
	if g, err := b.readOpenAIGrant(); err != nil {
		return err
	} else if g != nil {
		return b.writeOpenAIGrant(g)
	}
	return nil
}

func (b *Broker) readOpenAIGrant() (*openaiGrant, error) {
	var g openaiGrant
	if err := readPrivateJSON(filepath.Join(b.config.StoreDir, openaiGrantFile), &g); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, ErrState
	}
	if g.Schema != 1 || g.Identity != b.config.Identity || (g.Status != "ready" && g.Status != "uncertain") {
		return nil, ErrState
	}
	if g.Status == "ready" && (g.Generation == 0 || g.CommittedAt.Unix() <= 0 || g.ExpiresAt <= g.CommittedAt.Unix() ||
		!jwtShaped(g.Token.Access) || !opaqueToken(g.Token.Refresh) || (g.Token.ID != "" && !jwtShaped(g.Token.ID)) || !safeText(g.AccountID, 256)) {
		return nil, ErrState
	}
	if err := util.SyncDir(b.config.StoreDir); err != nil {
		return nil, ErrState
	}
	return &g, nil
}

func (b *Broker) writeOpenAIGrant(g *openaiGrant) error {
	if err := b.write(filepath.Join(b.config.StoreDir, openaiGrantFile), g); err != nil {
		b.failed = true
		return ErrState
	}
	return nil
}

// jwtShaped accepts a compact JWS whose payload is a JSON object. Clients
// decode the payload without verifying the signature.
func jwtShaped(token string) bool {
	_, ok := jwtClaims(token)
	return ok
}

func jwtClaims(token string) (map[string]json.RawMessage, bool) {
	parts := strings.Split(token, ".")
	if len(token) > 8192 || len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, false
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil || claims == nil {
		return nil, false
	}
	return claims, true
}

// openaiAccount reads the ChatGPT account id the clients derive from a token.
func openaiAccount(token string) string {
	claims, ok := jwtClaims(token)
	if !ok {
		return ""
	}
	var account string
	if raw, ok := claims["https://api.openai.com/auth"]; ok {
		var nested struct {
			Account string `json:"chatgpt_account_id"`
		}
		if json.Unmarshal(raw, &nested) == nil && nested.Account != "" {
			return nested.Account
		}
	}
	if raw, ok := claims["chatgpt_account_id"]; ok && json.Unmarshal(raw, &account) == nil {
		return account
	}
	return ""
}

func openaiExpiry(token string) int64 {
	claims, ok := jwtClaims(token)
	if !ok {
		return 0
	}
	var exp int64
	if raw, ok := claims["exp"]; ok && json.Unmarshal(raw, &exp) == nil {
		return exp
	}
	return 0
}

// handleJWT keeps the real token's header and payload, so clients see the same
// claims, and replaces its signature with the session's handle signature.
func handleJWT(token, signature string) string {
	parts := strings.SplitN(token, ".", 3)
	return parts[0] + "." + parts[1] + "." + signature
}

func (a openaiAdapter) roundTrip(req *http.Request, transport, exchange http.RoundTripper) (*http.Response, bool, error) {
	b := a.b
	if exactOrigin(req, openaiTokenHost) && exactPath(req, openaiTokenPath) {
		response, err := b.openaiExchange(req, exchange)
		return response, true, err
	}
	authorization := req.Header.Values("Authorization")
	if len(authorization) != 1 || !strings.HasPrefix(authorization[0], "Bearer ") {
		return nil, false, nil
	}
	parts := strings.Split(strings.TrimPrefix(authorization[0], "Bearer "), ".")
	if len(parts) != 3 || subtle.ConstantTimeCompare([]byte(parts[2]), []byte(b.openai.Access)) != 1 {
		return nil, false, nil
	}
	if !exactOrigin(req, openaiReleaseHost) {
		return nil, true, ErrDenied
	}
	response, err := b.openaiRelease(req, transport)
	return response, true, err
}

func (b *Broker) openaiRelease(req *http.Request, transport http.RoundTripper) (*http.Response, error) {
	outgoing := req.Clone(req.Context())
	var secrets []string
	err := b.locked(func() error {
		g, err := b.readOpenAIGrant()
		if err != nil {
			return err
		}
		if g == nil {
			return ErrDenied
		}
		if g.Status != "ready" {
			return ErrUncertain
		}
		if g.ExpiresAt <= b.now().Unix() {
			return ErrDenied
		}
		outgoing.Header.Set("Authorization", "Bearer "+g.Token.Access)
		secrets = []string{g.Token.Access, g.Token.Refresh, g.Token.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return release(req, outgoing, "Authorization", transport, secrets...)
}

type openaiRequest struct {
	fields url.Values
	json   bool
}

func (b *Broker) openaiRequest(req *http.Request) (openaiRequest, error) {
	var parsed openaiRequest
	if req.Method != http.MethodPost || req.Body == nil || req.ContentLength > maxDocument || len(req.Trailer) != 0 || req.Header.Get("Content-Encoding") != "" || len(req.Header.Values("Content-Type")) != 1 {
		return parsed, ErrDenied
	}
	defer func() { _ = req.Body.Close() }()
	data, err := boundedRead(req.Body)
	if err != nil || (req.ContentLength >= 0 && req.ContentLength != int64(len(data))) {
		return parsed, ErrDenied
	}
	media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	switch {
	case err != nil:
		return parsed, ErrDenied
	case media == "application/json":
		var fields map[string]string
		if uniqueJSON(data) != nil || json.Unmarshal(data, &fields) != nil {
			return parsed, ErrDenied
		}
		parsed.json = true
		parsed.fields = url.Values{}
		for key, value := range fields {
			parsed.fields.Set(key, value)
		}
	case media == "application/x-www-form-urlencoded":
		parsed.fields, err = url.ParseQuery(string(data))
		if err != nil {
			return parsed, ErrDenied
		}
	default:
		return parsed, ErrDenied
	}
	for key, values := range parsed.fields {
		switch key {
		case "grant_type", "client_id", "code", "redirect_uri", "code_verifier", "refresh_token", "scope":
		default:
			return parsed, ErrDenied
		}
		if len(values) != 1 {
			return parsed, ErrDenied
		}
	}
	// Request headers never carry handles; the refresh handle travels in the
	// declared body field only.
	if parsed.fields.Get("client_id") != openaiClientID || containsHandle(req) {
		return parsed, ErrDenied
	}
	switch parsed.fields.Get("grant_type") {
	case "authorization_code":
		if parsed.fields.Has("refresh_token") || !safeText(parsed.fields.Get("code"), 4096) || !safeText(parsed.fields.Get("code_verifier"), 256) || !safeText(parsed.fields.Get("redirect_uri"), 2048) {
			return parsed, ErrDenied
		}
	case "refresh_token":
		if parsed.fields.Has("code") || parsed.fields.Has("code_verifier") || subtle.ConstantTimeCompare([]byte(parsed.fields.Get("refresh_token")), []byte(b.openai.Refresh)) != 1 {
			return parsed, ErrDenied
		}
	default:
		// The token-exchange grant mints an API key the gateway does not hold.
		return parsed, ErrDenied
	}
	return parsed, nil
}

func uniqueJSON(data []byte) error {
	if len(data) == 0 || len(data) > maxDocument {
		return ErrResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrResponse
	}
	return nil
}

func (b *Broker) openaiExchange(req *http.Request, transport http.RoundTripper) (*http.Response, error) {
	request, err := b.openaiRequest(req)
	if err != nil {
		return nil, err
	}
	refresh := request.fields.Get("grant_type") == "refresh_token"
	var response *http.Response
	err = b.locked(func() error {
		if err := req.Context().Err(); err != nil {
			return ErrDenied
		}
		g, err := b.readOpenAIGrant()
		if err != nil {
			return err
		}
		if refresh {
			if g == nil {
				return ErrDenied
			}
			if g.Status != "ready" {
				return ErrUncertain
			}
			if reuseCommitted(g.ExpiresAt, g.CommittedAt, g.Token.Life, b.now()) {
				response, err = b.openaiProject(g)
				return err
			}
			request.fields.Set("refresh_token", g.Token.Refresh)
		} else if g != nil && g.Status == "ready" {
			return ErrDenied // Fresh logins only; reset the identity to switch.
		}
		pending := openaiGrant{Schema: 1, Identity: b.config.Identity, Status: "uncertain"}
		if g != nil {
			pending = *g
			pending.Status = "uncertain"
		}
		// Persist intent BEFORE dispatch: a lost or failed exchange leaves
		// uncertain state, never permission to resend a consumed token.
		if err := b.writeOpenAIGrant(&pending); err != nil {
			return err
		}
		upstream, err := openaiDispatch(req.Context(), req, request, transport)
		if err != nil {
			return err
		}
		body, err := responseBody(upstream)
		if err != nil {
			return ErrResponse
		}
		if upstream.StatusCode != http.StatusOK {
			response, err = sanitizedError(upstream, body)
			return err
		}
		response, err = b.openaiCommit(body, &pending, refresh)
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func openaiDispatch(ctx context.Context, req *http.Request, request openaiRequest, transport http.RoundTripper) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	outgoing := req.Clone(ctx)
	var payload []byte
	contentType := "application/x-www-form-urlencoded"
	if request.json {
		fields := map[string]string{}
		for key := range request.fields {
			fields[key] = request.fields.Get(key)
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			cancel()
			return nil, ErrState
		}
		payload, contentType = encoded, "application/json"
	} else {
		payload = []byte(request.fields.Encode())
	}
	outgoing.Body = io.NopCloser(bytes.NewReader(payload))
	outgoing.GetBody = nil
	outgoing.ContentLength = int64(len(payload))
	outgoing.Header = http.Header{"Content-Type": {contentType}, "Accept": {"application/json"}, "Accept-Encoding": {"identity"}}
	upstream, err := transport.RoundTrip(outgoing)
	if err != nil {
		cancel()
		if upstream != nil && upstream.Body != nil {
			_ = upstream.Body.Close()
		}
		return nil, ErrUncertain
	}
	upstream.Body = cancelOnClose{ReadCloser: upstream.Body, cancel: cancel}
	return upstream, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	defer c.cancel()
	return c.ReadCloser.Close()
}

// openaiCommit runs under the identity lock and never consults native files.
func (b *Broker) openaiCommit(body []byte, pending *openaiGrant, refresh bool) (*http.Response, error) {
	var fields map[string]json.RawMessage
	if strictJSON(body, &fields) != nil || exactKeys(body, "access_token", "refresh_token", "id_token", "expires_in", "token_type", "scope") != nil {
		return nil, ErrResponse
	}
	var token struct {
		Access  string `json:"access_token"`  // #nosec G117 -- captured provider token is never forwarded to the workload.
		Refresh string `json:"refresh_token"` // #nosec G117 -- captured provider token is never forwarded to the workload.
		ID      string `json:"id_token"`      // #nosec G117 -- captured provider token is never forwarded to the workload.
		Expires *int64 `json:"expires_in"`
		Type    string `json:"token_type"`
		Scope   string `json:"scope"`
	}
	if json.Unmarshal(body, &token) != nil || !jwtShaped(token.Access) {
		return nil, ErrResponse
	}
	if (token.Refresh != "" && !opaqueToken(token.Refresh)) || (token.ID != "" && !jwtShaped(token.ID)) ||
		(token.Expires != nil && (*token.Expires < 1 || *token.Expires > 86400*366)) ||
		(token.Type != "" && !strings.EqualFold(token.Type, "bearer")) || (token.Scope != "" && !safeText(token.Scope, 4096)) {
		return nil, ErrResponse
	}
	if ContainsHandle(string(body)) {
		return nil, ErrResponse
	}
	next := pending.Token
	next.Access = token.Access
	if token.Refresh != "" {
		next.Refresh = token.Refresh
	}
	if token.ID != "" {
		next.ID = token.ID
	}
	if token.Type != "" {
		next.Type = token.Type
	}
	if token.Scope != "" {
		next.Scope = token.Scope
	}
	if !refresh && (token.Refresh == "" || token.ID == "") {
		return nil, ErrResponse
	}
	// The account comes from the tokens just issued; a refresh that names no
	// account keeps the saved one. Reauthentication must not switch the
	// account behind sibling handles.
	account := openaiAccount(token.Access)
	if fromID := openaiAccount(token.ID); fromID != "" {
		if account != "" && account != fromID {
			return nil, ErrResponse
		}
		account = fromID
	}
	if account == "" && pending.Generation != 0 {
		account = pending.AccountID
	}
	if account == "" || (pending.Generation != 0 && account != pending.AccountID) {
		return nil, ErrResponse
	}
	committedAt := b.now().UTC()
	expiresAt := openaiExpiry(next.Access)
	if token.Expires != nil {
		expiresAt = committedAt.Unix() + *token.Expires
	}
	if expiresAt <= committedAt.Unix() {
		expiresAt = committedAt.Unix() + 3600
	}
	next.Life = expiresAt - committedAt.Unix()
	committed := openaiGrant{Schema: 1, Identity: b.config.Identity, Generation: pending.Generation + 1, Status: "ready", CommittedAt: committedAt, ExpiresAt: expiresAt, AccountID: account, Token: next}
	if err := b.writeOpenAIGrant(&committed); err != nil {
		return nil, err
	}
	return b.openaiProject(&committed)
}

// openaiProject renders a token response holding handles only.
func (b *Broker) openaiProject(g *openaiGrant) (*http.Response, error) {
	remaining := g.ExpiresAt - b.now().Unix()
	if remaining < 1 {
		return nil, ErrUncertain
	}
	projected := map[string]any{
		"access_token":  handleJWT(g.Token.Access, b.openai.Access),
		"refresh_token": b.openai.Refresh,
		"expires_in":    remaining,
		"token_type":    "Bearer",
	}
	if g.Token.ID != "" {
		projected["id_token"] = handleJWT(g.Token.ID, b.openai.ID)
	}
	if g.Token.Scope != "" {
		projected["scope"] = g.Token.Scope
	}
	return projectedResponse(http.StatusOK, projected)
}

// openaiProjection is the ready grant's handle form for native views.
type openaiProjection struct {
	Access, Refresh, ID, AccountID string
	ExpiresAt                      int64
	CommittedAt                    time.Time
}

func (b *Broker) openaiReady() (*openaiProjection, error) {
	if b.openai == nil {
		return nil, nil
	}
	g, err := b.readOpenAIGrant()
	if err != nil || g == nil || g.Status != "ready" {
		return nil, err
	}
	projection := &openaiProjection{Access: handleJWT(g.Token.Access, b.openai.Access), Refresh: b.openai.Refresh, AccountID: g.AccountID, ExpiresAt: g.ExpiresAt, CommittedAt: g.CommittedAt}
	if g.Token.ID != "" {
		projection.ID = handleJWT(g.Token.ID, b.openai.ID)
	}
	return projection, nil
}
