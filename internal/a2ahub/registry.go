// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"enclave/internal/a2a"
	"enclave/internal/model"
)

// bearerScheme is the security scheme name every hub-served card declares.
const bearerScheme = "bearer"

func peerURL(st *state, name string) string {
	return fmt.Sprintf("http://%s:%d%s%s", a2a.HubDNSName, st.hubPort, a2a.PeerPathPrefix, name)
}

// handleListing serves a2a.PeerListing of the applied revision; workers only.
func (p *Proxy) handleListing(w http.ResponseWriter, r *http.Request) {
	c, herr := p.authenticate(r, a2a.RoleOrchestrator, a2a.RoleHost)
	if herr != nil {
		writeHubError(w, herr)
		return
	}
	defer p.unregister(c)
	st := c.st
	listing := a2a.PeerListing{
		Group:           st.group,
		GroupGeneration: string(st.groupGeneration),
		Revision:        uint64(st.revision),
		Epoch:           p.epoch,
		Peers:           make([]a2a.PeerEntry, 0, len(st.workers)),
	}
	for _, m := range st.workers {
		url := peerURL(st, m.Name)
		listing.Peers = append(listing.Peers, a2a.PeerEntry{
			Name:        m.Name,
			Generation:  string(m.Generation),
			Description: m.Description,
			URL:         url,
			CardURL:     url + a2a.CardPath,
		})
	}
	w.Header().Set("Cache-Control", "max-age=0")
	writeJSON(w, http.StatusOK, listing)
}

// handleCard fetches the worker's card and rewrites the registry fields from
// the snapshot: name, description, skills, the hub-facing interface URL and
// the bearer scheme. Capabilities (queue depth) stay the adapter's.
func (p *Proxy) handleCard(w http.ResponseWriter, r *http.Request, name string) {
	c, herr := p.authenticate(r, a2a.RoleOrchestrator, a2a.RoleHost)
	if herr != nil {
		writeHubError(w, herr)
		return
	}
	defer p.unregister(c)
	if model.SanitizeSessionName(name) != name {
		writeHubError(w, errUnknownMember(""))
		return
	}
	target, herr := p.setTarget(c, name)
	if herr != nil {
		writeHubError(w, herr)
		return
	}
	card, herr := p.fetchCard(c, target)
	if herr != nil {
		writeHubError(w, herr)
		return
	}
	skills := target.Skills
	if skills == nil {
		skills = []a2a.AgentSkill{}
	}
	overrides := map[string]any{
		"name":        target.Name,
		"description": target.Description,
		"skills":      skills,
		"supportedInterfaces": []a2a.AgentInterface{{
			URL:             peerURL(c.st, target.Name),
			ProtocolBinding: a2a.ProtocolBinding,
			ProtocolVersion: a2a.ProtocolVersion,
		}},
		"securitySchemes": map[string]a2a.SecurityScheme{
			bearerScheme: {HTTPAuth: &a2a.HTTPAuthSecurityScheme{Scheme: "Bearer"}},
		},
		"securityRequirements": []a2a.SecurityRequirement{{
			Schemes: map[string]a2a.StringList{bearerScheme: {List: []string{}}},
		}},
	}
	for key, v := range overrides {
		data, err := json.Marshal(v)
		if err != nil {
			writeHubError(w, errUnreachable(target.Name))
			return
		}
		card[key] = data
	}
	w.Header().Set("Cache-Control", "max-age=0")
	writeJSON(w, http.StatusOK, card)
}

func (p *Proxy) fetchCard(c *caller, target *member) (map[string]json.RawMessage, *hubError) {
	ctx, cancel := context.WithTimeout(c.ctx, cardTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adapterURL(target, a2a.CardPath), nil)
	if err != nil {
		return nil, errUnreachable(target.Name)
	}
	a2a.SetProtocolHeaders(req.Header)
	req.Header.Set("Accept", "application/json")
	p.setUpstreamIdentity(req.Header, c, target)
	resp, err := p.client.Do(req) // #nosec G704 -- target address comes from the verified snapshot, never from the request.
	if err != nil {
		if rev := revocationError(c.ctx, c); rev != nil {
			return nil, rev
		}
		return nil, errUnreachable(target.Name)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errUnreachable(target.Name)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCardBytes+1))
	if err != nil {
		return nil, errUnreachable(target.Name)
	}
	var card map[string]json.RawMessage
	if len(data) > maxCardBytes || json.Unmarshal(data, &card) != nil || card == nil {
		return nil, &hubError{err: &a2a.Error{Code: a2a.CodeInvalidAgentResponse, Message: "Invalid agent response"}, status: http.StatusBadGateway}
	}
	return card, nil
}
