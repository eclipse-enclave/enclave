// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"io"
	"strings"

	"enclave/internal/model"
	"enclave/internal/network"
)

// networkPolicySchemaVersion versions the `enclave network print --json`
// envelope, an integration contract: add fields, never rename or remove them.
const networkPolicySchemaVersion = "1"

const networkPolicyNoteHostsFromCredential = "Hosts selected at session start through serviceAuth.hostsFromCredential are not listed."

type networkPolicyEnvelope struct {
	SchemaVersion string                `json:"schemaVersion"`
	Kind          string                `json:"kind"`
	Tool          *string               `json:"tool"`
	Project       *networkPolicyProject `json:"project"`
	Mode          *string               `json:"mode"`
	Domains       []string              `json:"domains"`
	Denied        []string              `json:"denied"`
	Notes         []string              `json:"notes"`
	Error         string                `json:"error"`
}

type networkPolicyProject struct {
	Dir  string `json:"dir"`
	Hash string `json:"hash"`
}

// writeNetworkPolicyJSON reports the allowlist a run with the input's options
// enforces.
func writeNetworkPolicyJSON(w io.Writer, input *CommandInput) int {
	envelope := newNetworkPolicyEnvelope(&input.Options.Tool, networkPolicyProjectOf(input.Ctx.Project()))
	return writeNetworkPolicyEnvelope(w, envelope, fillNetworkPolicyEnvelope(&envelope, input))
}

func newNetworkPolicyEnvelope(tool *string, project *networkPolicyProject) networkPolicyEnvelope {
	return networkPolicyEnvelope{
		SchemaVersion: networkPolicySchemaVersion,
		Kind:          "network-policy",
		Tool:          tool,
		Project:       project,
		Domains:       []string{},
		Denied:        []string{},
		Notes:         []string{},
	}
}

// networkPolicyProjectOf is nil when the project could not be resolved.
func networkPolicyProjectOf(project model.Project, err error) *networkPolicyProject {
	if err != nil {
		return nil
	}
	return &networkPolicyProject{Dir: project.Dir, Hash: project.Hash}
}

// writeNetworkPolicyEnvelope writes envelope, carrying policyErr in error, and
// returns the exit code: 1 when the policy could not be resolved.
func writeNetworkPolicyEnvelope(w io.Writer, envelope networkPolicyEnvelope, policyErr error) int {
	code := 0
	if policyErr != nil {
		envelope.Error = policyErr.Error()
		code = 1
	}
	if err := encodeIndentedJSON(w, envelope); err != nil {
		return 1
	}
	return code
}

func fillNetworkPolicyEnvelope(envelope *networkPolicyEnvelope, input *CommandInput) error {
	policyCtx, err := resolvePrintPolicyContext(input)
	if err != nil {
		return err
	}
	ep := policyCtx.Resolved.Effective
	mode := model.NetworkModeRestricted
	if strings.EqualFold(strings.TrimSpace(ep.Mode), model.NetworkModeUnrestricted) {
		mode = model.NetworkModeUnrestricted
	}
	if mode == model.NetworkModeRestricted {
		domains, denied, err := network.EffectiveRenderDomainsForTool(ep, input.Options.Tool)
		if err != nil {
			return err
		}
		envelope.Domains = append(envelope.Domains, domains...)
		envelope.Denied = append(envelope.Denied, denied...)
		envelope.Notes = append(envelope.Notes, networkPolicyNoteHostsFromCredential)
	}
	envelope.Mode = &mode
	return nil
}
