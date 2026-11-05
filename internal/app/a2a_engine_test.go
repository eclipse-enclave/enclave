// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/peergroup"
)

func (f *a2aFixture) recordEngine(t *testing.T, name string) {
	t.Helper()
	f.rec.Engine = &peergroup.Engine{Name: name}
	f.saveRecord(t)
}

func captureEngineBinary(t *testing.T) *[]string {
	t.Helper()
	var set []string
	old := a2aSetEngineBinary
	a2aSetEngineBinary = func(name string) { set = append(set, name) }
	t.Cleanup(func() { a2aSetEngineBinary = old })
	return &set
}

func dockerOptions(source model.OptionSource) model.Options {
	opts := config.DefaultOptions()
	opts.Backend = backend.NameDocker
	opts.Sources.Backend = source
	return opts
}

func TestA2AEngineForFollowsTheRecord(t *testing.T) {
	f := newA2AFixture(t)
	set := captureEngineBinary(t)

	// A record without an engine leaves the configured backend.
	opts, err := a2aEngineFor(dockerOptions(model.SourceGlobal), f.state, "review", true)
	if err != nil || opts.Backend != backend.NameDocker || len(*set) != 0 {
		t.Fatalf("no engine: backend %q, %v, set %v", opts.Backend, err, *set)
	}

	f.recordEngine(t, backend.NamePodman)
	opts, err = a2aEngineFor(dockerOptions(model.SourceGlobal), f.state, "review", true)
	if err != nil || opts.Backend != backend.NamePodman || len(*set) != 1 || (*set)[0] != backend.NamePodman {
		t.Fatalf("configured docker: backend %q, %v, set %v", opts.Backend, err, *set)
	}

	for _, tc := range []struct {
		name      string
		source    model.OptionSource
		canSwitch bool
	}{
		{"explicit --backend", model.SourceCLI, true},
		{"caller cannot switch", model.SourceGlobal, false},
	} {
		_, err := a2aEngineFor(dockerOptions(tc.source), f.state, "review", tc.canSwitch)
		if err == nil || !strings.Contains(err.Error(), "runs on podman, not docker; pass --backend podman") {
			t.Fatalf("%s: error = %v", tc.name, err)
		}
	}
	if len(*set) != 1 {
		t.Fatalf("binary switched on a refusal: %v", *set)
	}

	opts = dockerOptions(model.SourceCLI)
	opts.Backend = backend.NamePodman
	if got, err := a2aEngineFor(opts, f.state, "review", false); err != nil || got.Backend != backend.NamePodman {
		t.Fatalf("matching engine: backend %q, %v", got.Backend, err)
	}
}

func TestRunA2AHostVerbUsesTheGroupEngine(t *testing.T) {
	f := newA2AFixture(t)
	f.recordEngine(t, backend.NamePodman)
	captureEngineBinary(t)
	var out bytes.Buffer
	var seen []string
	oldHome, oldStdout, oldAccess := a2aResolveHome, a2aStdout, a2aNewHostAccess
	a2aResolveHome = func() (string, error) { return f.home, nil }
	a2aStdout = &out
	a2aNewHostAccess = func(_ context.Context, opts model.Options, _ a2aState) (a2aHostAccess, error) {
		seen = append(seen, opts.Backend)
		return nil, errors.New("stop here")
	}
	t.Cleanup(func() { a2aResolveHome, a2aStdout, a2aNewHostAccess = oldHome, oldStdout, oldAccess })
	req := &cli.A2ARequest{Verb: cli.A2AVerbSend, Peer: "worker-1", Text: "hi", JSON: true}

	opts := dockerOptions(model.SourceGlobal)
	opts.PeerGroup = "review"
	runA2A(opts, req)
	if len(seen) != 1 || seen[0] != backend.NamePodman {
		t.Fatalf("host access built for %v, want podman", seen)
	}

	out.Reset()
	opts.Sources.Backend = model.SourceCLI
	if code := runA2A(opts, req); code != 1 || len(seen) != 1 || !strings.Contains(out.String(), "pass --backend podman") {
		t.Fatalf("explicit docker: exit %d, access %v, out %s", code, seen, out.String())
	}
}

func TestRunStopPeerGroupRefusesAnotherEngine(t *testing.T) {
	f := newA2AFixture(t)
	f.recordEngine(t, backend.NamePodman)
	lc := &fakeLifecycle{}
	oldHome, oldLifecycle := a2aResolveHome, a2aNewLifecycle
	a2aResolveHome = func() (string, error) { return f.home, nil }
	a2aNewLifecycle = func(context.Context, model.Options, string) (a2aLifecycle, *backenddocker.Backend, error) {
		return lc, nil, nil
	}
	t.Cleanup(func() { a2aResolveHome, a2aNewLifecycle = oldHome, oldLifecycle })
	stopped := 0
	stop := func(context.Context, string) error { stopped++; return nil }
	if code := runStopPeerGroup(dockerOptions(model.SourceGlobal), "review", stop); code != 1 || stopped != 0 || len(lc.calls) != 0 {
		t.Fatalf("stop on docker of a podman group: exit %d, stopped %d, calls %v", code, stopped, lc.calls)
	}
	opts := dockerOptions(model.SourceCLI)
	opts.Backend = backend.NamePodman
	if code := runStopPeerGroup(opts, "review", stop); code != 0 || stopped != 3 {
		t.Fatalf("stop on podman: exit %d, stopped %d", code, stopped)
	}
}

func TestReportA2AReconcileSkippedGroup(t *testing.T) {
	var out bytes.Buffer
	res := backenddocker.ReconcileResult{Group: "review", Skipped: "peer group review runs on podman, not docker; use --backend podman"}
	if code := reportA2AReconcile(&out, []backenddocker.ReconcileResult{res}, nil, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := out.String(); got != "review: skipped: peer group review runs on podman, not docker; use --backend podman\n" {
		t.Fatalf("output %q", got)
	}
}
