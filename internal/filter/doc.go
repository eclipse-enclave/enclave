// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package filter holds the compiled form of a request filter: the policy the
// gateway enforces, the secret credential payload that goes with it, and the
// container-facing summary. It also defines the policy language shared by the
// authoring format (package spec) and the evaluator (package engine): path
// templates, the JSONPath subset, bind steps, and constraint values.
//
// Sub-packages:
//   - spec loads filter extensions and presets and compiles them into a Policy.
//   - engine evaluates requests and responses against a Policy.
//   - token mints access tokens from a Credential.
package filter
