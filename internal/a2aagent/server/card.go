// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"

	"enclave/internal/a2a"
)

const textMode = "text/plain"

// BearerSchemeName keys the card's only security scheme.
const BearerSchemeName = "bearer"

// Card builds the agent card with the hub-facing URL, since the hub never rewrites bodies (P4, P6).
func (s *Server) Card() a2a.AgentCard {
	description := s.cfg.Description
	if description == "" {
		description = "enclave worker " + s.cfg.Member
	}
	skills := s.opts.Skills
	if len(skills) == 0 {
		// The spec requires at least one skill.
		skills = []a2a.AgentSkill{{ID: "general", Name: s.cfg.Member, Description: description, Tags: []string{"enclave"}}}
	}
	version := s.opts.Version
	if version == "" {
		version = "dev"
	}
	streaming := true
	params := map[string]any{}
	raw, _ := json.Marshal(a2a.CardExtensionParams{QueueDepth: s.queueDepth})
	_ = json.Unmarshal(raw, &params)
	return a2a.AgentCard{
		Name:        s.cfg.Member,
		Description: description,
		SupportedInterfaces: []a2a.AgentInterface{{
			URL:             s.peerURL(),
			ProtocolBinding: a2a.ProtocolBinding,
			ProtocolVersion: a2a.ProtocolVersion,
		}},
		Version: version,
		Capabilities: a2a.AgentCapabilities{
			Streaming: &streaming,
			Extensions: []a2a.AgentExtension{{
				URI:         a2a.ExtensionURI,
				Description: "enclave peer group: termination status, ending reason, queue position, retry, principal",
				Required:    true,
				Params:      params,
			}},
		},
		SecuritySchemes: map[string]a2a.SecurityScheme{
			BearerSchemeName: {HTTPAuth: &a2a.HTTPAuthSecurityScheme{Scheme: "Bearer"}},
		},
		SecurityRequirements: []a2a.SecurityRequirement{{
			Schemes: map[string]a2a.StringList{BearerSchemeName: {List: []string{}}},
		}},
		DefaultInputModes:  []string{textMode},
		DefaultOutputModes: []string{textMode},
		Skills:             skills,
	}
}

func (s *Server) peerURL() string {
	host := s.cfg.HubHost
	if host == "" {
		host = a2a.HubDNSName
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.cfg.HubPort)) + a2a.PeerPathPrefix + s.cfg.Member
}

func (s *Server) serveCard(w http.ResponseWriter) {
	data, err := json.Marshal(s.Card())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=0")
	_, _ = w.Write(data)
}
