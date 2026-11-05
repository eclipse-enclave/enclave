// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestParseStopPeerGroup(t *testing.T) {
	res, err := Parse([]string{"stop", "--peer-group", "review"}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "stop" || res.Options.PeerGroup != "review" || res.Sources.PeerGroup != model.SourceCLI {
		t.Fatalf("action %q, peer group %q from source %v", res.Action, res.Options.PeerGroup, res.Sources.PeerGroup)
	}
	if _, err := Parse([]string{"stop", "--peer-group", "Bad!"}, config.DefaultOptions()); err == nil {
		t.Fatal("an invalid group name must be refused")
	}
}
