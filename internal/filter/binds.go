// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"fmt"
	"sort"
)

// Bind step operations.
const (
	StepPath      = "path"
	StepQuery     = "query"
	StepHeader    = "header"
	StepBody      = "body"
	StepResponse  = "response"
	StepMultipart = "multipart"
	StepJSON      = "json"
	StepBase64URL = "base64url"
	StepRFC822    = "rfc822"
	StepSplit     = "split"
)

// rfc822 step modes. RFC822Recipients reads the fields a message is
// delivered to. RFC822Destinations adds the fields that direct replies,
// receipts, and bounces, such as Reply-To.
const (
	RFC822Recipients   = "recipients"
	RFC822Destinations = "destinations"
)

// BindStep is one step of a bind pipeline. Arg is the step's argument: a
// capture, parameter, or header name, a content type prefix, a JSON path, an
// rfc822 mode, or a split separator. Steps without an argument leave it empty.
// Index selects a multipart part by position instead of Arg.
type BindStep struct {
	Op    string `json:"op"`
	Arg   string `json:"arg,omitempty"`
	Index *int   `json:"index,omitempty"`
}

// MaxMultipartParts bounds the parts of an inspected multipart body.
const MaxMultipartParts = 64

var sourceSteps = map[string]bool{StepPath: true, StepQuery: true, StepHeader: true, StepBody: true, StepResponse: true}

var transformSteps = map[string]bool{StepMultipart: true, StepJSON: true, StepBase64URL: true, StepRFC822: true, StepSplit: true}

// StepTakesArg reports whether op needs an argument. Unknown ops report false.
func StepTakesArg(op string) bool {
	switch op {
	case StepBody, StepResponse, StepBase64URL:
		return false
	default:
		return sourceSteps[op] || transformSteps[op]
	}
}

// KnownStep reports whether op names a bind step.
func KnownStep(op string) bool { return sourceSteps[op] || transformSteps[op] }

// IsResponseBind reports whether the pipeline reads the response body.
func IsResponseBind(steps []BindStep) bool {
	return len(steps) > 0 && steps[0].Op == StepResponse
}

// ReadsRequestBody reports whether the pipeline reads the request body.
func ReadsRequestBody(steps []BindStep) bool {
	return len(steps) > 0 && steps[0].Op == StepBody
}

// ValidateBind checks a pipeline. captures holds the route's path capture
// names. The error names the offending step index.
func ValidateBind(steps []BindStep, captures map[string]bool) error {
	if len(steps) == 0 {
		return fmt.Errorf("must have at least one step")
	}
	for i, step := range steps {
		if !KnownStep(step.Op) {
			return fmt.Errorf("[%d]: unknown step %q (want one of %s)", i, step.Op, stepList())
		}
		if i == 0 && !sourceSteps[step.Op] {
			return fmt.Errorf("[%d]: %s cannot start a bind, start with path, query, header, body, or response", i, step.Op)
		}
		if i > 0 && sourceSteps[step.Op] {
			return fmt.Errorf("[%d]: %s can only be the first step", i, step.Op)
		}
		if step.Index != nil && step.Op != StepMultipart {
			return fmt.Errorf("[%d]: only multipart takes an index", i)
		}
		if step.Op == StepMultipart && step.Index != nil {
			if step.Arg != "" {
				return fmt.Errorf("[%d]: multipart takes a content type prefix or an index, not both", i)
			}
			if *step.Index < 0 || *step.Index >= MaxMultipartParts {
				return fmt.Errorf("[%d]: multipart index must be between 0 and %d", i, MaxMultipartParts-1)
			}
		}
		if !StepTakesArg(step.Op) {
			if step.Arg != "" {
				return fmt.Errorf("[%d]: %s takes no argument, use {}", i, step.Op)
			}
			continue
		}
		if step.Arg == "" && (step.Op != StepMultipart || step.Index == nil) {
			return fmt.Errorf("[%d]: %s needs an argument", i, step.Op)
		}
		switch step.Op {
		case StepPath:
			if !captures[step.Arg] {
				return fmt.Errorf("[%d]: path %q is not a capture of the route path", i, step.Arg)
			}
		case StepQuery:
			if !ValidQueryParamName(step.Arg) {
				return fmt.Errorf("[%d]: query parameter name %q must use only %s", i, step.Arg, paramNameRule)
			}
		case StepHeader:
			if err := ValidateHeaderName(step.Arg); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		case StepMultipart:
			if prev := steps[i-1].Op; prev != StepBody && prev != StepResponse {
				return fmt.Errorf("[%d]: multipart must directly follow body or response", i)
			}
			if hasControl(step.Arg) {
				return fmt.Errorf("[%d]: multipart content type prefix contains control characters", i)
			}
		case StepJSON:
			if _, err := ParseJSONPath(step.Arg); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		case StepRFC822:
			if step.Arg != RFC822Recipients && step.Arg != RFC822Destinations {
				return fmt.Errorf("[%d]: rfc822 mode must be %q or %q", i, RFC822Recipients, RFC822Destinations)
			}
		case StepSplit:
			if hasControl(step.Arg) && step.Arg != "\n" && step.Arg != "\t" {
				return fmt.Errorf("[%d]: split separator contains control characters", i)
			}
		}
	}
	return nil
}

func stepList() string {
	ops := make([]string, 0, len(sourceSteps)+len(transformSteps))
	for op := range sourceSteps {
		ops = append(ops, op)
	}
	for op := range transformSteps {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	out := ""
	for i, op := range ops {
		if i > 0 {
			out += ", "
		}
		out += op
	}
	return out
}

// SortedKeys returns the keys of m in sorted order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
