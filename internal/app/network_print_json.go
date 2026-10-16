// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"io"

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
	policyCtx, err := resolveSessionPolicyContext(input, true)
	if err != nil {
		return writeNetworkPolicyEnvelope(w, networkPolicyEnvelope{Tool: &input.Options.Tool}, input.Ctx.Project, err)
	}
	ep := policyCtx.Resolved.Effective
	envelope := networkPolicyEnvelope{Tool: &policyCtx.Tool}
	mode := model.NetworkModeUnrestricted
	if ep.Mode != model.NetworkModeUnrestricted {
		mode = model.NetworkModeRestricted
		envelope.Domains, envelope.Denied, err = network.EffectiveRenderDomainsForTool(ep, policyCtx.Tool)
		if err != nil {
			return writeNetworkPolicyEnvelope(w, networkPolicyEnvelope{Tool: &policyCtx.Tool}, input.Ctx.Project, err)
		}
		envelope.Notes = []string{networkPolicyNoteHostsFromCredential}
	}
	envelope.Mode = &mode
	return writeNetworkPolicyEnvelope(w, envelope, input.Ctx.Project, nil)
}

// writeNetworkPolicyEnvelope completes envelope with its constant fields, the
// project when resolveProject succeeds, and policyErr, writes it, and returns
// the exit code: 1 when the policy could not be resolved.
func writeNetworkPolicyEnvelope(w io.Writer, envelope networkPolicyEnvelope, resolveProject func() (model.Project, error), policyErr error) int {
	envelope.SchemaVersion, envelope.Kind = networkPolicySchemaVersion, "network-policy"
	if project, err := resolveProject(); err == nil {
		envelope.Project = &networkPolicyProject{Dir: project.Dir, Hash: project.Hash}
	}
	envelope.Domains = append([]string{}, envelope.Domains...)
	envelope.Denied = append([]string{}, envelope.Denied...)
	envelope.Notes = append([]string{}, envelope.Notes...)
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
