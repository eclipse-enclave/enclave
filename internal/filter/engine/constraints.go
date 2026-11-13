// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"context"

	"enclave/internal/filter"
)

// checkConstraint applies one constraint whose value is not "*". The bound
// list must be non-empty and every value must pass.
func (e *Engine) checkConstraint(ctx context.Context, r *route, check constraintCheck, vars map[string][]string) *Denial {
	con := check.constraint
	preset := e.policy.Preset
	if !check.bound {
		return e.forbid(ReasonConstraint, "operation %s cannot be checked against %s", r.Op, check.name)
	}
	values := vars[con.Var]
	if len(values) == 0 {
		return e.forbid(ReasonConstraint, "%s: %s is empty, preset %s only allows listed values", r.Op, check.name, preset)
	}
	switch con.Kind {
	case filter.KindEmail:
		for _, v := range values {
			if !filter.MatchEmail(con.Value.Values, v) {
				return e.forbid(ReasonConstraint, "%s: %s %s is not allowed by preset %s", r.Op, check.name, quote(v), preset)
			}
		}
	case filter.KindValue:
		allowed := setOf(con.Value.Values)
		for _, v := range values {
			if !allowed[v] {
				return e.forbid(ReasonConstraint, "%s: %s %s is not allowed by preset %s", r.Op, check.name, quote(v), preset)
			}
		}
	case filter.KindResource:
		ids := setOf(con.Value.IDs)
		within := setOf(con.Value.Within)
		for _, v := range values {
			if ids[v] || within[v] {
				continue
			}
			res := e.resolvers[con.Resolver]
			if len(within) == 0 || res == nil {
				return e.forbid(ReasonConstraint, "%s: %s %s is not allowed by preset %s", r.Op, check.name, quote(v), preset)
			}
			ok, err := res.within(ctx, v, within)
			if err != nil {
				cause := resolverCause(err)
				return e.forbid(ReasonResolver+":"+cause, "%s: %s %s could not be resolved (%s), so it is not allowed", r.Op, check.name, quote(v), cause)
			}
			if !ok {
				return e.forbid(ReasonConstraint, "%s: %s %s is not inside a location allowed by preset %s", r.Op, check.name, quote(v), preset)
			}
		}
	default:
		return e.forbid(ReasonConstraint, "%s: unknown constraint kind", r.Op)
	}
	return nil
}

func setOf(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}
