// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"reflect"
	"strings"
	"testing"
)

// podman 4.x renders Entrypoint as a single string where Docker and podman 5
// emit an array.
const podman4InspectLine = `{"Id":"abc","Name":"/enclave-gw","Created":"2026-09-11T10:00:00.000000000Z","State":{"Status":"running","Running":true},"Config":{"Image":"localhost/enclave-gateway-claude:latest","Entrypoint":"/usr/local/bin/gateway-entrypoint.sh","Cmd":null,"Labels":{"a":"b"}}}`

func TestDecodeInspectResponsesAcceptsPodman4StringEntrypoint(t *testing.T) {
	results, err := decodeInspectResponses(podman4InspectLine)
	if err != nil {
		t.Fatalf("decodeInspectResponses: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one container, got %d", len(results))
	}
	got := results[0].Config.Entrypoint
	if want := (StrSlice{"/usr/local/bin/gateway-entrypoint.sh"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Entrypoint = %v, want %v", got, want)
	}
	if results[0].Config.Cmd != nil {
		t.Fatalf("null Cmd must decode to nil, got %v", results[0].Config.Cmd)
	}
	if results[0].State == nil || results[0].State.Status != "running" {
		t.Fatalf("state not decoded: %+v", results[0].State)
	}
}

func TestDecodeInspectResponsesAcceptsArrayEntrypoint(t *testing.T) {
	line := `{"Id":"abc","Config":{"Entrypoint":["sh","-c"],"Cmd":["echo","hi"]}}`
	results, err := decodeInspectResponses(line)
	if err != nil {
		t.Fatalf("decodeInspectResponses: %v", err)
	}
	if want := (StrSlice{"sh", "-c"}); !reflect.DeepEqual(results[0].Config.Entrypoint, want) {
		t.Fatalf("Entrypoint = %v, want %v", results[0].Config.Entrypoint, want)
	}
	if want := (StrSlice{"echo", "hi"}); !reflect.DeepEqual(results[0].Config.Cmd, want) {
		t.Fatalf("Cmd = %v, want %v", results[0].Config.Cmd, want)
	}
}

func TestDecodeInspectResponsesReportsSchemaMismatch(t *testing.T) {
	_, err := decodeInspectResponses(`{"Id":"abc","Config":{"Labels":"not-an-object"}}`)
	if err == nil || !strings.Contains(err.Error(), "decode container inspect output") {
		t.Fatalf("expected a decode error, got %v", err)
	}
}
