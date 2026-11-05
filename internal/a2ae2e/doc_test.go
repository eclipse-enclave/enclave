// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package a2ae2e drives the enclave binary against a real engine to check an
// A2A peer group end to end: an orchestrator plus two workers, a plain
// session, a host send, departure acknowledgments, reconcile and teardown.
// The tests carry the docker build tag; without it the package is empty.
//
// Every run uses temporary XDG state, config and cache roots, no API key and
// no Claude login, so no billed request is made: a worker's harness fails
// its task for lack of credentials, which still exercises the whole route
// through the hub and the adapter. The first run builds the slim tool image.
//
//	go test -tags docker ./internal/a2ae2e -v -timeout 30m
//
// ENCLAVE_TEST_ENGINE=podman selects podman; ENCLAVE_TEST_BIN uses a built
// binary instead of building one; ENCLAVE_TEST_A2A_PREFIX names the resources
// (default a2a-e2e, followed by a random suffix).
package a2ae2e
