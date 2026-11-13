// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

// Summary identity.
const (
	SummarySchemaVersion = "1"
	SummaryKind          = "filter-summary"
)

// Summary is the read-only, non-secret view of an active policy exposed inside
// the tool container. It carries neither the placeholder nor any credential.
type Summary struct {
	SchemaVersion string              `json:"schemaVersion"`
	Kind          string              `json:"kind"`
	Filter        string              `json:"filter"`
	Preset        string              `json:"preset"`
	Hosts         []string            `json:"hosts"`
	Confine       []string            `json:"confine,omitempty"`
	Capabilities  []SummaryCapability `json:"capabilities"`
	Counters      []SummaryCounter    `json:"counters,omitempty"`
	// DeniedOperations lists operations the filter always denies.
	DeniedOperations []string `json:"deniedOperations,omitempty"`
}

// SummaryCapability is one capability and whether the preset enables it.
type SummaryCapability struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description,omitempty"`
	Public      bool                         `json:"public,omitempty"`
	Enabled     bool                         `json:"enabled"`
	Constraints map[string]SummaryConstraint `json:"constraints,omitempty"`
	Operations  []string                     `json:"operations,omitempty"`
}

// SummaryConstraint is a constraint and its preset value.
type SummaryConstraint struct {
	Kind  string          `json:"kind"`
	Value ConstraintValue `json:"value"`
}

// SummaryCounter is a counter and its quota. Quota is nil when unlimited.
type SummaryCounter struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Quota       *int64 `json:"quota,omitempty"`
}

// Summary builds the container-facing view of the policy.
func (p *Policy) Summary() Summary {
	ops := map[string][]string{}
	var denied []string
	for _, r := range p.Routes {
		if r.Action == ActionDeny {
			denied = append(denied, r.Op)
			continue
		}
		ops[r.Capability] = append(ops[r.Capability], r.Op)
	}
	s := Summary{
		SchemaVersion:    SummarySchemaVersion,
		Kind:             SummaryKind,
		Filter:           p.Filter,
		Preset:           p.Preset,
		Hosts:            append([]string(nil), p.Hosts...),
		Confine:          append([]string(nil), p.Confine...),
		Capabilities:     []SummaryCapability{},
		DeniedOperations: denied,
	}
	for _, name := range SortedKeys(p.Capabilities) {
		c := p.Capabilities[name]
		sc := SummaryCapability{
			Name:        name,
			Description: c.Description,
			Public:      c.Public,
			Enabled:     c.Enabled,
			Operations:  ops[name],
		}
		for _, cname := range SortedKeys(c.Constraints) {
			if sc.Constraints == nil {
				sc.Constraints = map[string]SummaryConstraint{}
			}
			con := c.Constraints[cname]
			sc.Constraints[cname] = SummaryConstraint{Kind: con.Kind, Value: con.Value}
		}
		s.Capabilities = append(s.Capabilities, sc)
	}
	for _, name := range SortedKeys(p.Counters) {
		c := p.Counters[name]
		s.Counters = append(s.Counters, SummaryCounter{Name: name, Description: c.Description, Unit: c.Unit, Quota: c.Quota})
	}
	return s
}
